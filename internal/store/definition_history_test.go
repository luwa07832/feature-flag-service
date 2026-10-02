package store

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCreateFlagAppendsCreatedEvent(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)

	clock.advance(base.Add(1 * time.Hour))
	flag, err := st.CreateFlag(CreateFlagInput{
		Key:         "checkout",
		Description: "Payments",
		Labels:      []string{"b", "a", "a"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Action != DefinitionActionCreated {
		t.Fatalf("action = %q", event.Action)
	}
	if event.ChangedAt != flag.CreatedAt {
		t.Fatalf("changed_at = %d, want created_at %d", event.ChangedAt, flag.CreatedAt)
	}
	if !equalStrings(event.ChangedFields, []string{"description", "labels"}) {
		t.Fatalf("changed_fields = %v", event.ChangedFields)
	}
	if event.Before != nil {
		t.Fatalf("before = %#v, want nil", event.Before)
	}
	if event.After == nil {
		t.Fatal("after is nil")
	}
	if event.After.Description != "Payments" {
		t.Fatalf("after description = %q", event.After.Description)
	}
	if !equalStrings(event.After.Labels, []string{"a", "b"}) {
		t.Fatalf("after labels = %v", event.After.Labels)
	}
	if event.EventID <= 0 {
		t.Fatalf("event_id = %d, want positive", event.EventID)
	}
}

func TestUpdateAppendsUpdatedEventWithBeforeAndAfter(t *testing.T) {
	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	createdAt := base.Add(1 * time.Hour).UnixNano()
	clock.advance(base.Add(1 * time.Hour))
	if _, err := st.CreateFlag(CreateFlagInput{
		Key: "checkout", Description: "Old", Labels: []string{"a"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	updatedAt := base.Add(2 * time.Hour).UnixNano()
	clock.advance(base.Add(2 * time.Hour))
	flag, err := st.UpdateFlagDefinition("checkout", "New", []string{"b", "b", "a"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if flag.UpdatedAt != updatedAt || flag.CreatedAt != createdAt {
		t.Fatalf("timestamps created=%d updated=%d", flag.CreatedAt, flag.UpdatedAt)
	}

	events, err := st.DefinitionHistory("checkout", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	updated := events[1]
	if updated.Action != DefinitionActionUpdated {
		t.Fatalf("action = %q", updated.Action)
	}
	if updated.ChangedAt != updatedAt {
		t.Fatalf("changed_at = %d, want %d", updated.ChangedAt, updatedAt)
	}
	if !equalStrings(updated.ChangedFields, []string{"description", "labels"}) {
		t.Fatalf("changed_fields = %v", updated.ChangedFields)
	}
	if updated.Before == nil || updated.Before.Description != "Old" ||
		!equalStrings(updated.Before.Labels, []string{"a"}) {
		t.Fatalf("before = %#v", updated.Before)
	}
	if updated.After == nil || updated.After.Description != "New" ||
		!equalStrings(updated.After.Labels, []string{"a", "b"}) {
		t.Fatalf("after = %#v", updated.After)
	}
}

func TestUpdateChangedFieldsOnlyActualDeltas(t *testing.T) {
	st, clock := openAt(t, time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC))
	if _, err := st.CreateFlag(CreateFlagInput{
		Key: "f", Description: "Same", Labels: []string{"a", "b"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.advance(clock.t.Add(time.Minute))
	if _, err := st.UpdateFlagDefinition("f", "Changed", []string{"b", "a"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	events, err := st.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if got := events[1].ChangedFields; !equalStrings(got, []string{"description"}) {
		t.Fatalf("changed_fields = %v, want [description]", got)
	}
}

func TestIdenticalReplaceAppendsEmptyFieldsAndNewTimestamp(t *testing.T) {
	st, clock := openAt(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC))
	created, err := st.CreateFlag(CreateFlagInput{
		Key: "f", Description: "Same", Labels: []string{"a"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.advance(clock.t.Add(time.Minute))
	updated, err := st.UpdateFlagDefinition("f", "Same", []string{"a"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("updated_at did not advance: %d", updated.UpdatedAt)
	}
	events, err := st.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[1].Action != DefinitionActionUpdated {
		t.Fatalf("action = %q", events[1].Action)
	}
	if len(events[1].ChangedFields) != 0 {
		t.Fatalf("changed_fields = %v, want empty", events[1].ChangedFields)
	}
}

func TestDefinitionHistoryOrdersByTimeThenEventID(t *testing.T) {
	base := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	st, _ := openAt(t, base)
	if _, err := st.CreateFlag(CreateFlagInput{Key: "zeta"}); err != nil {
		t.Fatalf("create zeta: %v", err)
	}
	if _, err := st.CreateFlag(CreateFlagInput{Key: "alpha"}); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	events, err := st.DefinitionHistory("zeta", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 1 || events[0].FlagKey != "zeta" {
		t.Fatalf("events = %#v", events)
	}

	zetaEvents, err := st.DefinitionHistory("zeta", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	alphaEvents, err := st.DefinitionHistory("alpha", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if alphaEvents[0].EventID != zetaEvents[0].EventID+1 {
		t.Fatalf("event ids not strictly increasing: zeta=%d alpha=%d",
			zetaEvents[0].EventID, alphaEvents[0].EventID)
	}
}

func TestSameInstantEventsOrderedByEventID(t *testing.T) {
	at := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: at}
	st, err := OpenWithClock(t.TempDir()+"/same.db", func() time.Time { return clock.t })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.UpdateFlagDefinition("f", "v2", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	events, err := st.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].ChangedAt != events[1].ChangedAt {
		t.Fatalf("times differ: %d vs %d", events[0].ChangedAt, events[1].ChangedAt)
	}
	if events[0].EventID >= events[1].EventID {
		t.Fatalf("event ids out of order: %d then %d", events[0].EventID, events[1].EventID)
	}
	if events[0].Action != DefinitionActionCreated || events[1].Action != DefinitionActionUpdated {
		t.Fatalf("actions = %q, %q", events[0].Action, events[1].Action)
	}
}

func TestDefinitionHistoryWindowIsHalfOpen(t *testing.T) {
	base := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	clock.advance(base.Add(1 * time.Hour))
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.advance(base.Add(2 * time.Hour))
	if _, err := st.UpdateFlagDefinition("f", "v2", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	clock.advance(base.Add(3 * time.Hour))
	if _, err := st.UpdateFlagDefinition("f", "v3", nil); err != nil {
		t.Fatalf("update: %v", err)
	}

	from := base.Add(2 * time.Hour).UnixNano()
	to := base.Add(3 * time.Hour).UnixNano()
	events, err := st.DefinitionHistory("f", &from, &to)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 in [2h, 3h)", len(events))
	}
	if events[0].After.Description != "v2" {
		t.Fatalf("event = %#v", events[0])
	}
}

func TestDefinitionHistoryPersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/restart.db"
	clock := &fakeClock{t: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)}
	open := func() *Store {
		st, err := OpenWithClock(path, func() time.Time { return clock.t })
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return st
	}
	st := open()
	createdAt := clock.t.UnixNano()
	if _, err := st.CreateFlag(CreateFlagInput{
		Key: "f", Description: "v1", Labels: []string{"a"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	clock.advance(clock.t.Add(time.Hour))
	updatedAt := clock.t.UnixNano()
	if _, err := st.UpdateFlagDefinition("f", "v2", []string{"b"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := open()
	defer reopened.Close()
	events, err := reopened.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 after restart", len(events))
	}
	if events[0].ChangedAt != createdAt || events[1].ChangedAt != updatedAt {
		t.Fatalf("times = %d, %d want %d, %d",
			events[0].ChangedAt, events[1].ChangedAt, createdAt, updatedAt)
	}
}

func TestBackfillCreatesOneCreatedEventPerExistingFlag(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	clock := func() time.Time { return time.Unix(0, 77_000_000_000) }

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE service_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE environments (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE flags (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE flag_labels (
	flag_key TEXT NOT NULL, label TEXT NOT NULL,
	PRIMARY KEY(flag_key, label)
);
CREATE TABLE config_history (
	id INTEGER PRIMARY KEY AUTOINCREMENT, flag_key TEXT NOT NULL, environment TEXT NOT NULL,
	version TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL, percentage INTEGER NOT NULL,
	starts_at INTEGER, ends_at INTEGER, changed_at INTEGER NOT NULL, tombstone INTEGER NOT NULL DEFAULT 0
);
INSERT INTO flags(key, created_at) VALUES('later', 200), ('earlier', 100);
INSERT INTO flag_labels(flag_key, label) VALUES('later', 'z'), ('later', 'a');
`); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := OpenWithClock(path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer migrated.Close()

	later, err := migrated.DefinitionHistory("later", nil, nil)
	if err != nil {
		t.Fatalf("later history: %v", err)
	}
	earlier, err := migrated.DefinitionHistory("earlier", nil, nil)
	if err != nil {
		t.Fatalf("earlier history: %v", err)
	}
	if len(later) != 1 || len(earlier) != 1 {
		t.Fatalf("backfill counts later=%d earlier=%d, want 1 each", len(later), len(earlier))
	}
	if later[0].Action != DefinitionActionCreated || earlier[0].Action != DefinitionActionCreated {
		t.Fatalf("actions = %q, %q", later[0].Action, earlier[0].Action)
	}
	if later[0].ChangedAt != 200 || earlier[0].ChangedAt != 100 {
		t.Fatalf("changed_at = %d, %d want 200, 100", later[0].ChangedAt, earlier[0].ChangedAt)
	}
	if !equalStrings(later[0].ChangedFields, []string{"description", "labels"}) {
		t.Fatalf("changed_fields = %v", later[0].ChangedFields)
	}
	if later[0].Before != nil {
		t.Fatalf("before = %#v", later[0].Before)
	}
	if later[0].After == nil || !equalStrings(later[0].After.Labels, []string{"a", "z"}) {
		t.Fatalf("after = %#v", later[0].After)
	}
	if earlier[0].EventID > later[0].EventID {
		t.Fatalf("earlier created event must sort first: %d > %d",
			earlier[0].EventID, later[0].EventID)
	}

	// Reopening must not fabricate further backfill events.
	if err := migrated.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := OpenWithClock(path, clock)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer reopened.Close()
	again, err := reopened.DefinitionHistory("later", nil, nil)
	if err != nil {
		t.Fatalf("history after reopen: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("events after reopen = %d, want 1", len(again))
	}
}

func TestFailedWritesDoNotAppendDefinitionHistory(t *testing.T) {
	st, _ := openAt(t, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "dup"}); err != ErrAlreadyExists {
		t.Fatalf("duplicate create err = %v, want ErrAlreadyExists", err)
	}
	if _, err := st.UpdateFlagDefinition("missing", "x", nil); err != ErrNotFound {
		t.Fatalf("unknown update err = %v, want ErrNotFound", err)
	}
	events, err := st.DefinitionHistory("f", nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want only the successful created event", len(events))
	}
}
