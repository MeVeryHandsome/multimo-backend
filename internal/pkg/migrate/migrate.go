// Package migrate applies the embedded SQL migrations in order, once each.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// lockKey serialises migrations when several backend instances start together.
const lockKey = 724115001

// Vars are placeholders substituted into migration SQL, e.g.
// {"EMBEDDING_DIM": 1024} replaces {{EMBEDDING_DIM}}.
type Vars map[string]int

// Up applies every migration in fsys (sql/*.sql) not yet recorded in
// schema_migrations, after substituting vars.
//
// Each migration runs in its own transaction holding a transaction-scoped
// advisory lock, and re-checks schema_migrations after taking the lock, so
// concurrent instances apply each migration exactly once.
func Up(ctx context.Context, db gdb.DB, fsys fs.FS, vars Vars) ([]string, error) {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := fs.Glob(fsys, "sql/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "sql/"), ".sql")
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return applied, err
		}
		sql := string(raw)
		for k, v := range vars {
			sql = strings.ReplaceAll(sql, "{{"+k+"}}", strconv.Itoa(v))
		}
		if i := strings.Index(sql, "{{"); i >= 0 {
			return applied, fmt.Errorf("migration %s: unresolved placeholder near %q", version, sql[i:min(i+40, len(sql))])
		}
		ran := false
		err = db.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
			if _, err := tx.Exec("SELECT pg_advisory_xact_lock(?)", lockKey); err != nil {
				return err
			}
			v, err := tx.GetValue("SELECT count(*) FROM schema_migrations WHERE version = ?", version)
			if err != nil {
				return err
			}
			if v.Int() > 0 {
				return nil
			}
			if _, err := tx.Exec(sql); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
				return err
			}
			ran = true
			return nil
		})
		if err != nil {
			return applied, fmt.Errorf("migration %s: %w", version, err)
		}
		if ran {
			applied = append(applied, version)
		}
	}
	return applied, nil
}

// WaitAndUp retries until the database accepts connections (compose may start
// the backend before postgres finishes recovery), then applies migrations.
func WaitAndUp(ctx context.Context, db gdb.DB, fsys fs.FS, vars Vars, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		applied, err := Up(ctx, db, fsys, vars)
		if err == nil {
			if len(applied) > 0 {
				g.Log().Infof(ctx, "applied migrations: %v", applied)
			}
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		g.Log().Warningf(ctx, "database not ready (%v), retrying", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
