package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gogf/gf/v2/database/gdb"

	"multimo-backend/internal/pkg/workerpool"
	"multimo-backend/internal/testutil"
)

func insertAsset(t *testing.T, db gdb.DB, n int) int64 {
	t.Helper()
	v, err := db.GetValue(context.Background(), `INSERT INTO assets (uid, type, format, filename, storage_path, size_bytes, md5)
		VALUES (?, 'image', 'jpeg', 'a.jpg', '/tmp/a.jpg', 1, ?) RETURNING id`, fmt.Sprintf("u%d", n), fmt.Sprintf("m%d", n))
	if err != nil {
		t.Fatal(err)
	}
	return v.Int64()
}

func waitFor(t *testing.T, db gdb.DB, cond string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := db.GetValue(context.Background(), "SELECT count(*) FROM tasks WHERE "+cond)
		if v.Int() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, _ := db.GetValue(context.Background(), "SELECT count(*) FROM tasks WHERE "+cond)
	t.Fatalf("timed out waiting for %d tasks where %s (have %d)", want, cond, v.Int())
}

func TestDispatcherRetriesBoundsAndFails(t *testing.T) {
	db := testutil.DB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := New(db, workerpool.New(2))
	d.SetTiming(50*time.Millisecond, 10*time.Millisecond)

	var cur, peak atomic.Int32
	var flakyCalls atomic.Int32
	d.Register("work", func(ctx context.Context, task *Task, report Reporter) error {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		report(0.5, "halfway")
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	d.Register("flaky", func(ctx context.Context, task *Task, report Reporter) error {
		if flakyCalls.Add(1) < 3 {
			return errors.New("temporary")
		}
		return nil
	})
	d.Register("broken", func(ctx context.Context, task *Task, report Reporter) error {
		return Permanent(errors.New("unsupported format"))
	})
	d.Register("panics", func(ctx context.Context, task *Task, report Reporter) error { panic("bad") })
	var waits atomic.Int32
	d.Register("waits", func(ctx context.Context, task *Task, report Reporter) error {
		if waits.Add(1) <= 5 { // more defers than max_attempts, none of them counted
			return Defer(10*time.Millisecond, "dependency not ready")
		}
		return nil
	})

	for i := 0; i < 8; i++ {
		if err := d.Enqueue(ctx, insertAsset(t, db, i), "work"); err != nil {
			t.Fatal(err)
		}
	}
	d.Enqueue(ctx, insertAsset(t, db, 100), "flaky")
	d.Enqueue(ctx, insertAsset(t, db, 101), "broken")
	d.Enqueue(ctx, insertAsset(t, db, 102), "panics")
	d.Enqueue(ctx, insertAsset(t, db, 103), "waits")

	go d.Run(ctx)
	waitFor(t, db, "kind = 'work' AND status = 'done' AND progress = 1", 8)
	waitFor(t, db, "kind = 'flaky' AND status = 'done' AND attempts = 3", 1)
	waitFor(t, db, "kind = 'broken' AND status = 'failed' AND attempts = 1", 1)
	waitFor(t, db, "kind = 'panics' AND status = 'failed' AND attempts = 3 AND error LIKE 'panic%'", 1)
	waitFor(t, db, "kind = 'waits' AND status = 'done' AND attempts = 1", 1)
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds pool size 2", peak.Load())
	}
}

func TestEnqueueDoesNotResetRunningTask(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	d := New(db, workerpool.New(1))
	id := insertAsset(t, db, 1)
	d.Enqueue(ctx, id, "k")
	db.Exec(ctx, "UPDATE tasks SET status = 'running', attempts = 1")
	d.Enqueue(ctx, id, "k")
	waitFor(t, db, "status = 'running' AND attempts = 1", 1)

	n, err := d.RecoverStale(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	waitFor(t, db, "status = 'pending'", 1)
}
