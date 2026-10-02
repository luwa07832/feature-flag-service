package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// EnvironmentExists reports whether environment was registered (there is no
// environment deletion, so registration is permanent).
func (s *Store) EnvironmentExists(environment string) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM environments WHERE key = ?", environment).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup environment: %w", err)
	}
	return true, nil
}

// ListFlags returns every registered flag definition ordered by key, each
// with its labels ordered lexicographically.
func (s *Store) ListFlags() ([]Flag, error) {
	rows, err := s.db.Query(
		"SELECT key, description, created_at, updated_at FROM flags ORDER BY key ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("list flags: %w", err)
	}
	defer rows.Close()
	flags := make([]Flag, 0)
	for rows.Next() {
		flag, err := scanFlag(rows)
		if err != nil {
			return nil, err
		}
		flag.Labels = []string{}
		flags = append(flags, flag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list flags: %w", err)
	}
	if err := s.attachLabels(flags); err != nil {
		return nil, err
	}
	return flags, nil
}

// GetFlag returns one flag definition with its labels ordered
// lexicographically. It returns ErrNotFound when the flag is unknown.
func (s *Store) GetFlag(flagKey string) (*Flag, error) {
	row := s.db.QueryRow(
		"SELECT key, description, created_at, updated_at FROM flags WHERE key = ?",
		flagKey,
	)
	flag, err := scanFlag(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	labels, err := s.flagLabels(flag.Key)
	if err != nil {
		return nil, err
	}
	flag.Labels = labels
	return &flag, nil
}

// flagLabels returns one flag's labels in lexicographic order.
func (s *Store) flagLabels(flagKey string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT label FROM flag_labels WHERE flag_key = ? ORDER BY label ASC",
		flagKey,
	)
	if err != nil {
		return nil, fmt.Errorf("query labels: %w", err)
	}
	defer rows.Close()
	labels := make([]string, 0)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// attachLabels fills the Labels slice on flags ordered by key; the flags
// slice must itself be ordered by key.
func (s *Store) attachLabels(flags []Flag) error {
	if len(flags) == 0 {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT f.key, l.label
		 FROM flags f
		 JOIN flag_labels l ON l.flag_key = f.key
		 ORDER BY f.key ASC, l.label ASC`,
	)
	if err != nil {
		return fmt.Errorf("query all labels: %w", err)
	}
	defer rows.Close()
	byKey := make(map[string]int, len(flags))
	for i := range flags {
		byKey[flags[i].Key] = i
	}
	for rows.Next() {
		var key, label string
		if err := rows.Scan(&key, &label); err != nil {
			return fmt.Errorf("scan label: %w", err)
		}
		if i, ok := byKey[key]; ok {
			flags[i].Labels = append(flags[i].Labels, label)
		}
	}
	return rows.Err()
}

// flagRowScanner covers both multi-row and single-row flag scans.
type flagRowScanner interface {
	Scan(dest ...any) error
}

func scanFlag(row flagRowScanner) (Flag, error) {
	var flag Flag
	if err := row.Scan(&flag.Key, &flag.Description, &flag.CreatedAt, &flag.UpdatedAt); err != nil {
		return Flag{}, fmt.Errorf("scan flag: %w", err)
	}
	return flag, nil
}

// ConfigHistory returns the append-only version chain for one flag in one
// environment, oldest first, including tombstone records.
func (s *Store) ConfigHistory(flagKey, environment string) ([]ConfigRecord, error) {
	rows, err := s.db.Query(
		`SELECT flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone
		 FROM config_history
		 WHERE flag_key = ? AND environment = ?
		 ORDER BY changed_at ASC, id ASC`,
		flagKey, environment,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	var records []ConfigRecord
	for rows.Next() {
		record, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// EnvironmentChanges returns the append-only version chains across every flag
// in environment, oldest first by (changed_at, insertion order), including
// tombstone records. When flagKey is non-empty the result is limited to that
// flag; callers are responsible for validating identifiers and existence.
func (s *Store) EnvironmentChanges(environment, flagKey string) ([]ConfigRecord, error) {
	query := `SELECT flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone
		 FROM config_history
		 WHERE environment = ?`
	args := []any{environment}
	if flagKey != "" {
		query += " AND flag_key = ?"
		args = append(args, flagKey)
	}
	query += " ORDER BY changed_at ASC, id ASC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query environment changes: %w", err)
	}
	defer rows.Close()
	var records []ConfigRecord
	for rows.Next() {
		record, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// EffectiveConfigsAt restores the latest configuration record per flag within
// environment as of at. Selection rule: for each flag the last record whose
// changed_at is not later than at (ties broken by insertion order). The
// returned map may contain tombstone records; callers treat those as
// unconfigured. Flags that never had a record by at are absent from the map.
func (s *Store) EffectiveConfigsAt(environment string, at int64) (map[string]ConfigRecord, error) {
	rows, err := s.db.Query(
		`SELECT flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone
		 FROM config_history
		 WHERE environment = ? AND changed_at <= ?
		 ORDER BY changed_at ASC, id ASC`,
		environment, at,
	)
	if err != nil {
		return nil, fmt.Errorf("query effective configs: %w", err)
	}
	defer rows.Close()
	effective := make(map[string]ConfigRecord)
	for rows.Next() {
		record, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		// Rows are ordered by (changed_at, id) ascending, so the last row
		// seen per flag is the version to restore.
		effective[record.FlagKey] = record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return effective, nil
}

// LatestConfigAt restores one flag's last configuration record in environment
// as of at: the record with the greatest changed_at not later than at, ties
// broken by insertion order (so a same-instant rewrite wins). It returns nil
// without error when no record exists by at. The returned record may be a
// tombstone; callers treat that as the unconfigured state.
func (s *Store) LatestConfigAt(flagKey, environment string, at int64) (*ConfigRecord, error) {
	row := s.db.QueryRow(
		`SELECT flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone
		 FROM config_history
		 WHERE flag_key = ? AND environment = ? AND changed_at <= ?
		 ORDER BY changed_at DESC, id DESC
		 LIMIT 1`,
		flagKey, environment, at,
	)
	record, err := scanConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// rowScanner is satisfied by both *sql.Rows and *sql.Row scans inside a tx.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfig(row rowScanner) (ConfigRecord, error) {
	var record ConfigRecord
	var enabled, tombstone int
	var startsAt, endsAt sql.NullInt64
	if err := row.Scan(
		&record.FlagKey, &record.Environment, &record.Version,
		&enabled, &record.Percentage, &startsAt, &endsAt,
		&record.ChangedAt, &tombstone,
	); err != nil {
		return ConfigRecord{}, fmt.Errorf("scan config: %w", err)
	}
	record.Enabled = enabled == 1
	record.Tombstone = tombstone == 1
	if startsAt.Valid {
		v := startsAt.Int64
		record.StartsAt = &v
	}
	if endsAt.Valid {
		v := endsAt.Int64
		record.EndsAt = &v
	}
	return record, nil
}

func latestRecordTx(tx *sql.Tx, flagKey, environment string, at int64) (*ConfigRecord, error) {
	row := tx.QueryRow(
		`SELECT flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone
		 FROM config_history
		 WHERE flag_key = ? AND environment = ? AND changed_at <= ?
		 ORDER BY changed_at DESC, id DESC
		 LIMIT 1`,
		flagKey, environment, at,
	)
	record, err := scanConfig(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// FlagExists reports whether flag was registered.
func (s *Store) FlagExists(flagKey string) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM flags WHERE key = ?", flagKey).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup flag: %w", err)
	}
	return true, nil
}
