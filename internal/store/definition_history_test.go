package store

import (
	"testing"
	"time"
)

func TestCreateFlagAppendsCreatedDefinitionRecord(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)

	clock.advance(base.Add(1 * time.Hour))
	created, err := st.CreateFlag(CreateFlagInput{
		Key:         "checkout",
		Description: "Payments",
		Labels:      []string{"zeta", "alpha", "alpha"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	records, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	if record.Action != DefinitionActionCreated {
		t.Fatalf("action = %q, want created", record.Action)
	}
	if record.ChangedAt != created.CreatedAt {
		t.Fatalf("changed_at = %d, want created_at %d", record.ChangedAt, created.CreatedAt)
	}
	if record.Before != nil {
		t.Fatalf("before = %#v, want nil", record.Before)
	}
	if record.After == nil {
		t.Fatal("after is nil")
	}
	if record.After.Description != "Payments" {
		t.Fatalf("after description = %q", record.After.Description)
	}
	if !equalStrings(record.After.Labels, []string{"alpha", "zeta"}) {
		t.Fatalf("after labels = %v, want deduped+sorted", record.After.Labels)
	}
	if !equalStrings(record.ChangedFields, []string{"description", "labels"}) {
		t.Fatalf("changed_fields = %v, want both", record.ChangedFields)
	}
	if record.EventID <= 0 {
		t.Fatalf("event_id = %d, want positive", record.EventID)
	}
}

func TestUpdateFlagDefinitionAppendsUpdatedRecord(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	clock.advance(base)
	created, err := st.CreateFlag(CreateFlagInput{
		Key: "checkout", Description: "First", Labels: []string{"a"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.advance(base.Add(1 * time.Hour))
	updated, err := st.UpdateFlagDefinition("checkout", "Second", []string{"b", "a"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	records, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	entry := records[1]
	if entry.Action != DefinitionActionUpdated {
		t.Fatalf("action = %q, want updated", entry.Action)
	}
	if entry.ChangedAt != updated.UpdatedAt {
		t.Fatalf("changed_at = %d, want updated_at %d", entry.ChangedAt, updated.UpdatedAt)
	}
	if entry.ChangedAt == created.CreatedAt {
		t.Fatal("changed_at did not advance")
	}
	if entry.Before == nil || entry.After == nil {
		t.Fatalf("before/after = %#v/%#v", entry.Before, entry.After)
	}
	if entry.Before.Description != "First" || !equalStrings(entry.Before.Labels, []string{"a"}) {
		t.Fatalf("before = %#v", entry.Before)
	}
	if entry.After.Description != "Second" || !equalStrings(entry.After.Labels, []string{"a", "b"}) {
		t.Fatalf("after = %#v", entry.After)
	}
	if !equalStrings(entry.ChangedFields, []string{"description", "labels"}) {
		t.Fatalf("changed_fields = %v, want both", entry.ChangedFields)
	}
	if entry.EventID != records[0].EventID+1 {
		t.Fatalf("event_id not strictly increasing: %d after %d", entry.EventID, records[0].EventID)
	}
}

func TestDefinitionChangedFieldsReflectsActualDiffs(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	if _, err := st.CreateFlag(CreateFlagInput{
		Key: "checkout", Description: "Same", Labels: []string{"a", "b"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.advance(base.Add(1 * time.Hour))
	if _, err := st.UpdateFlagDefinition("checkout", "Same", []string{"b", "a", "b"}); err != nil {
		t.Fatalf("identical replace: %v", err)
	}
	clock.advance(base.Add(2 * time.Hour))
	if _, err := st.UpdateFlagDefinition("checkout", "Changed", []string{"b", "a"}); err != nil {
		t.Fatalf("description-only update: %v", err)
	}
	clock.advance(base.Add(3 * time.Hour))
	if _, err := st.UpdateFlagDefinition("checkout", "Changed", []string{"a", "c"}); err != nil {
		t.Fatalf("labels-only update: %v", err)
	}

	records, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("got %d records, want 4 (identical replace still appended)", len(records))
	}
	if len(records[1].ChangedFields) != 0 {
		t.Fatalf("identical replace fields = %v, want empty", records[1].ChangedFields)
	}
	if records[1].Action != DefinitionActionUpdated {
		t.Fatalf("identical replace action = %q, want updated", records[1].Action)
	}
	if !equalStrings(records[2].ChangedFields, []string{"description"}) {
		t.Fatalf("description-only fields = %v", records[2].ChangedFields)
	}
	if !equalStrings(records[3].ChangedFields, []string{"labels"}) {
		t.Fatalf("labels-only fields = %v", records[3].ChangedFields)
	}
}

func TestFailedDefinitionUpdateAppendsNothing(t *testing.T) {
	st := openStore(t)
	if _, err := st.CreateFlag(CreateFlagInput{Key: "checkout"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if _, err := st.UpdateFlagDefinition("missing", "d", nil); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	after, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed update appended history: %d -> %d", len(before), len(after))
	}
}

func TestDefinitionHistoryWindowAndOrdering(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)

	t0 := base
	clock.advance(t0)
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "d0"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	t1 := base.Add(1 * time.Hour)
	clock.advance(t1)
	if _, err := st.UpdateFlagDefinition("f", "d1", nil); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	t2 := base.Add(2 * time.Hour)
	clock.advance(t2)
	if _, err := st.UpdateFlagDefinition("f", "d2", nil); err != nil {
		t.Fatalf("update 2: %v", err)
	}

	from := t1.UnixNano()
	to := t2.UnixNano()
	records, err := st.DefinitionHistory("f", &from, &to)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records in [t1, t2), want 1", len(records))
	}
	if records[0].ChangedAt != t1.UnixNano() {
		t.Fatalf("windowed changed_at = %d, want %d (from inclusive, to exclusive)", records[0].ChangedAt, t1.UnixNano())
	}

	all, err := st.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].EventID <= all[i-1].EventID {
			t.Fatalf("event_id not increasing at %d", i)
		}
		if all[i].ChangedAt < all[i-1].ChangedAt {
			t.Fatalf("changed_at not ascending at %d", i)
		}
	}
}

func TestDefinitionHistoryPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/store.db"
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return base }

	st, err := OpenWithClock(path, clock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "d", Labels: []string{"x"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenWithClock(path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	records, err := reopened.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records after reopen, want 1", len(records))
	}
}

func TestDefinitionHistoryIsIndependentFromConfigHistory(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")

	clock.advance(base.Add(1 * time.Hour))
	mustPut(t, st, "checkout", "prod", true, 10)

	definitionRecords, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("definition history: %v", err)
	}
	if len(definitionRecords) != 1 {
		t.Fatalf("definition records = %d, want only created (config versions are not edits)", len(definitionRecords))
	}
}
