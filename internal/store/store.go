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
	historyExisted, err := tableExists(db, "flag_definition_history")
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if !historyExisted {
		if err := backfillDefinitionHistory(db); err != nil {
			db.Close()
			return nil, err
		}
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
CREATE TABLE IF NOT EXISTS flag_definition_history (
	event_id        INTEGER PRIMARY KEY AUTOINCREMENT,
	flag_key        TEXT NOT NULL,
	changed_at      INTEGER NOT NULL,
	action          TEXT NOT NULL,
	changed_fields  TEXT NOT NULL,
	before          TEXT,
	after           TEXT NOT NULL,
	FOREIGN KEY(flag_key) REFERENCES flags(key)
);
CREATE INDEX IF NOT EXISTS idx_def_history_flag_time
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

// tableExists reports whether a table with the given name is present.
func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect tables: %w", err)
	}
	return true, nil
}

// backfillDefinitionHistory seeds one created event for every flag that
// existed before definition history existed. It runs only when the history
// table was just created, so it never fabricates updates or duplicates
// events on later opens. Each event uses the flag's created_at and the
// current definition as its after snapshot.
func backfillDefinitionHistory(db *sql.DB) error {
	rows, err := db.Query(
		`SELECT key, description, created_at FROM flags
		 ORDER BY created_at ASC, key ASC`,
	)
	if err != nil {
		return fmt.Errorf("backfill definition history: %w", err)
	}
	type flagRow struct {
		key         string
		description string
		createdAt   int64
	}
	var existing []flagRow
	for rows.Next() {
		var row flagRow
		if err := rows.Scan(&row.key, &row.description, &row.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("backfill definition history: %w", err)
		}
		existing = append(existing, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backfill definition history: %w", err)
	}
	rows.Close()

	labels := make(map[string][]string)
	labelRows, err := db.Query(
		"SELECT flag_key, label FROM flag_labels ORDER BY flag_key ASC, label ASC",
	)
	if err != nil {
		return fmt.Errorf("backfill definition history: %w", err)
	}
	for labelRows.Next() {
		var flagKey, label string
		if err := labelRows.Scan(&flagKey, &label); err != nil {
			labelRows.Close()
			return fmt.Errorf("backfill definition history: %w", err)
		}
		labels[flagKey] = append(labels[flagKey], label)
	}
	if err := labelRows.Err(); err != nil {
		return fmt.Errorf("backfill definition history: %w", err)
	}
	labelRows.Close()

	for _, row := range existing {
		after, err := marshalSnapshot(DefinitionSnapshot{
			Description: row.description,
			Labels:      labels[row.key],
		})
		if err != nil {
			return fmt.Errorf("backfill definition history: %w", err)
		}
		fields, err := marshalStringList(createdFields)
		if err != nil {
			return fmt.Errorf("backfill definition history: %w", err)
		}
		if _, err := db.Exec(
			`INSERT INTO flag_definition_history
				(flag_key, changed_at, action, changed_fields, before, after)
			 VALUES(?, ?, 'created', ?, NULL, ?)`,
			row.key, row.createdAt, fields, string(after),
		); err != nil {
			return fmt.Errorf("backfill definition history: %w", err)
		}
	}
	return nil
}
