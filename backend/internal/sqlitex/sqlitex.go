// Package sqlitex opens a service's SQLite database and applies migrations.
//
// Each service owns exactly one database file. The pool is limited to a single
// connection (SQLite allows one writer anyway, and it keeps ":memory:" databases
// coherent in tests). Consequence: never run a query on the *sql.DB while
// iterating another query's rows or while holding a *sql.Tx — read rows into a
// slice first, or use the Tx — otherwise the call waits forever for the
// connection.
package sqlitex

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)
)

//go:embed core/*.sql
var coreFS embed.FS

// Open opens (creating if needed) the SQLite database at path, enables WAL,
// then applies the shared core migrations followed by the service's own.
// serviceMigrations holds files named like "0001_create_x.sql" in its root and
// may be nil. Use path ":memory:" for an in-memory database.
func Open(ctx context.Context, path string, serviceMigrations fs.FS) (*sql.DB, error) {
	dsn := dsnFor(path)
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	core, err := fs.Sub(coreFS, "core")
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(ctx, db, "core", core); err != nil {
		db.Close()
		return nil, err
	}
	if serviceMigrations != nil {
		if err := Migrate(ctx, db, "service", serviceMigrations); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func dsnFor(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	if path == ":memory:" {
		return "file::memory:?" + q.Encode()
	}
	return "file:" + path + "?" + q.Encode()
}

// Migrate applies every not-yet-applied "<version>_<name>.sql" file in fsys, in
// version order, each in its own transaction. Applied versions are tracked per
// scope in the schema_version table so core and service migrations never clash.
func Migrate(ctx context.Context, db *sql.DB, scope string, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		scope      TEXT    NOT NULL,
		version    INTEGER NOT NULL,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL,
		PRIMARY KEY (scope, version)
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("read migrations (%s): %w", scope, err)
	}

	type migration struct {
		version int
		name    string
	}
	var migrations []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, convErr := strconv.Atoi(prefix)
		if !ok || convErr != nil {
			return fmt.Errorf("migration %q: name must look like 0001_description.sql", e.Name())
		}
		migrations = append(migrations, migration{version: v, name: e.Name()})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version == migrations[i-1].version {
			return fmt.Errorf("migrations %q and %q share version %d", migrations[i-1].name, migrations[i].name, migrations[i].version)
		}
	}

	for _, m := range migrations {
		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_version WHERE scope = ? AND version = ?`, scope, m.version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", m.name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := fs.ReadFile(fsys, m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		if err := applyOne(ctx, db, scope, m.version, m.name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, scope string, version int, name, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (scope, version, name, applied_at) VALUES (?, ?, ?, ?)`,
		scope, version, name, time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit()
}
