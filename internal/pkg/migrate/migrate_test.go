package migrate_test

import (
	"context"
	"testing"

	"multimo-backend/internal/pkg/migrate"
	"multimo-backend/internal/testutil"
	"multimo-backend/manifest"
)

func TestUpIsIdempotent(t *testing.T) {
	db := testutil.DB(t) // already migrated once
	ctx := context.Background()

	again, err := migrate.Up(ctx, db, manifest.Migrations, migrate.Vars{"EMBEDDING_DIM": 1024, "VISION_EMBEDDING_DIM": 1152})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second run applied %v, want nothing", again)
	}

	for _, table := range []string{"assets", "tasks", "keyframes", "detections", "doc_chunks", "ai_call_log"} {
		v, err := db.GetValue(ctx, "SELECT to_regclass(?) IS NOT NULL", table)
		if err != nil || !v.Bool() {
			t.Errorf("table %s missing (err %v)", table, err)
		}
	}
	dim, err := db.GetValue(ctx, `SELECT atttypmod FROM pg_attribute
		WHERE attrelid = to_regclass('keyframes') AND attname = 'embedding'`)
	if err != nil || dim.Int() != 1024 {
		t.Errorf("embedding dimension = %v (err %v), want 1024", dim, err)
	}
}
