package store

import (
	"database/sql"
	"fmt"
	"sort"
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

// CreateFlagInput carries a feature flag definition at registration time.
type CreateFlagInput struct {
	Key         string
	Description string
	Labels      []string
}

// CreateFlag registers a feature flag definition. A new flag shares one
// instant between created_at and updated_at.
func (s *Store) CreateFlag(input CreateFlagInput) (*Flag, error) {
	createdAt := s.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		"INSERT INTO flags(key, description, created_at, updated_at) VALUES(?, ?, ?, ?)",
		input.Key, input.Description, createdAt, createdAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("insert flag: %w", err)
	}
	input.Labels = normalizeLabels(input.Labels)
	if err := replaceLabelsTx(tx, input.Key, input.Labels); err != nil {
		return nil, err
	}
	after := &FlagDefinitionSnapshot{
		Description: input.Description,
		Labels:      append([]string(nil), input.Labels...),
	}
	if err := insertDefinitionRecordTx(
		tx, input.Key, DefinitionActionCreated,
		[]string{"description", "labels"}, nil, after, createdAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit flag: %w", err)
	}
	return &Flag{
		Key:         input.Key,
		Description: input.Description,
		Labels:      append([]string(nil), input.Labels...),
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}, nil
}

// UpdateFlagDefinition replaces the description and labels of an existing
// flag as a whole. created_at never moves; updated_at does. It never appends
// configuration history. ErrNotFound is returned for an unknown flag.
func (s *Store) UpdateFlagDefinition(flagKey, description string, labels []string) (*Flag, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var createdAt int64
	var oldDescription string
	err = tx.QueryRow("SELECT created_at, description FROM flags WHERE key = ?", flagKey).
		Scan(&createdAt, &oldDescription)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup flag: %w", err)
	}
	oldLabels, err := flagLabelsTx(tx, flagKey)
	if err != nil {
		return nil, err
	}
	updatedAt := s.Now()
	if _, err := tx.Exec(
		"UPDATE flags SET description = ?, updated_at = ? WHERE key = ?",
		description, updatedAt, flagKey,
	); err != nil {
		return nil, fmt.Errorf("update flag: %w", err)
	}
	labels = normalizeLabels(labels)
	if err := replaceLabelsTx(tx, flagKey, labels); err != nil {
		return nil, err
	}
	before := &FlagDefinitionSnapshot{
		Description: oldDescription,
		Labels:      oldLabels,
	}
	after := &FlagDefinitionSnapshot{
		Description: description,
		Labels:      append([]string(nil), labels...),
	}
	if err := insertDefinitionRecordTx(
		tx, flagKey, DefinitionActionUpdated,
		definitionChangedFields(before, after), before, after, updatedAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit flag update: %w", err)
	}
	return &Flag{
		Key:         flagKey,
		Description: description,
		Labels:      append([]string(nil), labels...),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}

// definitionChangedFields lists the definition fields that actually differ,
// in the fixed description-before-labels order. An identical replacement
// still appends an updated record, with an empty field list.
func definitionChangedFields(before, after *FlagDefinitionSnapshot) []string {
	fields := make([]string, 0, 2)
	if before.Description != after.Description {
		fields = append(fields, "description")
	}
	if !equalLabelSets(before.Labels, after.Labels) {
		fields = append(fields, "labels")
	}
	return fields
}

func equalLabelSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func replaceLabelsTx(tx *sql.Tx, flagKey string, labels []string) error {
	if _, err := tx.Exec("DELETE FROM flag_labels WHERE flag_key = ?", flagKey); err != nil {
		return fmt.Errorf("clear labels: %w", err)
	}
	for _, label := range labels {
		if _, err := tx.Exec(
			"INSERT INTO flag_labels(flag_key, label) VALUES(?, ?)",
			flagKey, label,
		); err != nil {
			return fmt.Errorf("insert label: %w", err)
		}
	}
	return nil
}

// normalizeLabels deduplicates labels and returns them lexicographically
// ordered so storage matches the published response shape.
func normalizeLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	unique := make([]string, 0, len(labels))
	for _, label := range labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		unique = append(unique, label)
	}
	sort.Strings(unique)
	return unique
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
	s.configMu.Lock()
	defer s.configMu.Unlock()

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
	s.configMu.Lock()
	defer s.configMu.Unlock()

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

// PutConfigConditionalInput carries one compare-and-swap configuration
// append: the write only happens when the current effective version matches.
type PutConfigConditionalInput struct {
	FlagKey     string
	Environment string
	// ExpectedVersion is the caller's view of the current effective
	// configuration version; nil means the caller expects no effective
	// configuration (no records yet, or a tombstone on top).
	ExpectedVersion *string
	Enabled         bool
	Percentage      int
	StartsAt        *int64
	EndsAt          *int64
}

// PutConfigConditional appends a new immutable configuration version only
// when ExpectedVersion matches the current effective version at execution
// time. The check and the append run inside the serialized configuration
// write path, so concurrent submissions with the same expected version let
// at most one succeed; the rest fail with ErrVersionConflict and append
// nothing.
func (s *Store) PutConfigConditional(input PutConfigConditionalInput) (*ConfigRecord, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	current, err := latestRecordTx(tx, input.FlagKey, input.Environment, s.Now())
	if err != nil {
		return nil, err
	}
	var currentVersion *string
	if current != nil && !current.Tombstone {
		version := current.Version
		currentVersion = &version
	}
	if !sameOptionalVersion(currentVersion, input.ExpectedVersion) {
		return nil, ErrVersionConflict
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

// sameOptionalVersion compares two optional version identifiers; a nil side
// means "no effective configuration".
func sameOptionalVersion(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
