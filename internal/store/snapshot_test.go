package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func appendAt(t *testing.T, st *Store, env, flag string, enabled bool, rollout int, at time.Time, hasWindow bool) FlagRecord {
	t.Helper()
	in := AppendInput{
		EnvironmentID: env,
		FlagID:        flag,
		Enabled:       enabled,
		RolloutPct:    rollout,
		ChangedAt:     at,
	}
	if hasWindow {
		in.HasWindow = true
		in.WindowStart = at.Add(-time.Hour)
		in.WindowEnd = at.Add(time.Hour)
	}
	rec, err := st.AppendFlagChange(context.Background(), in)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return rec
}

func TestSnapshotAtSelectsLatestVersionNotLaterThanPoint(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	v1 := appendAt(t, st, "prod", "checkout", true, 10, base, false)
	v2 := appendAt(t, st, "prod", "checkout", true, 50, base.Add(2*time.Hour), false)
	v3 := appendAt(t, st, "prod", "checkout", false, 0, base.Add(4*time.Hour), false)

	cases := []struct {
		name    string
		at      time.Time
		want    string
		rollout int
	}{
		{"before first", base.Add(-time.Minute), "", 0},
		{"at first", base, v1.VersionID, 10},
		{"between versions", base.Add(time.Hour), v1.VersionID, 10},
		{"at second", base.Add(2 * time.Hour), v2.VersionID, 50},
		{"at third", base.Add(4 * time.Hour), v3.VersionID, 0},
		{"in the future", base.Add(24 * time.Hour), v3.VersionID, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, order, err := st.SnapshotAt(context.Background(), "prod", tc.at)
			if tc.want == "" {
				if !errors.Is(err, ErrEnvironmentNotFound) {
					t.Fatalf("err = %v, want ErrEnvironmentNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if len(order) != 1 || order[0] != "checkout" {
				t.Fatalf("order = %v", order)
			}
			rec := snapshot["checkout"]
			if rec.VersionID != tc.want || rec.RolloutPct != tc.rollout {
				t.Fatalf("got version=%s rollout=%d, want %s/%d", rec.VersionID, rec.RolloutPct, tc.want, tc.rollout)
			}
		})
	}
}

func TestSnapshotAtMarksLaterAddedFlagsUnconfigured(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	appendAt(t, st, "prod", "first", true, 100, base, false)
	appendAt(t, st, "prod", "second", true, 100, base.Add(2*time.Hour), false)

	snapshot, order, err := st.SnapshotAt(context.Background(), "prod", base.Add(time.Hour))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("order = %v", order)
	}
	if _, ok := snapshot["first"]; !ok {
		t.Fatalf("first flag should be configured")
	}
	if _, ok := snapshot["second"]; ok {
		t.Fatalf("second flag must not inherit a future configuration")
	}
}

func TestSnapshotUnknownEnvironment(t *testing.T) {
	st := openTestStore(t)
	_, _, err := st.SnapshotAt(context.Background(), "missing", time.Now())
	if !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestHistoryIsAppendOnlyNewestFirst(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	v1 := appendAt(t, st, "prod", "flag", true, 10, base, false)
	v2 := appendAt(t, st, "prod", "flag", false, 20, base.Add(time.Hour), true)

	history, err := st.History(context.Background(), "prod", "flag")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 || history[0].VersionID != v2.VersionID || history[1].VersionID != v1.VersionID {
		t.Fatalf("history order/ids = %+v", history)
	}
	if !history[0].HasWindow || history[0].WindowStart().IsZero() {
		t.Fatalf("window not restored: %+v", history[0])
	}
}

func TestAppendRejectsInvalidInput(t *testing.T) {
	st := openTestStore(t)
	now := time.Now()
	bad := []AppendInput{
		{EnvironmentID: "", FlagID: "f", ChangedAt: now},
		{EnvironmentID: "e", FlagID: "f", RolloutPct: 101, ChangedAt: now},
		{EnvironmentID: "e", FlagID: "f", RolloutPct: 50, ChangedAt: now,
			HasWindow: true, WindowStart: now, WindowEnd: now.Add(-time.Hour)},
	}
	for i, in := range bad {
		if _, err := st.AppendFlagChange(context.Background(), in); !errors.Is(err, ErrInvalidChange) {
			t.Fatalf("case %d: err = %v, want ErrInvalidChange", i, err)
		}
	}
}
