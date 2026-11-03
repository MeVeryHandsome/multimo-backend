// Package pipeline runs asset processing tasks from the durable tasks table
// on the worker pool.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/pkg/workerpool"
)

type Task struct {
	ID          int64
	AssetID     int64
	Kind        string
	Attempts    int
	MaxAttempts int
}

// Reporter updates a running task's progress (0..1) and status message.
type Reporter func(progress float64, message string)

type Handler func(ctx context.Context, t *Task, report Reporter) error

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the task fails at once instead of being retried.
func Permanent(err error) error { return permanentError{err} }

// deferError asks for the task to run again later without counting an attempt.
type deferError struct {
	after  time.Duration
	reason string
}

func (e deferError) Error() string { return "deferred: " + e.reason }

// Defer re-queues the task after d without spending an attempt, for work
// that waits on another task (e.g. a basemap still being indexed). A task is
// deferred at most maxDefers times before it runs out of patience.
func Defer(d time.Duration, reason string) error { return deferError{d, reason} }

const maxDefers = 120

// IsPermanent reports whether err (or anything it wraps) came from Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type Dispatcher struct {
	db        gdb.DB
	pool      *workerpool.Pool
	poll      time.Duration
	retryBase time.Duration
	wake      chan struct{}

	mu       sync.RWMutex
	handlers map[string]Handler
}

func New(db gdb.DB, pool *workerpool.Pool) *Dispatcher {
	pool.OnPanic = func(v any, stack []byte) {
		g.Log().Errorf(context.Background(), "task panic: %v\n%s", v, stack)
	}
	return &Dispatcher{db: db, pool: pool, poll: 2 * time.Second, retryBase: 5 * time.Second,
		wake: make(chan struct{}, 1), handlers: map[string]Handler{}}
}

// SetTiming overrides the poll interval and retry backoff base (tests).
func (d *Dispatcher) SetTiming(poll, retryBase time.Duration) { d.poll, d.retryBase = poll, retryBase }

func (d *Dispatcher) Register(kind string, h Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[kind] = h
}

func (d *Dispatcher) kinds() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.handlers))
	for k := range d.handlers {
		out = append(out, k)
	}
	return out
}

// Enqueue schedules (or reschedules) kind for an asset. A task that is
// currently running is left alone so it never runs twice concurrently.
func (d *Dispatcher) Enqueue(ctx context.Context, assetID int64, kind string) error {
	_, err := d.db.Exec(ctx, `
		INSERT INTO tasks (asset_id, kind) VALUES (?, ?)
		ON CONFLICT (asset_id, kind) DO UPDATE
		SET status = 'pending', attempts = 0, defers = 0, progress = 0, message = NULL, error = NULL,
		    run_after = now(), started_at = NULL, finished_at = NULL, updated_at = now()
		WHERE tasks.status <> 'running'`, assetID, kind)
	if err == nil {
		d.Wake()
	}
	return err
}

// Wake makes Run look for work immediately.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// RecoverStale returns tasks left 'running' by a previous process to the
// queue. Call once at startup, before Run (single backend instance).
func (d *Dispatcher) RecoverStale(ctx context.Context) (int64, error) {
	res, err := d.db.Exec(ctx, `UPDATE tasks SET status = 'pending', run_after = now(), updated_at = now()
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Run claims tasks while the pool has free slots, until ctx ends; then it
// waits for running tasks to finish.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		for d.pool.Running() < d.pool.Size() {
			t, err := d.claim(ctx)
			if err != nil {
				if ctx.Err() == nil {
					g.Log().Warningf(ctx, "claim task: %v", err)
				}
				break
			}
			if t == nil {
				break
			}
			if err := d.pool.Go(ctx, func() { d.execute(ctx, t) }); err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			d.pool.Wait()
			return
		case <-d.wake:
		case <-time.After(d.poll):
		}
	}
}

func (d *Dispatcher) claim(ctx context.Context) (*Task, error) {
	kinds := d.kinds()
	if len(kinds) == 0 {
		return nil, nil
	}
	rec, err := d.db.GetOne(ctx, `
		UPDATE tasks SET status = 'running', attempts = attempts + 1, started_at = now(), updated_at = now()
		WHERE id = (
			SELECT id FROM tasks
			WHERE status = 'pending' AND run_after <= now() AND kind IN (?)
			ORDER BY id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, asset_id, kind, attempts, max_attempts`, kinds)
	if err != nil || rec.IsEmpty() {
		return nil, err
	}
	return &Task{ID: rec["id"].Int64(), AssetID: rec["asset_id"].Int64(), Kind: rec["kind"].String(),
		Attempts: rec["attempts"].Int(), MaxAttempts: rec["max_attempts"].Int()}, nil
}

func (d *Dispatcher) execute(ctx context.Context, t *Task) {
	defer d.Wake() // a slot is free again
	d.mu.RLock()
	h := d.handlers[t.Kind]
	d.mu.RUnlock()

	report := func(p float64, msg string) {
		d.db.Exec(ctx, `UPDATE tasks SET progress = ?, message = ?, updated_at = now() WHERE id = ?`, p, msg, t.ID) //nolint:errcheck
	}
	start := time.Now()
	err := func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		return h(ctx, t, report)
	}()

	bg := context.WithoutCancel(ctx)
	if err == nil {
		d.db.Exec(bg, `UPDATE tasks SET status = 'done', progress = 1, error = NULL, finished_at = now(), updated_at = now()
			WHERE id = ?`, t.ID) //nolint:errcheck
		g.Log().Infof(ctx, "task %d %s asset=%d done in %s", t.ID, t.Kind, t.AssetID, time.Since(start).Round(time.Millisecond))
		return
	}

	if ctx.Err() != nil {
		// Interrupted by shutdown: put it back without spending an attempt.
		d.db.Exec(bg, `UPDATE tasks SET status = 'pending', attempts = greatest(attempts - 1, 0), run_after = now(),
			updated_at = now() WHERE id = ?`, t.ID) //nolint:errcheck
		return
	}
	var def deferError
	if errors.As(err, &def) {
		res, _ := d.db.GetValue(bg, `UPDATE tasks SET status = 'pending', attempts = attempts - 1,
			defers = defers + 1, message = ?, run_after = now() + make_interval(secs => ?), updated_at = now()
			WHERE id = ? RETURNING defers`, def.reason, def.after.Seconds(), t.ID)
		if res.Int() > maxDefers {
			d.db.Exec(bg, `UPDATE tasks SET status = 'failed', error = ?, finished_at = now() WHERE id = ?`,
				"gave up waiting: "+def.reason, t.ID) //nolint:errcheck
		}
		return
	}
	var perm permanentError
	if errors.As(err, &perm) || t.Attempts >= t.MaxAttempts {
		d.db.Exec(bg, `UPDATE tasks SET status = 'failed', error = ?, finished_at = now(), updated_at = now()
			WHERE id = ?`, err.Error(), t.ID) //nolint:errcheck
		g.Log().Warningf(ctx, "task %d %s asset=%d failed: %v", t.ID, t.Kind, t.AssetID, err)
		return
	}
	delay := d.retryBase << (t.Attempts - 1)
	d.db.Exec(bg, `UPDATE tasks SET status = 'pending', error = ?, run_after = now() + make_interval(secs => ?),
		updated_at = now() WHERE id = ?`, err.Error(), delay.Seconds(), t.ID) //nolint:errcheck
	g.Log().Warningf(ctx, "task %d %s asset=%d attempt %d failed, retry in %s: %v", t.ID, t.Kind, t.AssetID, t.Attempts, delay, err)
}
