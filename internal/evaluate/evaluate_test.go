package evaluate

import (
	"testing"
	"time"
)

func TestMarkerValid(t *testing.T) {
	cases := map[string]bool{
		"user-42": true,
		"a_b.c-X": true,
		"":        false,
		"a b":     false,
		"用户":      false,
		"a/b":     false,
		"abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ01": true,
	}
	for marker, want := range cases {
		if got := MarkerValid(marker); got != want {
			t.Errorf("MarkerValid(%q) = %v, want %v", marker, got, want)
		}
	}
}

func TestBucketIsStableAndBoundByRollout(t *testing.T) {
	first := Bucket("prod", "checkout", "user-1")
	second := Bucket("prod", "checkout", "user-1")
	if first != second {
		t.Fatalf("bucket changed between calls: %d vs %d", first, second)
	}
	if first >= 10000 {
		t.Fatalf("bucket out of basis-point range: %d", first)
	}
}

func TestResolveStates(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-time.Hour)
	windowEnd := now.Add(time.Hour)

	full := func(rollout int, marker string) Config {
		return Config{FlagID: "f", Enabled: true, RolloutPct: rollout,
			HasWindow: true, WindowStart: windowStart, WindowEnd: windowEnd}
	}

	t.Run("unconfigured", func(t *testing.T) {
		if _, got := Resolve("prod", nil, now, "user-1"); got != StatusUnconfigured {
			t.Fatalf("result = %q", got)
		}
	})

	t.Run("unevaluated without marker", func(t *testing.T) {
		cfg := full(100, "")
		if _, got := Resolve("prod", &cfg, now, ""); got != StatusUnevaluated {
			t.Fatalf("result = %q", got)
		}
	})

	t.Run("full rollout is on", func(t *testing.T) {
		cfg := full(100, "user-1")
		if enabled, got := Resolve("prod", &cfg, now, "user-1"); !enabled || got != StatusOn {
			t.Fatalf("enabled=%v result=%q", enabled, got)
		}
	})

	t.Run("zero rollout is off", func(t *testing.T) {
		cfg := full(0, "user-1")
		if enabled, got := Resolve("prod", &cfg, now, "user-1"); enabled || got != StatusOff {
			t.Fatalf("enabled=%v result=%q", enabled, got)
		}
	})

	t.Run("disabled flag is off even at full rollout", func(t *testing.T) {
		cfg := full(100, "user-1")
		cfg.Enabled = false
		if enabled, got := Resolve("prod", &cfg, now, "user-1"); enabled || got != StatusOff {
			t.Fatalf("enabled=%v result=%q", enabled, got)
		}
	})

	t.Run("outside window is off", func(t *testing.T) {
		cfg := full(100, "user-1")
		before := windowStart.Add(-time.Minute)
		if enabled, got := Resolve("prod", &cfg, before, "user-1"); enabled || got != StatusOff {
			t.Fatalf("before window: enabled=%v result=%q", enabled, got)
		}
		atEnd := windowEnd
		if enabled, got := Resolve("prod", &cfg, atEnd, "user-1"); enabled || got != StatusOff {
			t.Fatalf("at window end: enabled=%v result=%q", enabled, got)
		}
		atStart := windowStart
		if enabled, got := Resolve("prod", &cfg, atStart, "user-1"); !enabled || got != StatusOn {
			t.Fatalf("at window start: enabled=%v result=%q", enabled, got)
		}
	})

	t.Run("no window always in scope", func(t *testing.T) {
		cfg := Config{FlagID: "f", Enabled: true, RolloutPct: 100}
		if enabled, got := Resolve("prod", &cfg, now.AddDate(5, 0, 0), "user-1"); !enabled || got != StatusOn {
			t.Fatalf("enabled=%v result=%q", enabled, got)
		}
	})
}
