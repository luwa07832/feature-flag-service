package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// CreateEnvironment registers a deployment target. Unknown environments are
// rejected by every query and write, which is what makes EnvironmentNotFound
// unambiguous.
func (s *Store) CreateEnvironment(key string) (*Environment, error) {
	createdAt := s.Now()
	_, err := s.db.Exec(
		"INSERT INTO environments(key, created_at) VALUES(?, ?)",
		key, createdAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("insert environment: %w", err)
	}
	return &Environment{Key: key, CreatedAt: createdAt}, nil
}

// CreateFlag registers a feature flag definition. The description and labels
// are validated and normalized by the caller; a freshly created flag has
// updated_at equal to created_at.
func (s *Store) CreateFlag(key, description string, labels []string) (*Flag, error) {
	createdAt := s.Now()
	encoded, err := encodeLabels(labels)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		"INSERT INTO flags(key, created_at, updated_at, description, labels) VALUES(?, ?, ?, ?, ?)",
		key, createdAt, createdAt, description, encoded,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("insert flag: %w", err)
	}
	return &Flag{
		Key:         key,
		Description: description,
		Labels:      copyLabels(labels),
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}, nil
}

// ReplaceFlagDefinition overwrites a flag's description and labels and bumps
// updated_at; created_at is untouched and no configuration version is
// appended. It returns ErrNotFound when the flag does not exist.
func (s *Store) ReplaceFlagDefinition(key, description string, labels []string) (*Flag, error) {
	encoded, err := encodeLabels(labels)
	if err != nil {
		return nil, err
	}
	result, err := s.db.Exec(
		"UPDATE flags SET description = ?, labels = ?, updated_at = ? WHERE key = ?",
		description, encoded, s.Now(), key,
	)
	if err != nil {
		return nil, fmt.Errorf("update flag definition: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update flag definition: %w", err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return s.GetFlag(key)
}

// PutConfigInput carries one configuration version to append.
type PutConfigInput struct {
	FlagKey     string
	Environment string
	Enabled     bool
	Percentage  int
	StartsAt    *int64
	EndsAt      *int64
}

// PutConfig appends a new immutable configuration version for flag+environment.
// Nothing existing is modified or deleted.
func (s *Store) PutConfig(input PutConfigInput) (*ConfigRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := requireRow(tx, "SELECT 1 FROM environments WHERE key = ?", input.Environment); err != nil {
		return nil, err
	}
	if err := requireRow(tx, "SELECT 1 FROM flags WHERE key = ?", input.FlagKey); err != nil {
		return nil, err
	}

	changedAt := s.Now()
	record := ConfigRecord{
		FlagKey:     input.FlagKey,
		Environment: input.Environment,
		Version:     s.newVersion(changedAt),
		Enabled:     input.Enabled,
		Percentage:  input.Percentage,
		StartsAt:    input.StartsAt,
		EndsAt:      input.EndsAt,
		ChangedAt:   changedAt,
	}
	if _, err := tx.Exec(
		`INSERT INTO config_history
			(flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		record.FlagKey, record.Environment, record.Version,
		boolToInt(record.Enabled), record.Percentage,
		record.StartsAt, record.EndsAt, record.ChangedAt,
	); err != nil {
		return nil, fmt.Errorf("insert config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit config: %w", err)
	}
	return &record, nil
}

// DeleteConfig appends a tombstone for the current flag+environment
// configuration. It returns ErrNotFound when no live configuration exists.
func (s *Store) DeleteConfig(flagKey, environment string) (*ConfigRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	current, err := latestRecordTx(tx, flagKey, environment, s.Now())
	if err != nil {
		return nil, err
	}
	if current == nil || current.Tombstone {
		return nil, ErrNotFound
	}

	changedAt := s.Now()
	tombstone := ConfigRecord{
		FlagKey:     flagKey,
		Environment: environment,
		Version:     s.newVersion(changedAt),
		Enabled:     current.Enabled,
		Percentage:  current.Percentage,
		StartsAt:    current.StartsAt,
		EndsAt:      current.EndsAt,
		ChangedAt:   changedAt,
		Tombstone:   true,
	}
	if _, err := tx.Exec(
		`INSERT INTO config_history
			(flag_key, environment, version, enabled, percentage, starts_at, ends_at, changed_at, tombstone)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		tombstone.FlagKey, tombstone.Environment, tombstone.Version,
		boolToInt(tombstone.Enabled), tombstone.Percentage,
		tombstone.StartsAt, tombstone.EndsAt, tombstone.ChangedAt,
	); err != nil {
		return nil, fmt.Errorf("insert tombstone: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tombstone: %w", err)
	}
	return &tombstone, nil
}

// newVersion builds a unique, time-ordered configuration version identifier.
func (s *Store) newVersion(changedAt int64) string {
	seq := s.versionSeq.Add(1) % 10000
	return fmt.Sprintf("cfg-%020d-%04d", changedAt, seq)
}

func requireRow(tx *sql.Tx, query string, arg string) error {
	var one int
	if err := tx.QueryRow(query, arg).Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("lookup: %w", err)
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// encodeLabels stores the label set as a JSON array; labels match
// [a-z0-9_-]{1,32} so the encoding is unambiguous.
func encodeLabels(labels []string) (string, error) {
	raw, err := json.Marshal(copyLabels(labels))
	if err != nil {
		return "", fmt.Errorf("encode labels: %w", err)
	}
	return string(raw), nil
}

func decodeLabels(raw string) ([]string, error) {
	labels := []string{}
	if raw == "" {
		return labels, nil
	}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	if labels == nil {
		labels = []string{}
	}
	return labels, nil
}

func copyLabels(labels []string) []string {
	if len(labels) == 0 {
		return []string{}
	}
	copied := make([]string, len(labels))
	copy(copied, labels)
	return copied
}
