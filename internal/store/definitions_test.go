package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCreateFlagWithDefinition(t *testing.T) {
	st := openStore(t)
	flag, err := st.CreateFlag(CreateFlagInput{
		Key:         "checkout",
		Description: "Payments",
		Labels:      []string{"z", "a", "a"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if flag.CreatedAt != flag.UpdatedAt {
		t.Fatalf("created_at %d != updated_at %d", flag.CreatedAt, flag.UpdatedAt)
	}
	if want := []string{"a", "z"}; len(flag.Labels) != len(want) {
		t.Fatalf("labels = %v, want %v", flag.Labels, want)
	} else {
		for i := range want {
			if flag.Labels[i] != want[i] {
				t.Fatalf("labels = %v, want %v", flag.Labels, want)
			}
		}
	}

	got, err := st.GetFlag("checkout")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "Payments" {
		t.Fatalf("description = %q", got.Description)
	}
	if want := []string{"a", "z"}; !equalStrings(got.Labels, want) {
		t.Fatalf("labels = %v, want %v", got.Labels, want)
	}
}

func TestCreateFlagDefaultsEmptyDefinition(t *testing.T) {
	st := openStore(t)
	if _, err := st.CreateFlag(CreateFlagInput{Key: "plain"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetFlag("plain")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "" || len(got.Labels) != 0 {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestUpdateFlagDefinition(t *testing.T) {
	st := openStore(t)
	created, err := st.CreateFlag(CreateFlagInput{Key: "f", Description: "first", Labels: []string{"a"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.UpdateFlagDefinition("missing", "d", nil); err != ErrNotFound {
		t.Fatalf("missing flag err = %v, want ErrNotFound", err)
	}

	updated, err := st.UpdateFlagDefinition("f", "second", []string{"b"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Fatalf("created_at moved: %d -> %d", created.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt < created.UpdatedAt {
		t.Fatalf("updated_at did not advance: %d", updated.UpdatedAt)
	}
	if updated.Description != "second" || !equalStrings(updated.Labels, []string{"b"}) {
		t.Fatalf("updated = %#v", updated)
	}

	got, err := st.GetFlag("f")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "second" || !equalStrings(got.Labels, []string{"b"}) {
		t.Fatalf("persisted = %#v", got)
	}
}

func TestListFlagsIncludesDefinitionOrderedByKey(t *testing.T) {
	st := openStore(t)
	if _, err := st.CreateFlag(CreateFlagInput{Key: "zeta", Labels: []string{"z"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.CreateFlag(CreateFlagInput{Key: "alpha", Description: "d", Labels: []string{"a"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	flags, err := st.ListFlags()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(flags) != 2 || flags[0].Key != "alpha" || flags[1].Key != "zeta" {
		t.Fatalf("order = %#v", flags)
	}
	if !equalStrings(flags[0].Labels, []string{"a"}) {
		t.Fatalf("alpha labels = %v", flags[0].Labels)
	}
}

func TestOpenMigratesLegacyFlagsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	clock := func() time.Time { return time.Unix(0, 77_000_000_000) }

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE service_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE environments (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE flags (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE config_history (
	id INTEGER PRIMARY KEY AUTOINCREMENT, flag_key TEXT NOT NULL, environment TEXT NOT NULL,
	version TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL, percentage INTEGER NOT NULL,
	starts_at INTEGER, ends_at INTEGER, changed_at INTEGER NOT NULL, tombstone INTEGER NOT NULL DEFAULT 0
);
INSERT INTO flags(key, created_at) VALUES('old', 42);
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

	flag, err := migrated.GetFlag("old")
	if err != nil {
		t.Fatalf("get migrated: %v", err)
	}
	if flag.Description != "" {
		t.Fatalf("description = %q, want empty", flag.Description)
	}
	if flag.UpdatedAt != flag.CreatedAt || flag.CreatedAt != 42 {
		t.Fatalf("timestamps = created %d updated %d, want 42/42", flag.CreatedAt, flag.UpdatedAt)
	}
	if len(flag.Labels) != 0 {
		t.Fatalf("labels = %v, want empty", flag.Labels)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func equalStrings(a, b []string) bool {
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
