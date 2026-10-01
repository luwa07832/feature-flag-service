// Package store owns the SQLite file and every write the service performs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite handle so callers never touch database/sql directly.
type Store struct {
	db *sql.DB
}

// ErrInvalidChange records that a write carried a value the schema cannot hold.
// Public handlers map it to their own validation codes.
var ErrInvalidChange = errors.New("invalid flag change")

// ErrEnvironmentNotFound records that an environment has never been written to
// the change history.
var ErrEnvironmentNotFound = errors.New("environment not found")

// FlagRecord is one append-only row of the change history. Timestamps are kept
// as Unix nanoseconds so comparisons are unambiguous instants.
type FlagRecord struct {
	ID              int64
	VersionID       string
	EnvironmentID   string
	FlagID          string
	Enabled         bool
	RolloutPct      int
	HasWindow       bool
	WindowStartNano int64
	WindowEndNano   int64
	ChangedAtNano   int64
	Note            string
}

// WindowStart returns the window start as an instant, or the zero time when the
// record carries no window.
func (r FlagRecord) WindowStart() time.Time {
	if !r.HasWindow {
		return time.Time{}
	}
	return time.Unix(0, r.WindowStartNano).UTC()
}

// WindowEnd returns the window end as an instant, or the zero time when the
// record carries no window.
func (r FlagRecord) WindowEnd() time.Time {
	if !r.HasWindow {
		return time.Time{}
	}
	return time.Unix(0, r.WindowEndNano).UTC()
}

// ChangedAt returns the change instant.
func (r FlagRecord) ChangedAt() time.Time {
	return time.Unix(0, r.ChangedAtNano).UTC()
}

// AppendInput carries a new flag configuration version. Every accepted input
// becomes one immutable history row; the store never updates or deletes rows.
type AppendInput struct {
	EnvironmentID string
	FlagID        string
	Enabled       bool
	RolloutPct    int
	HasWindow     bool
	WindowStart   time.Time
	WindowEnd     time.Time
	ChangedAt     time.Time
	Note          string
}

// Open prepares the database file and the schema this service needs.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Ping reports whether the storage layer is usable.
func (s *Store) Ping() error { return s.db.Ping() }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// AppendFlagChange writes one immutable configuration version and returns the
// stored record, including its version identifier.
func (s *Store) AppendFlagChange(ctx context.Context, in AppendInput) (FlagRecord, error) {
	if err := validateAppend(in); err != nil {
		return FlagRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FlagRecord{}, fmt.Errorf("begin change tx: %w", err)
	}
	defer tx.Rollback()

	var windowStart, windowEnd int64
	if in.HasWindow {
		windowStart = in.WindowStart.UTC().UnixNano()
		windowEnd = in.WindowEnd.UTC().UnixNano()
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO flag_changes
	(version_id, environment_id, flag_id, enabled, rollout_pct, has_window,
	 window_start_nano, window_end_nano, changed_at_nano, note)
VALUES ('', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.EnvironmentID, in.FlagID, in.Enabled, in.RolloutPct, in.HasWindow,
		windowStart, windowEnd, in.ChangedAt.UTC().UnixNano(), in.Note)
	if err != nil {
		return FlagRecord{}, fmt.Errorf("insert flag change: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return FlagRecord{}, fmt.Errorf("read change id: %w", err)
	}
	versionID := fmt.Sprintf("v%d", id)
	if _, err := tx.ExecContext(ctx,
		"UPDATE flag_changes SET version_id = ? WHERE id = ?", versionID, id); err != nil {
		return FlagRecord{}, fmt.Errorf("assign version id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FlagRecord{}, fmt.Errorf("commit change: %w", err)
	}
	rec, err := s.changeByID(ctx, id)
	if err != nil {
		return FlagRecord{}, err
	}
	return rec, nil
}

// History returns every recorded change for a flag in an environment, newest
// change first. An unknown environment or flag yields an empty slice.
func (s *Store) History(ctx context.Context, environmentID, flagID string) ([]FlagRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, version_id, environment_id, flag_id, enabled, rollout_pct, has_window,
       window_start_nano, window_end_nano, changed_at_nano, note
FROM flag_changes
WHERE environment_id = ? AND flag_id = ?
ORDER BY changed_at_nano DESC, id DESC`, environmentID, flagID)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// SnapshotAt restores the configuration of every flag known in an environment
// as of at. For each flag the selected record is the last effective change
// whose changed_at is not later than at.
//
// The returned map only holds flags with an effective configuration at at;
// flagOrder lists every flag ever written to the environment (in first-seen
// order) so callers can render later-added flags as unconfigured instead of
// inheriting a future configuration. ErrEnvironmentNotFound is returned when
// the environment has never been written, or when none of its flags had an
// effective configuration at at.
func (s *Store) SnapshotAt(ctx context.Context, environmentID string, at time.Time) (map[string]FlagRecord, []string, error) {
	exists, err := s.environmentExists(ctx, environmentID)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, ErrEnvironmentNotFound
	}

	flagOrder, err := s.environmentFlags(ctx, environmentID)
	if err != nil {
		return nil, nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT id, version_id, environment_id, flag_id, enabled, rollout_pct, has_window,
       window_start_nano, window_end_nano, changed_at_nano, note
FROM flag_changes
WHERE environment_id = ? AND changed_at_nano <= ?
ORDER BY flag_id ASC, changed_at_nano DESC, id DESC`,
		environmentID, at.UnixNano())
	if err != nil {
		return nil, nil, fmt.Errorf("query snapshot: %w", err)
	}
	records, err := scanRecords(rows)
	if err != nil {
		return nil, nil, err
	}

	snapshot := make(map[string]FlagRecord, len(records))
	for _, rec := range records {
		if _, seen := snapshot[rec.FlagID]; seen {
			continue
		}
		snapshot[rec.FlagID] = rec
	}
	if len(snapshot) == 0 {
		return nil, nil, ErrEnvironmentNotFound
	}
	return snapshot, flagOrder, nil
}

// environmentFlags lists every flag ever written to an environment in
// first-seen order.
func (s *Store) environmentFlags(ctx context.Context, environmentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT flag_id FROM flag_changes
WHERE environment_id = ?
GROUP BY flag_id
ORDER BY MIN(id) ASC`, environmentID)
	if err != nil {
		return nil, fmt.Errorf("query environment flags: %w", err)
	}
	defer rows.Close()
	var flags []string
	for rows.Next() {
		var flagID string
		if err := rows.Scan(&flagID); err != nil {
			return nil, err
		}
		flags = append(flags, flagID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan environment flags: %w", err)
	}
	return flags, nil
}

// LatestFlagConfig returns the newest change for a flag in an environment.
func (s *Store) LatestFlagConfig(ctx context.Context, environmentID, flagID string) (FlagRecord, error) {
	history, err := s.History(ctx, environmentID, flagID)
	if err != nil {
		return FlagRecord{}, err
	}
	if len(history) == 0 {
		return FlagRecord{}, ErrEnvironmentNotFound
	}
	return history[0], nil
}

func (s *Store) environmentExists(ctx context.Context, environmentID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM flag_changes WHERE environment_id = ?)",
		environmentID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("probe environment: %w", err)
	}
	return exists == 1, nil
}

func (s *Store) changeByID(ctx context.Context, id int64) (FlagRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, version_id, environment_id, flag_id, enabled, rollout_pct, has_window,
       window_start_nano, window_end_nano, changed_at_nano, note
FROM flag_changes WHERE id = ?`, id)
	rec, err := scanRecord(row)
	if err != nil {
		return FlagRecord{}, fmt.Errorf("load stored change: %w", err)
	}
	return rec, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(scanner rowScanner) (FlagRecord, error) {
	var rec FlagRecord
	err := scanner.Scan(
		&rec.ID, &rec.VersionID, &rec.EnvironmentID, &rec.FlagID, &rec.Enabled,
		&rec.RolloutPct, &rec.HasWindow, &rec.WindowStartNano, &rec.WindowEndNano,
		&rec.ChangedAtNano, &rec.Note)
	if err != nil {
		return FlagRecord{}, err
	}
	return rec, nil
}

func scanRecords(rows *sql.Rows) ([]FlagRecord, error) {
	defer rows.Close()
	var out []FlagRecord
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan history: %w", err)
	}
	return out, nil
}

func validateAppend(in AppendInput) error {
	switch {
	case in.EnvironmentID == "", in.FlagID == "":
		return fmt.Errorf("%w: environment and flag are required", ErrInvalidChange)
	case in.RolloutPct < 0 || in.RolloutPct > 100:
		return fmt.Errorf("%w: rollout percentage must be in 0..100", ErrInvalidChange)
	case in.HasWindow && (!in.WindowStart.Before(in.WindowEnd)):
		return fmt.Errorf("%w: window start must be before window end", ErrInvalidChange)
	case !in.HasWindow && (!in.WindowStart.IsZero() || !in.WindowEnd.IsZero()):
		return fmt.Errorf("%w: window bounds require a window", ErrInvalidChange)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS service_metadata (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS flag_changes (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	version_id         TEXT NOT NULL,
	environment_id     TEXT NOT NULL,
	flag_id            TEXT NOT NULL,
	enabled            INTEGER NOT NULL,
	rollout_pct        INTEGER NOT NULL,
	has_window         INTEGER NOT NULL,
	window_start_nano  INTEGER NOT NULL DEFAULT 0,
	window_end_nano    INTEGER NOT NULL DEFAULT 0,
	changed_at_nano    INTEGER NOT NULL,
	note               TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_flag_changes_env_time
	ON flag_changes (environment_id, changed_at_nano, id);
CREATE INDEX IF NOT EXISTS idx_flag_changes_env_flag_time
	ON flag_changes (environment_id, flag_id, changed_at_nano, id);
`
