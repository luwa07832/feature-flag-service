package eval

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidateMarker(t *testing.T) {
	cases := []struct {
		marker string
		wantOK bool
	}{
		{"", true},
		{"user-42", true},
		{"a_b", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"User", false},
		{"user/1", false},
		{"user 1", false},
		{"中文", false},
	}
	for _, tc := range cases {
		err := ValidateMarker(tc.marker)
		if (err == nil) != tc.wantOK {
			t.Errorf("ValidateMarker(%q) err=%v, wantOK=%v", tc.marker, err, tc.wantOK)
		}
	}
}

func ptr(v int64) *int64 { return &v }

func TestWindowActiveAt(t *testing.T) {
	cases := []struct {
		name   string
		window Window
		at     int64
		want   bool
	}{
		{"unbounded", Window{}, 1_000, true},
		{"before start", Window{StartsAt: ptr(10)}, 9, false},
		{"at start inclusive", Window{StartsAt: ptr(10)}, 10, true},
		{"at end exclusive", Window{EndsAt: ptr(10)}, 10, false},
		{"just before end", Window{EndsAt: ptr(10)}, 9, true},
		{"inside bounded", Window{StartsAt: ptr(0), EndsAt: ptr(10)}, 5, true},
	}
	for _, tc := range cases {
		if got := tc.window.ActiveAt(tc.at); got != tc.want {
			t.Errorf("%s: ActiveAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRolloutBoundaries(t *testing.T) {
	for _, marker := range []string{"u1", "u2"} {
		if (Rollout{FlagKey: "f", Environment: "e", Percentage: 0}.Served(marker)) {
			t.Errorf("0%% served %s", marker)
		}
		if !(Rollout{FlagKey: "f", Environment: "e", Percentage: 100}.Served(marker)) {
			t.Errorf("100%% not served %s", marker)
		}
	}
}

func TestRolloutIsDeterministic(t *testing.T) {
	first := Rollout{FlagKey: "checkout", Environment: "prod", Percentage: 50}.Served("user-7")
	for i := 0; i < 10; i++ {
		got := Rollout{FlagKey: "checkout", Environment: "prod", Percentage: 50}.Served("user-7")
		if got != first {
			t.Fatalf("rollout not deterministic: %v then %v", first, got)
		}
	}
}

func TestRolloutCoversRoughlyHalfAtFiftyPercent(t *testing.T) {
	served := 0
	const n = 1000
	for i := 0; i < n; i++ {
		if (Rollout{FlagKey: "f", Environment: "e", Percentage: 50}.Served("user-" + strconv.Itoa(i))) {
			served++
		}
	}
	if served < 400 || served > 600 {
		t.Fatalf("50%% rollout served %d/%d, expected ~500", served, n)
	}
}

func TestEvaluatedStatuses(t *testing.T) {
	enabled := &Configuration{FlagKey: "f", Enabled: true, Percentage: 100}
	disabled := &Configuration{FlagKey: "f", Enabled: false, Percentage: 100}
	if got := Evaluated("e", nil, "m", 0); got != StatusUnconfigured {
		t.Errorf("nil config = %v, want unconfigured", got)
	}
	if got := Evaluated("e", enabled, "m", 0); got != StatusOn {
		t.Errorf("100%% enabled = %v, want on", got)
	}
	if got := Evaluated("e", disabled, "m", 0); got != StatusOff {
		t.Errorf("disabled = %v, want off", got)
	}
	zero := &Configuration{FlagKey: "f", Enabled: true, Percentage: 0}
	if got := Evaluated("e", zero, "m", 0); got != StatusOff {
		t.Errorf("0%% enabled = %v, want off", got)
	}
	notStarted := &Configuration{FlagKey: "f", Enabled: true, Percentage: 100, Window: Window{StartsAt: ptr(10)}}
	if got := Evaluated("e", notStarted, "m", 5); got != StatusOff {
		t.Errorf("window not started = %v, want off", got)
	}
	ended := &Configuration{FlagKey: "f", Enabled: true, Percentage: 100, Window: Window{EndsAt: ptr(10)}}
	if got := Evaluated("e", ended, "m", 10); got != StatusOff {
		t.Errorf("window ended = %v, want off", got)
	}
}

func TestExplainedReasons(t *testing.T) {
	cases := []struct {
		name   string
		cfg    *Configuration
		at     int64
		status Status
		reason Reason
	}{
		{"unconfigured", nil, 0, StatusUnconfigured, ReasonUnconfigured},
		{"disabled", &Configuration{FlagKey: "f", Enabled: false, Percentage: 100}, 0, StatusOff, ReasonDisabled},
		{"window not started", &Configuration{FlagKey: "f", Enabled: true, Percentage: 100, Window: Window{StartsAt: ptr(10)}}, 5, StatusOff, ReasonWindowInactive},
		{"window ended", &Configuration{FlagKey: "f", Enabled: true, Percentage: 100, Window: Window{EndsAt: ptr(10)}}, 10, StatusOff, ReasonWindowInactive},
		{"rollout miss", &Configuration{FlagKey: "f", Enabled: true, Percentage: 0}, 0, StatusOff, ReasonRolloutMiss},
		{"enabled", &Configuration{FlagKey: "f", Enabled: true, Percentage: 100}, 0, StatusOn, ReasonEnabled},
	}
	for _, tc := range cases {
		status, reason := Explained("e", tc.cfg, "m", tc.at)
		if status != tc.status || reason != tc.reason {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, status, reason, tc.status, tc.reason)
		}
		if got := Evaluated("e", tc.cfg, "m", tc.at); got != tc.status {
			t.Errorf("%s: Evaluated = %v, want %v (surfaces must agree)", tc.name, got, tc.status)
		}
	}
}

func TestExplainedDecisionOrder(t *testing.T) {
	// Disabled wins over an inactive window and a rollout miss.
	disabled := &Configuration{FlagKey: "f", Enabled: false, Percentage: 0, Window: Window{EndsAt: ptr(10)}}
	if _, reason := Explained("e", disabled, "m", 20); reason != ReasonDisabled {
		t.Errorf("disabled+inactive window+miss reason = %v, want disabled", reason)
	}
	// An inactive window wins over a rollout miss.
	inactive := &Configuration{FlagKey: "f", Enabled: true, Percentage: 0, Window: Window{EndsAt: ptr(10)}}
	if _, reason := Explained("e", inactive, "m", 20); reason != ReasonWindowInactive {
		t.Errorf("inactive window+miss reason = %v, want window_inactive", reason)
	}
}
