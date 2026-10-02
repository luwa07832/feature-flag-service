package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Definition change actions recorded by the append-only definition history.
const (
	DefinitionActionCreated = "created"
	DefinitionActionUpdated = "updated"
)

// Definition fields the history can report as changed, in the published
// ordering.
var (
	createdFields = []string{"description", "labels"}
)

// marshalSnapshot serialises one definition snapshot. Labels always encode
// as an array: the store only ever holds normalised (deduplicated, sorted)
// labels, but nil is replaced defensively.
func marshalSnapshot(snapshot DefinitionSnapshot) ([]byte, error) {
	labels := snapshot.Labels
	if labels == nil {
		labels = []string{}
	}
	encoded, err := json.Marshal(struct {
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}{Description: snapshot.Description, Labels: labels})
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}
	return encoded, nil
}

func marshalStringList(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal fields: %w", err)
	}
	return string(encoded), nil
}

func unmarshalStringList(encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("unmarshal fields: %w", err)
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func unmarshalSnapshot(encoded string) (*DefinitionSnapshot, error) {
	var snapshot struct {
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	if snapshot.Labels == nil {
		snapshot.Labels = []string{}
	}
	return &DefinitionSnapshot{
		Description: snapshot.Description,
		Labels:      snapshot.Labels,
	}, nil
}

// insertDefinitionEventTx appends one definition change inside the caller's
// transaction, so a definition write and its history row commit or roll back
// together. beforeJSON is empty for a created event.
func insertDefinitionEventTx(
	tx *sql.Tx,
	flagKey string,
	changedAt int64,
	action string,
	changedFields []string,
	before *DefinitionSnapshot,
	after *DefinitionSnapshot,
) error {
	afterJSON, err := marshalSnapshot(*after)
	if err != nil {
		return err
	}
	fieldsJSON, err := marshalStringList(changedFields)
	if err != nil {
		return err
	}
	var beforeJSON any
	if before != nil {
		encoded, err := marshalSnapshot(*before)
		if err != nil {
			return err
		}
		beforeJSON = string(encoded)
	}
	if _, err := tx.Exec(
		`INSERT INTO flag_definition_history
			(flag_key, changed_at, action, changed_fields, before, after)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		flagKey, changedAt, action, fieldsJSON, beforeJSON, string(afterJSON),
	); err != nil {
		return fmt.Errorf("insert definition event: %w", err)
	}
	return nil
}

// definitionChangedFields reports the fields that differ between two
// snapshots, in the fixed description/labels ordering.
func definitionChangedFields(before, after *DefinitionSnapshot) []string {
	fields := make([]string, 0, 2)
	if before.Description != after.Description {
		fields = append(fields, "description")
	}
	if !equalStringSlices(before.Labels, after.Labels) {
		fields = append(fields, "labels")
	}
	return fields
}

func equalStringSlices(a, b []string) bool {
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

// DefinitionHistory returns the append-only definition change chain for one
// flag, oldest first by (changed_at, event_id). The window is half open:
// from is inclusive and to is exclusive; either bound may be nil.
func (s *Store) DefinitionHistory(flagKey string, from, to *int64) ([]DefinitionEvent, error) {
	query := `SELECT event_id, flag_key, changed_at, action, changed_fields, before, after
		FROM flag_definition_history
		WHERE flag_key = ?`
	args := []any{flagKey}
	if from != nil {
		query += " AND changed_at >= ?"
		args = append(args, *from)
	}
	if to != nil {
		query += " AND changed_at < ?"
		args = append(args, *to)
	}
	query += " ORDER BY changed_at ASC, event_id ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query definition history: %w", err)
	}
	defer rows.Close()
	events := make([]DefinitionEvent, 0)
	for rows.Next() {
		event, err := scanDefinitionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query definition history: %w", err)
	}
	return events, nil
}

// definitionEventScanner covers both multi-row and single-row scans.
type definitionEventScanner interface {
	Scan(dest ...any) error
}

func scanDefinitionEvent(row definitionEventScanner) (DefinitionEvent, error) {
	var event DefinitionEvent
	var changedFieldsJSON, afterJSON string
	var beforeJSON sql.NullString
	if err := row.Scan(
		&event.EventID, &event.FlagKey, &event.ChangedAt, &event.Action,
		&changedFieldsJSON, &beforeJSON, &afterJSON,
	); err != nil {
		return DefinitionEvent{}, fmt.Errorf("scan definition event: %w", err)
	}
	fields, err := unmarshalStringList(changedFieldsJSON)
	if err != nil {
		return DefinitionEvent{}, err
	}
	event.ChangedFields = fields
	if beforeJSON.Valid {
		before, err := unmarshalSnapshot(beforeJSON.String)
		if err != nil {
			return DefinitionEvent{}, err
		}
		event.Before = before
	}
	after, err := unmarshalSnapshot(afterJSON)
	if err != nil {
		return DefinitionEvent{}, err
	}
	event.After = after
	return event, nil
}
