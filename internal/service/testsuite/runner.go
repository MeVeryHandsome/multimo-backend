package testsuite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gogf/gf/v2/frame/g"
)

// Runner runs the suite in the background for the HTTP API and keeps the
// reports under Dir/<run id>/.
type Runner struct {
	Suite *Suite
	Dir   string
	Base  context.Context // cancelled on shutdown

	mu      sync.Mutex
	running bool
	status  Status
}

type Status struct {
	Running   bool       `json:"running"`
	RunID     string     `json:"runId,omitempty"`
	Sections  []string   `json:"sections,omitempty"`
	Progress  string     `json:"progress,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
	Last      *RunInfo   `json:"last,omitempty"`
}

// RunInfo summarises one saved report.
type RunInfo struct {
	ID          string    `json:"id"`
	GeneratedAt time.Time `json:"generatedAt"`
	DurationSec float64   `json:"durationSec"`
	Sections    []string  `json:"sections"`
	Passed      int       `json:"passed"`
	Primary     int       `json:"primary"`
	AllPassed   bool      `json:"allPassed"`
	Metrics     []Metric  `json:"metrics"`
}

var ErrRunning = errors.New("评测正在进行中")

// NewRunID names a run directory after its start time.
func NewRunID(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// Start launches a run unless one is already going.
func (r *Runner) Start(sections []string) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return r.status, ErrRunning
	}
	now := time.Now()
	r.running = true
	r.status = Status{Running: true, RunID: NewRunID(now), Sections: sections, StartedAt: &now, Progress: "starting"}
	id := r.status.RunID
	go func() {
		ctx := r.Base
		if ctx == nil {
			ctx = context.Background()
		}
		rep := r.Suite.Run(ctx, sections, func(p string) {
			r.mu.Lock()
			r.status.Progress = p
			r.mu.Unlock()
		})
		err := ctx.Err()
		if err == nil {
			err = rep.Save(filepath.Join(r.Dir, id))
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.running = false
		r.status.Running, r.status.Progress = false, "done"
		if err != nil {
			r.status.Error = err.Error()
			g.Log().Warningf(ctx, "testsuite run %s: %v", id, err)
		} else {
			g.Log().Infof(ctx, "testsuite run %s saved, all passed: %v", id, rep.AllPassed())
		}
	}()
	return r.status, nil
}

// Status reports the current or most recent run.
func (r *Runner) Status() Status {
	r.mu.Lock()
	st := r.status
	r.mu.Unlock()
	if runs := r.List(); len(runs) > 0 {
		st.Last = &runs[0]
	}
	return st
}

// List returns saved runs, newest first.
func (r *Runner) List() []RunInfo {
	entries, _ := os.ReadDir(r.Dir)
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	var out []RunInfo
	for _, id := range ids {
		rep, err := Load(filepath.Join(r.Dir, id))
		if err != nil {
			continue
		}
		out = append(out, Info(id, rep))
	}
	return out
}

// Info summarises a report.
func Info(id string, rep *Report) RunInfo {
	in := RunInfo{ID: id, GeneratedAt: rep.GeneratedAt, DurationSec: rep.DurationSec, Sections: rep.Sections,
		AllPassed: rep.AllPassed(), Metrics: rep.Metrics}
	for _, m := range rep.Metrics {
		if m.Primary {
			in.Primary++
			if m.Pass {
				in.Passed++
			}
		}
	}
	return in
}

// Dir of a run; "latest" (or empty) picks the newest one.
func (r *Runner) RunDir(id string) (string, error) {
	if id == "" || id == "latest" {
		runs := r.List()
		if len(runs) == 0 {
			return "", os.ErrNotExist
		}
		id = runs[0].ID
	}
	if id != filepath.Base(id) || id == "." || id == ".." {
		return "", os.ErrNotExist
	}
	d := filepath.Join(r.Dir, id)
	if _, err := os.Stat(filepath.Join(d, JSONFile)); err != nil {
		return "", os.ErrNotExist
	}
	return d, nil
}
