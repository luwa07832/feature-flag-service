package api

import (
	"net/http"
	"testing"
)

// compareHarness seeds two environments for the checkout flag:
//   - prod: v1 enabled 10% at 02:00, v2 enabled 50% at 03:00, tombstone 08:00
//   - staging: enabled 50% since 01:00 with window [03:00, 06:00)
//
// plus an unconfigured coupon flag.
func compareHarness(t *testing.T) *apiHarness {
	t.Helper()
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createEnv(t, "staging")
	h.createFlag(t, "checkout")
	h.createFlag(t, "coupon")

	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":50,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)

	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)

	h.setTime("2026-01-01T03:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":50}`)

	h.setTime("2026-01-01T08:00:00Z")
	h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")
	return h
}

func comparePath(query string) string {
	return "/api/v1/compare/checkout?" + query
}

func getCompare(t *testing.T, h *apiHarness, query string) (int, map[string]any) {
	t.Helper()
	rec := h.do(t, http.MethodGet, "/api/v1/compare/checkout?"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare -> %d %s", rec.Code, rec.Body.String())
	}
	return rec.Code, decodeBody(t, rec)
}

func assertStrings(t *testing.T, got []any, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fields = %v, want %v", got, want)
		}
	}
}

func TestCompareSameConfiguration(t *testing.T) {
	h := compareHarness(t)
	// At 04:00 prod is enabled 50% with no window; staging is enabled 50% but
	// inside an active window: same enabled/percentage, different window.
	_, body := getCompare(t, h, "source=prod&target=staging&at=2026-01-01T04:00:00Z")
	if body["comparison"] != "different" {
		t.Fatalf("comparison = %v, want different", body["comparison"])
	}
	assertStrings(t, body["changed_fields"].([]any), []string{"window.starts_at", "window.ends_at"})
	if body["source_status"] != "not_evaluated" || body["target_status"] != "not_evaluated" {
		t.Errorf("statuses without marker = %v/%v, want not_evaluated", body["source_status"], body["target_status"])
	}
	if body["marker"] != nil {
		t.Errorf("marker = %v, want null", body["marker"])
	}
	source := asMap(t, body["source"])
	if source["percentage"] != float64(50) {
		t.Errorf("source percentage = %v, want 50", source["percentage"])
	}
	target := asMap(t, body["target"])
	window := asMap(t, target["window"])
	if window["starts_at"] != "2026-01-01T03:00:00Z" || window["ends_at"] != "2026-01-01T06:00:00Z" {
		t.Errorf("target window = %v", window)
	}
	if source["version"] == nil || target["version"] == nil {
		t.Errorf("versions must be present: %v %v", source["version"], target["version"])
	}
}

func TestCompareOnlySource(t *testing.T) {
	h := compareHarness(t)
	// At 00:30 nothing exists yet on either side.
	_, body := getCompare(t, h, "source=prod&target=staging&at=2026-01-01T00:30:00Z")
	if body["comparison"] != "unconfigured_both" {
		t.Fatalf("comparison = %v, want unconfigured_both", body["comparison"])
	}
	assertStrings(t, body["changed_fields"].([]any), []string{})
	if body["source"] != nil || body["target"] != nil {
		t.Errorf("sides must be null: %v %v", body["source"], body["target"])
	}

	// At 01:30 only staging has a configuration.
	_, body = getCompare(t, h, "source=prod&target=staging&at=2026-01-01T01:30:00Z")
	if body["comparison"] != "only_target" {
		t.Fatalf("comparison = %v, want only_target", body["comparison"])
	}
	if body["source"] != nil {
		t.Errorf("source = %v, want null", body["source"])
	}
	assertStrings(t, body["changed_fields"].([]any), []string{"enabled", "percentage", "window.starts_at", "window.ends_at"})

	// At 02:30 both are enabled 50? No: prod is still 10%, so different;
	// check only_source after prod tombstone at 09:00 (staging config remains).
	_, body = getCompare(t, h, "source=prod&target=staging&at=2026-01-01T09:00:00Z")
	if body["comparison"] != "only_target" {
		t.Fatalf("comparison = %v, want only_target (prod tombstoned)", body["comparison"])
	}
}

func TestCompareOnlyTargetAndFlagKey(t *testing.T) {
	h := compareHarness(t)
	// Reverse sides: staging as source at 01:30 means only the source exists.
	_, body := getCompare(t, h, "source=staging&target=prod&at=2026-01-01T01:30:00Z")
	if body["comparison"] != "only_source" {
		t.Fatalf("comparison = %v, want only_source", body["comparison"])
	}
	if body["flag_key"] != "checkout" {
		t.Errorf("flag_key = %v", body["flag_key"])
	}
	if body["at"] != "2026-01-01T01:30:00Z" {
		t.Errorf("at = %v", body["at"])
	}
}

func TestCompareSameIgnoresVersion(t *testing.T) {
	h := compareHarness(t)
	h.createEnv(t, "canary")
	h.setTime("2026-01-01T00:30:00Z")
	h.putConfig(t, "canary", "checkout", `{"enabled":true,"percentage":50}`)
	// At 04:00 prod and canary carry identical fields but distinct versions.
	_, body := getCompare(t, h, "source=prod&target=canary&at=2026-01-01T04:00:00Z")
	if body["comparison"] != "same" {
		t.Fatalf("comparison = %v, want same", body["comparison"])
	}
	assertStrings(t, body["changed_fields"].([]any), []string{})
	prodVersion := asMap(t, body["source"])["version"]
	canaryVersion := asMap(t, body["target"])["version"]
	if prodVersion == canaryVersion {
		t.Fatalf("versions differ per environment but got %v", prodVersion)
	}
}

func TestCompareSameInstantTieBreaksByWriteOrder(t *testing.T) {
	h := compareHarness(t)
	// Two writes at the same changed_at on staging: the later write wins.
	h.setTime("2026-01-01T05:00:00Z")
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":25}`)
	h.putConfig(t, "staging", "checkout", `{"enabled":false,"percentage":25}`)
	_, body := getCompare(t, h, "source=staging&target=prod&at=2026-01-01T05:00:00Z")
	target := asMap(t, body["source"])
	if target["enabled"] != false {
		t.Errorf("later same-instant write must win, source = %v", target)
	}
	assertStrings(t, body["changed_fields"].([]any), []string{"enabled", "percentage"})
}

func TestCompareMarkerEvaluatesBothSides(t *testing.T) {
	h := compareHarness(t)
	marker := "alpha"
	// At 04:00 prod is enabled 50% unconstrained; staging is enabled 50% but
	// inside its active window, so statuses follow the fixed gate order.
	_, body := getCompare(t, h, "source=prod&target=staging&at=2026-01-01T04:00:00Z&marker="+marker)
	if body["marker"] != marker {
		t.Errorf("marker = %v, want alpha", body["marker"])
	}
	for _, side := range []string{"source_status", "target_status"} {
		status := body[side]
		if status != "on" && status != "off" {
			t.Errorf("%s = %v, want on or off", side, status)
		}
	}

	// At 02:30 staging's window has not started, so its status is off while
	// prod (10%) is evaluated independently; no side leaks the other's gates.
	_, body = getCompare(t, h, "source=staging&target=prod&at=2026-01-01T02:30:00Z&marker=alpha")
	if body["source_status"] != "off" {
		t.Errorf("staging outside window status = %v, want off", body["source_status"])
	}

	// Tombstoned side is unconfigured with a marker.
	_, body = getCompare(t, h, "source=prod&target=staging&at=2026-01-01T09:00:00Z&marker=alpha")
	if body["source_status"] != "unconfigured" {
		t.Errorf("tombstoned prod status = %v, want unconfigured", body["source_status"])
	}
	if body["target_status"] != "on" && body["target_status"] != "off" {
		t.Errorf("staging status = %v, want on or off", body["target_status"])
	}
}

func TestCompareIsPureReadAndDeterministic(t *testing.T) {
	h := compareHarness(t)
	query := "source=prod&target=staging&at=2026-01-01T04:00:00Z&marker=alpha"
	first := h.do(t, http.MethodGet, comparePath(query), "").Body.String()
	second := h.do(t, http.MethodGet, comparePath(query), "").Body.String()
	if first != second {
		t.Fatalf("compare not stable:\n%s\n%s", first, second)
	}
	history := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	versions := history["versions"].([]any)
	if len(versions) != 3 {
		t.Fatalf("compare appended history: got %d versions, want 3", len(versions))
	}
}

func TestCompareValidation(t *testing.T) {
	h := compareHarness(t)
	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{"bad flag key", "/api/v1/compare/Bad%20Key?source=prod&target=staging&at=2026-01-01T04:00:00Z", http.StatusBadRequest, "InvalidRequest"},
		{"missing source", comparePath("target=staging&at=2026-01-01T04:00:00Z"), http.StatusBadRequest, "InvalidRequest"},
		{"missing target", comparePath("source=prod&at=2026-01-01T04:00:00Z"), http.StatusBadRequest, "InvalidRequest"},
		{"bad source", comparePath("source=Bad%20Env&target=staging&at=2026-01-01T04:00:00Z"), http.StatusBadRequest, "InvalidRequest"},
		{"same sides", comparePath("source=prod&target=prod&at=2026-01-01T04:00:00Z"), http.StatusBadRequest, "InvalidRequest"},
		{"missing at", comparePath("source=prod&target=staging"), http.StatusBadRequest, "InvalidTimestamp"},
		{"bad at", comparePath("source=prod&target=staging&at=not-a-time"), http.StatusBadRequest, "InvalidTimestamp"},
		{"bad marker", comparePath("source=prod&target=staging&at=2026-01-01T04:00:00Z&marker=Bad%20Marker"), http.StatusBadRequest, "InvalidMarker"},
		{"missing source env", comparePath("source=ghost&target=staging&at=2026-01-01T04:00:00Z"), http.StatusNotFound, "EnvironmentNotFound"},
		{"missing target env", comparePath("source=prod&target=ghost&at=2026-01-01T04:00:00Z"), http.StatusNotFound, "EnvironmentNotFound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, tc.path, "")
			assertErrorCode(t, rec, tc.status, tc.code)
		})
	}
}

func TestCompareValidationOrderSourceBeforeTarget(t *testing.T) {
	h := compareHarness(t)
	// Both environments unknown: source is checked first, still 404 but
	// confirms the single-error shape; target unknown while source valid also
	// resolves to the same code.
	rec := h.do(t, http.MethodGet, comparePath("source=ghost1&target=ghost2&at=2026-01-01T04:00:00Z"), "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestCompareUnknownFlag(t *testing.T) {
	h := compareHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/compare/missing?source=prod&target=staging&at=2026-01-01T04:00:00Z", "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
}

func TestCompareUnconfiguredFlagKnownReturnsOK(t *testing.T) {
	h := compareHarness(t)
	// coupon exists but never had a configuration anywhere: still a valid
	// comparison, unconfigured on both sides.
	rec := h.do(t, http.MethodGet, "/api/v1/compare/coupon?source=prod&target=staging&at=2026-01-01T04:00:00Z&marker=alpha", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["comparison"] != "unconfigured_both" {
		t.Errorf("comparison = %v, want unconfigured_both", body["comparison"])
	}
	if body["source_status"] != "unconfigured" || body["target_status"] != "unconfigured" {
		t.Errorf("statuses = %v/%v, want unconfigured", body["source_status"], body["target_status"])
	}
}

func TestCompareTopLevelShape(t *testing.T) {
	h := compareHarness(t)
	_, body := getCompare(t, h, "source=prod&target=staging&at=2026-01-01T04:00:00Z")
	for _, key := range []string{"source", "target", "flag_key", "at", "marker", "changed_fields", "comparison", "source_status", "target_status"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing top-level field %q in %v", key, body)
		}
	}
	if len(body) != 9 {
		t.Errorf("top-level field count = %d, want 9", len(body))
	}
	source := asMap(t, body["source"])
	for _, key := range []string{"version", "enabled", "percentage", "window"} {
		if _, ok := source[key]; !ok {
			t.Errorf("source config missing %q", key)
		}
	}
	window := asMap(t, source["window"])
	if _, ok := window["starts_at"]; !ok {
		t.Errorf("window missing starts_at")
	}
	if _, ok := window["ends_at"]; !ok {
		t.Errorf("window missing ends_at")
	}
	if window["starts_at"] != nil || window["ends_at"] != nil {
		t.Errorf("empty window endpoints must be null: %v", window)
	}
}
