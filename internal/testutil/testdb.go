// Package testutil holds helpers shared by integration tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	_ "github.com/gogf/gf/contrib/drivers/pgsql/v2"
	"github.com/gogf/gf/v2/database/gdb"

	"multimo-backend/internal/pkg/migrate"
	"multimo-backend/manifest"
)

// DB returns a connection whose search_path points at a fresh schema with all
// migrations applied. The schema is dropped when the test ends. Tests are
// skipped unless POSTGRES_PASSWORD is set (source ../.env first).
func DB(t *testing.T) gdb.DB {
	t.Helper()
	if os.Getenv("POSTGRES_PASSWORD") == "" {
		t.Skip("POSTGRES_PASSWORD not set; skipping database test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())

	admin := open(t, "public")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
		admin.Close(context.Background())                                  //nolint:errcheck
	})

	db := open(t, schema+",public")
	t.Cleanup(func() { db.Close(context.Background()) }) //nolint:errcheck
	if _, err := migrate.Up(ctx, db, manifest.Migrations, migrate.Vars{"EMBEDDING_DIM": 1024, "VISION_EMBEDDING_DIM": 1152}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func open(t *testing.T, searchPath string) gdb.DB {
	t.Helper()
	port, _ := strconv.Atoi(env("POSTGRES_PORT", "5432"))
	db, err := gdb.New(gdb.ConfigNode{
		Type:  "pgsql",
		Host:  env("POSTGRES_HOST", "127.0.0.1"),
		Port:  strconv.Itoa(port),
		User:  env("POSTGRES_USER", "multimo"),
		Pass:  os.Getenv("POSTGRES_PASSWORD"),
		Name:  env("POSTGRES_DB", "multimo"),
		Extra: "sslmode=disable&search_path=" + searchPath,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

// TestdataRoot is the main repo's testdata/ directory.
func TestdataRoot() string {
	if root := os.Getenv("TESTDATA_DIR"); filepath.IsAbs(root) {
		return root
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata"))
}

// Testdata returns the path of a file under the main repo's testdata/, or
// skips the test when it has not been downloaded.
func Testdata(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(TestdataRoot(), rel)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("testdata %s not present (run scripts/prepare_testdata.sh)", rel)
	}
	return p
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
