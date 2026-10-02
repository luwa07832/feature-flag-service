// Package store owns the SQLite file and every write the service performs.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite handle so callers never touch database/sql directly.
type Store struct {
	db  *sql.DB
	now func() time.Time
	// versionSeq disambiguates configuration versions created within the same
	// nanosecond. Configuration history is append-only.
	versionSeq atomic.Uint64
}

// Open prepares the database file and the schema this service needs.
func Open(path string) (*Store, error) {
	return OpenWithClock(path, time.Now)
}

// OpenWithClock is Open with an injectable clock; production uses Open and
// deterministic tests use this entry point.
func OpenWithClock(path string, now func() time.Time) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}, nil
}

// Now returns the current time in the service's unified Unix-nanosecond
// representation.
func (s *Store) Now() int64 { return s.now().UnixNano() }

// Ping reports whether the storage layer is usable.
func (s *Store) Ping() error { return s.db.Ping() }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// ErrNotFound reports a lookup that matched nothing.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists reports a create request for an existing key.
var ErrAlreadyExists = errors.New("already exists")

const schema = `
CREATE TABLE IF NOT EXISTS service_metadata (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS environments (
	key        TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS flags (
	key         TEXT PRIMARY KEY,
	description TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS flag_labels (
	flag_key TEXT NOT NULL,
	label    TEXT NOT NULL,
	PRIMARY KEY(flag_key, label),
	FOREIGN KEY(flag_key) REFERENCES flags(key)
);
CREATE INDEX IF NOT EXISTS idx_flag_labels_label
	ON flag_labels(label, flag_key)
;
CREATE TABLE IF NOT EXISTS config_history (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	flag_key    TEXT NOT NULL,
	environment TEXT NOT NULL,
	version     TEXT NOT NULL UNIQUE,
	enabled     INTEGER NOT NULL,
	percentage  INTEGER NOT NULL,
	starts_at   INTEGER,
	ends_at     INTEGER,
	changed_at  INTEGER NOT NULL,
	tombstone   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_config_env_time
	ON config_history(environment, changed_at, id);
CREATE INDEX IF NOT EXISTS idx_config_flag_env_time
	ON config_history(flag_key, environment, changed_at, id);
`

// migrate upgrades databases created before flag definitions carried
// description, labels and updated_at.
func migrate(db *sql.DB) error {
	if err := ensureColumn(db, "flags", "description", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "flags", "updated_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := db.Exec("UPDATE flags SET updated_at = created_at WHERE updated_at = 0"); err != nil {
		return fmt.Errorf("backfill updated_at: %w", err)
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, decl string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan %s columns: %w", table, err)
	}
	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}
