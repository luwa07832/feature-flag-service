package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Definition history actions.
const (
	DefinitionActionCreated = "created"
	DefinitionActionUpdated = "updated"
)

// encodeLabels serialises a labels snapshot as a JSON array. Labels are
// always stored (and rendered) as an array, never NULL.
func encodeLabels(labels []string) (string, error) {
	if labels == nil {
		labels = []string{}
	}
	raw, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("encode labels: %w", err)
	}
	return string(raw), nil
}

func encodeFields(fields []string) (string, error) {
	if fields == nil {
		fields = []string{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encode changed fields: %w", err)
	}
	return string(raw), nil
}

func decodeLabels(raw string) ([]string, error) {
	var labels []string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	if labels == nil {
		labels = []string{}
	}
	return labels, nil
}

func decodeFields(raw string) ([]string, error) {
	var fields []string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("decode changed fields: %w", err)
	}
	if fields == nil {
		fields = []string{}
	}
	return fields, nil
}

// flagLabelsTx returns one flag's labels in lexicographic order inside a tx.
func flagLabelsTx(tx *sql.Tx, flagKey string) ([]string, error) {
	rows, err := tx.Query(
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

// insertDefinitionRecordTx appends one immutable definition change inside
// the caller's transaction so a failed request never leaves partial state.
func insertDefinitionRecordTx(
	tx *sql.Tx,
	flagKey, action string,
	changedFields []string,
	before, after *FlagDefinitionSnapshot,
	changedAt int64,
) error {
	fieldsJSON, err := encodeFields(changedFields)
	if err != nil {
		return err
	}
	var beforeDesc, beforeLabels any
	if before != nil {
		labels, err := encodeLabels(before.Labels)
		if err != nil {
			return err
		}
		beforeDesc = before.Description
		beforeLabels = labels
	}
	var afterDesc, afterLabels any
	if after != nil {
		labels, err := encodeLabels(after.Labels)
		if err != nil {
			return err
		}
		afterDesc = after.Description
		afterLabels = labels
	}
	if _, err := tx.Exec(
		`INSERT INTO flag_definition_history
			(flag_key, action, changed_fields, before_desc, before_labels, after_desc, after_labels, changed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		flagKey, action, fieldsJSON, beforeDesc, beforeLabels, afterDesc, afterLabels, changedAt,
	); err != nil {
		return fmt.Errorf("insert definition history: %w", err)
	}
	return nil
}

// DefinitionHistory returns one flag's definition changes in the
// half-open window [from, to), ordered by (changed_at, event_id). A nil bound
// means unbounded on that side; callers are responsible for validating the
// flag key and existence.
func (s *Store) DefinitionHistory(flagKey string, from, to *int64) ([]FlagDefinitionRecord, error) {
	query := `SELECT event_id, flag_key, action, changed_fields,
			before_desc, before_labels, after_desc, after_labels, changed_at
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
	records := make([]FlagDefinitionRecord, 0)
	for rows.Next() {
		record, err := scanDefinitionRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type definitionRowScanner interface {
	Scan(dest ...any) error
}

func scanDefinitionRecord(row definitionRowScanner) (FlagDefinitionRecord, error) {
	var record FlagDefinitionRecord
	var fieldsJSON string
	var beforeDesc, beforeLabels, afterDesc, afterLabels sql.NullString
	if err := row.Scan(
		&record.EventID, &record.FlagKey, &record.Action, &fieldsJSON,
		&beforeDesc, &beforeLabels, &afterDesc, &afterLabels, &record.ChangedAt,
	); err != nil {
		return FlagDefinitionRecord{}, fmt.Errorf("scan definition history: %w", err)
	}
	fields, err := decodeFields(fieldsJSON)
	if err != nil {
		return FlagDefinitionRecord{}, err
	}
	record.ChangedFields = fields
	if beforeDesc.Valid {
		labels, err := decodeLabels(beforeLabels.String)
		if err != nil {
			return FlagDefinitionRecord{}, err
		}
		record.Before = &FlagDefinitionSnapshot{Description: beforeDesc.String, Labels: labels}
	}
	if afterDesc.Valid {
		labels, err := decodeLabels(afterLabels.String)
		if err != nil {
			return FlagDefinitionRecord{}, err
		}
		record.After = &FlagDefinitionSnapshot{Description: afterDesc.String, Labels: labels}
	}
	return record, nil
}
