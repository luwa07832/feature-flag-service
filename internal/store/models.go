package store

// Environment is a registered deployment target.
type Environment struct {
	Key       string
	CreatedAt int64
}

// Flag is a feature flag definition. Description and labels are the editable
// definition surface; they never append configuration history.
type Flag struct {
	Key         string
	Description string
	Labels      []string
	CreatedAt   int64
	UpdatedAt   int64
}

// ConfigRecord is one immutable configuration version. The history is
// append-only: a deletion appends a record with Tombstone=true rather than
// removing prior rows.
type ConfigRecord struct {
	FlagKey     string
	Environment string
	Version     string
	Enabled     bool
	Percentage  int
	StartsAt    *int64
	EndsAt      *int64
	ChangedAt   int64
	Tombstone   bool
}

// DefinitionSnapshot is the editable definition surface captured at one
// instant. Labels are stored in the history exactly as presented in
// definition responses: deduplicated and lexicographically ordered.
type DefinitionSnapshot struct {
	Description string
	Labels      []string
}

// DefinitionEvent is one append-only definition change. Before is nil for
// the created action and populated for updated; after is always set. Events
// are ordered by (ChangedAt, EventID), so EventID disambiguates events
// produced within the same nanosecond.
type DefinitionEvent struct {
	EventID       int64
	FlagKey       string
	ChangedAt     int64
	Action        string
	ChangedFields []string
	Before        *DefinitionSnapshot
	After         *DefinitionSnapshot
}
