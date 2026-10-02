package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFlagDefinitionRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	created, err := st.CreateFlag("checkout", "Payments flow", []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.CreatedAt != created.UpdatedAt {
		t.Errorf("created_at=%d updated_at=%d, want equal", created.CreatedAt, created.UpdatedAt)
	}

	got, err := st.GetFlag("checkout")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "Payments flow" || len(got.Labels) != 2 || got.Labels[0] != "alpha" {
		t.Errorf("got %+v, want stored definition", got)
	}

	if _, err := st.GetFlag("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing flag err = %v, want ErrNotFound", err)
	}

	legacy, err := st.CreateFlag("legacy", "", nil)
	if err != nil {
		t.Fatalf("create legacy: %v", err)
	}
	if legacy.Description != "" || len(legacy.Labels) != 0 {
		t.Errorf("legacy flag = %+v, want empty definition", legacy)
	}

	flags, err := st.ListFlags()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(flags) != 2 || flags[0].Key != "checkout" || flags[1].Key != "legacy" {
		t.Errorf("list = %+v, want key-ordered pair", flags)
	}
}

func TestReplaceFlagDefinition(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, err := OpenWithClock(filepath.Join(t.TempDir(), "store.db"), func() time.Time { return now })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	created, err := st.CreateFlag("checkout", "old", []string{"old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	now = now.Add(24 * time.Hour)
	updated, err := st.ReplaceFlagDefinition("checkout", "new", []string{"beta", "alpha"})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if updated.Description != "new" || len(updated.Labels) != 2 {
		t.Errorf("updated = %+v, want replaced definition", updated)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Errorf("created_at changed: %d -> %d", created.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt <= created.UpdatedAt {
		t.Errorf("updated_at not bumped: %d -> %d", created.UpdatedAt, updated.UpdatedAt)
	}

	if _, err := st.ReplaceFlagDefinition("missing", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("replace missing err = %v, want ErrNotFound", err)
	}
}

func TestOpenMigratesLegacyFlagsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE flags (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL)"); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := raw.Exec("INSERT INTO flags(key, created_at) VALUES('checkout', 12345)"); err != nil {
		t.Fatalf("insert legacy flag: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer st.Close()

	flag, err := st.GetFlag("checkout")
	if err != nil {
		t.Fatalf("get migrated flag: %v", err)
	}
	if flag.Description != "" || len(flag.Labels) != 0 {
		t.Errorf("migrated flag = %+v, want empty definition", flag)
	}
	if flag.CreatedAt != 12345 || flag.UpdatedAt != 12345 {
		t.Errorf("migrated timestamps = %d/%d, want 12345/12345", flag.CreatedAt, flag.UpdatedAt)
	}

	if _, err := st.CreateFlag("coupon", "Discounts", []string{"core"}); err != nil {
		t.Fatalf("create after migration: %v", err)
	}
}
