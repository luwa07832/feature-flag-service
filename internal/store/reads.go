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

// ListFlags returns every registered flag definition ordered by key.
func (s *Store) ListFlags() ([]Flag, error) {
	rows, err := s.db.Query("SELECT key, created_at FROM flags ORDER BY key ASC")
	if err != nil {
		return nil, fmt.Errorf("list flags: %w", err)
	}
	defer rows.Close()
	var flags []Flag
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.Key, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan flag: %w", err)
		}
		flags = append(flags, f)
	}
	return flags, rows.Err()
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
