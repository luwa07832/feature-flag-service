// Package store owns the SQLite file and every write the service performs.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
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
	// configMu serializes every config-history write so a compare-and-swap
	// (conditional replace) is atomic against concurrent PUT/DELETE writes
	// sharing this Store handle.
	configMu sync.Mutex
}

// Open prepares the database file and the schema this service needs.
func Open(path string) (*Store, error) {
	return OpenWithClock(path, time.Now)
}

// OpenWithClock is Open with an injectable clock; production uses Open and
// deterministic tests use this entry point.
func OpenWithClock(path string, now func() time.Time) (*Store, error) {
	// busy_timeout makes concurrent writers wait for the write lock instead
	// of failing immediately with SQLITE_BUSY; configMu below adds the
	// in-process compare-and-swap guarantee.
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
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

// ErrVersionConflict reports a conditional replace whose expected_version no
// longer matches the current live (non-tombstone) configuration version.
var ErrVersionConflict = errors.New("version conflict")

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
CREATE TABLE IF NOT EXISTS flag_definition_history (
	event_id       INTEGER PRIMARY KEY AUTOINCREMENT,
	flag_key       TEXT NOT NULL,
	action         TEXT NOT NULL,
	changed_fields TEXT NOT NULL,
	before_desc    TEXT,
	before_labels  TEXT,
	after_desc     TEXT,
	after_labels   TEXT,
	changed_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_flag_def_history_flag_time
	ON flag_definition_history(flag_key, changed_at, event_id);
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
	if err := backfillDefinitionHistory(db); err != nil {
		return err
	}
	return nil
}

// backfillDefinitionHistory gives every flag that predates definition
// history exactly one synthetic created record. changed_at uses the flag's
// created_at and after holds its current definition; no subsequent edits are
// invented. It is a no-op once a flag already has any history, so reopening
// an upgraded database stays append-only.
func backfillDefinitionHistory(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin definition backfill: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT f.key, f.description, f.created_at
		 FROM flags f
		 WHERE NOT EXISTS (
			SELECT 1 FROM flag_definition_history h WHERE h.flag_key = f.key
		 )
		 ORDER BY f.key ASC`,
	)
	if err != nil {
		return fmt.Errorf("query flags lacking definition history: %w", err)
	}
	type legacyFlag struct {
		key, description string
		createdAt        int64
	}
	var legacy []legacyFlag
	for rows.Next() {
		var lf legacyFlag
		if err := rows.Scan(&lf.key, &lf.description, &lf.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy flag: %w", err)
		}
		legacy = append(legacy, lf)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy flags: %w", err)
	}

	for _, lf := range legacy {
		labels, err := flagLabelsTx(tx, lf.key)
		if err != nil {
			return err
		}
		labelsJSON, err := encodeLabels(labels)
		if err != nil {
			return err
		}
		fieldsJSON, err := encodeFields([]string{"description", "labels"})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO flag_definition_history
				(flag_key, action, changed_fields, before_desc, before_labels, after_desc, after_labels, changed_at)
			 VALUES(?, 'created', ?, NULL, NULL, ?, ?, ?)`,
			lf.key, fieldsJSON, lf.description, labelsJSON, lf.createdAt,
		); err != nil {
			return fmt.Errorf("backfill definition history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit definition backfill: %w", err)
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
