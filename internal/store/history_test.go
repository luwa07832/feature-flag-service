package store

import (
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func openAt(t *testing.T, now time.Time) (*Store, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: now}
	st, err := OpenWithClock(filepath.Join(t.TempDir(), "store.db"), func() time.Time {
		return clock.t
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, clock
}

func (c *fakeClock) advance(to time.Time) { c.t = to }

func TestEffectiveConfigsAtSelectsLastRecordNotLaterThan(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")
	mustFlag(t, st, "search")

	clock.advance(base.Add(1 * time.Hour))
	r1 := mustPut(t, st, "checkout", "prod", true, 10)

	clock.advance(base.Add(2 * time.Hour))
	r2 := mustPut(t, st, "checkout", "prod", true, 50)

	clock.advance(base.Add(3 * time.Hour))
	mustPut(t, st, "search", "prod", true, 100)

	at := base.Add(90 * time.Minute).UnixNano()
	got, err := st.EffectiveConfigsAt("prod", at)
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d flags, want 1 (search added later must not appear)", len(got))
	}
	checkout, ok := got["checkout"]
	if !ok {
		t.Fatalf("checkout missing: %v", got)
	}
	if checkout.Version != r1.Version {
		t.Errorf("version = %s, want %s (r2 happened later)", checkout.Version, r1.Version)
	}
	if checkout.Percentage != 10 {
		t.Errorf("percentage = %d, want 10", checkout.Percentage)
	}

	at2 := base.Add(2 * time.Hour).UnixNano()
	got2, err := st.EffectiveConfigsAt("prod", at2)
	if err != nil {
		t.Fatalf("effective at exact change time: %v", err)
	}
	if got2["checkout"].Version != r2.Version {
		t.Errorf("at changed_at version = %s, want %s (inclusive bound)", got2["checkout"].Version, r2.Version)
	}
}

func TestTombstoneRestoresUnconfiguredState(t *testing.T) {
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")

	clock.advance(base.Add(1 * time.Hour))
	mustPut(t, st, "checkout", "prod", true, 100)
	clock.advance(base.Add(2 * time.Hour))
	tomb, err := st.DeleteConfig("checkout", "prod")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	clock.advance(base.Add(3 * time.Hour))
	mustPut(t, st, "checkout", "prod", true, 25)

	beforeDelete := base.Add(90 * time.Minute).UnixNano()
	got, err := st.EffectiveConfigsAt("prod", beforeDelete)
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if r := got["checkout"]; r.Tombstone || r.Percentage != 100 {
		t.Errorf("before delete got tombstone=%v pct=%d, want live 100", r.Tombstone, r.Percentage)
	}

	afterDelete := base.Add(150 * time.Minute).UnixNano()
	got2, err := st.EffectiveConfigsAt("prod", afterDelete)
	if err != nil {
		t.Fatalf("effective after delete: %v", err)
	}
	r := got2["checkout"]
	if !r.Tombstone {
		t.Errorf("after delete tombstone=false, want true; version=%s", r.Version)
	}
	if r.Version != tomb.Version {
		t.Errorf("after delete version=%s, want %s", r.Version, tomb.Version)
	}
}

func TestPutConfigRejectsUnknownEnvAndFlag(t *testing.T) {
	st, _ := openAt(t, time.Now())
	if _, err := st.PutConfig(PutConfigInput{FlagKey: "f", Environment: "prod", Percentage: 1}); err != ErrNotFound {
		t.Fatalf("unknown env err = %v, want ErrNotFound", err)
	}
	mustEnv(t, st, "prod")
	if _, err := st.PutConfig(PutConfigInput{FlagKey: "f", Environment: "prod", Percentage: 1}); err != ErrNotFound {
		t.Fatalf("unknown flag err = %v, want ErrNotFound", err)
	}
}

func TestHistoryIsAppendOnly(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")

	clock.advance(base.Add(1 * time.Hour))
	v1 := mustPut(t, st, "checkout", "prod", true, 10)
	clock.advance(base.Add(2 * time.Hour))
	v2 := mustPut(t, st, "checkout", "prod", false, 20)
	clock.advance(base.Add(3 * time.Hour))
	v3, err := st.DeleteConfig("checkout", "prod")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	records, err := st.ConfigHistory("checkout", "prod")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	want := []string{v1.Version, v2.Version, v3.Version}
	if len(records) != len(want) {
		t.Fatalf("history len = %d, want %d", len(records), len(want))
	}
	for i, record := range records {
		if record.Version != want[i] {
			t.Errorf("history[%d] = %s, want %s", i, record.Version, want[i])
		}
	}
	if records[2].Tombstone != true {
		t.Errorf("last record tombstone=false")
	}
}

func mustEnv(t *testing.T, st *Store, key string) {
	t.Helper()
	if _, err := st.CreateEnvironment(key); err != nil {
		t.Fatalf("create env: %v", err)
	}
}

func mustFlag(t *testing.T, st *Store, key string) {
	t.Helper()
	if _, err := st.CreateFlag(key, "", nil); err != nil {
		t.Fatalf("create flag: %v", err)
	}
}

func mustPut(t *testing.T, st *Store, flagKey, env string, enabled bool, pct int) *ConfigRecord {
	t.Helper()
	record, err := st.PutConfig(PutConfigInput{FlagKey: flagKey, Environment: env, Enabled: enabled, Percentage: pct})
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	return record
}
