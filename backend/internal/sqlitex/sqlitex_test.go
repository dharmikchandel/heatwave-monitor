package sqlitex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestOpenAppliesCoreAndServiceMigrations(t *testing.T) {
	ctx := context.Background()
	svc := fstest.MapFS{
		"0001_create_things.sql": {Data: []byte(`CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO things (name) VALUES ('a');`)},
		"0002_add_color.sql":     {Data: []byte(`ALTER TABLE things ADD COLUMN color TEXT;`)},
	}
	db, err := Open(ctx, ":memory:", svc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{"outbox", "inbox", "things", "schema_version"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}
	var rows int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM things`).Scan(&rows)
	if rows != 1 {
		t.Errorf("multi-statement migration: want 1 seed row, got %d", rows)
	}
}

func TestMigrateIsIdempotentAndScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "svc.db")
	svc := fstest.MapFS{"0001_x.sql": {Data: []byte(`CREATE TABLE x (id INTEGER);`)}}

	for i := 0; i < 2; i++ { // second open must not re-run (CREATE TABLE would fail)
		db, err := Open(ctx, path, svc)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		var versions int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&versions)
		if versions != 2 { // core 0001 + service 0001 share a version number but not a scope
			t.Errorf("open #%d: want 2 schema_version rows, got %d", i+1, versions)
		}
		db.Close()
	}
}

func TestOpenUsesWAL(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "wal.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestMigrateRejectsBadNamesAndDuplicateVersions(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = Migrate(ctx, db, "bad", fstest.MapFS{"create.sql": {Data: []byte(`SELECT 1;`)}})
	if err == nil || !strings.Contains(err.Error(), "0001_description.sql") {
		t.Errorf("want naming error, got %v", err)
	}
	err = Migrate(ctx, db, "dup", fstest.MapFS{
		"0001_a.sql": {Data: []byte(`SELECT 1;`)},
		"0001_b.sql": {Data: []byte(`SELECT 1;`)},
	})
	if err == nil || !strings.Contains(err.Error(), "share version") {
		t.Errorf("want duplicate version error, got %v", err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	bad := fstest.MapFS{"0001_half.sql": {Data: []byte(`CREATE TABLE half (id INTEGER); THIS IS NOT SQL;`)}}
	if err := Migrate(ctx, db, "svc", bad); err == nil {
		t.Fatal("expected migration error")
	}
	var n int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='half'`).Scan(&n)
	if n != 0 {
		t.Error("partial migration was not rolled back")
	}
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version WHERE scope='svc'`).Scan(&n)
	if n != 0 {
		t.Error("failed migration was recorded as applied")
	}
}
