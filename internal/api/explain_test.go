package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type requestDoer interface {
	do(t *testing.T, method, path, body string) *httptest.ResponseRecorder
}

func explainURL(env, flag, query string) string {
	return "/api/v1/environments/" + env + "/flags/" + flag + "/explain?" + query
}

func explainBody(t *testing.T, h requestDoer, url string) map[string]any {
	t.Helper()
	rec := h.do(t, http.MethodGet, url, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s -> %d %s", url, rec.Code, rec.Body.String())
	}
	return decodeBody(t, rec)
}

func TestExplainRealtimeUsesCurrentVersionAndOnReason(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	version := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)

	body := explainBody(t, h, explainURL("prod", "checkout", "marker=alpha"))
	if body["environment"] != "prod" || body["flag_key"] != "checkout" || body["marker"] != "alpha" {
		t.Fatalf("identity fields wrong: %#v", body)
	}
	if body["evaluated_at"] != "2026-01-01T01:00:00Z" {
		t.Errorf("evaluated_at = %v, want the current service time", body["evaluated_at"])
	}
	if body["status"] != "on" || body["reason"] != "enabled" {
		t.Errorf("status/reason = %v/%v, want on/enabled", body["status"], body["reason"])
	}
	cfg := asMap(t, body["config"])
	if cfg["version"] != version["version"] || cfg["enabled"] != true || number(cfg["percentage"]) != 100 {
		t.Errorf("config payload wrong: %#v", cfg)
	}
	window := asMap(t, cfg["window"])
	if window["starts_at"] != nil || window["ends_at"] != nil {
		t.Errorf("empty window endpoints must be null: %#v", window)
	}
}

func TestExplainDisabledReason(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":false,"percentage":100}`)

	body := explainBody(t, h, explainURL("prod", "checkout", "marker=alpha"))
	if body["status"] != "off" || body["reason"] != "disabled" {
		t.Fatalf("status/reason = %v/%v, want off/disabled", body["status"], body["reason"])
	}
}

func TestExplainWindowInactiveReason(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)

	before := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T02:59:59Z&marker=alpha"))
	if before["status"] != "off" || before["reason"] != "window_inactive" {
		t.Errorf("before window: status/reason = %v/%v", before["status"], before["reason"])
	}
	// Start is inclusive.
	start := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T03:00:00Z&marker=alpha"))
	if start["status"] != "on" || start["reason"] != "enabled" {
		t.Errorf("at window start: status/reason = %v/%v", start["status"], start["reason"])
	}
	// End is exclusive.
	end := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T06:00:00Z&marker=alpha"))
	if end["status"] != "off" || end["reason"] != "window_inactive" {
		t.Errorf("at window end: status/reason = %v/%v", end["status"], end["reason"])
	}
}

func TestExplainRolloutMissReason(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":0}`)

	body := explainBody(t, h, explainURL("prod", "checkout", "marker=alpha"))
	if body["status"] != "off" || body["reason"] != "rollout_miss" {
		t.Fatalf("status/reason = %v/%v, want off/rollout_miss", body["status"], body["reason"])
	}
}

func TestExplainDisabledBeatsWindowAndRollout(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":false,"percentage":0,"window":{"starts_at":"2026-01-01T03:00:00Z"}}`)

	body := explainBody(t, h, explainURL("prod", "checkout", "marker=alpha"))
	if body["reason"] != "disabled" {
		t.Fatalf("reason = %v, want disabled first in the order", body["reason"])
	}
}

func TestExplainUnconfiguredBeforeFirstVersion(t *testing.T) {
	h := seededHarness(t)
	body := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T01:00:00Z&marker=alpha"))
	if body["status"] != "unconfigured" || body["reason"] != "unconfigured" {
		t.Errorf("status/reason = %v/%v, want unconfigured/unconfigured", body["status"], body["reason"])
	}
	if body["config"] != nil {
		t.Errorf("config = %v, want null", body["config"])
	}
	if body["evaluated_at"] != "2026-01-01T01:00:00Z" {
		t.Errorf("evaluated_at = %v", body["evaluated_at"])
	}
}

func TestExplainUnconfiguredAfterTombstone(t *testing.T) {
	h := seededHarness(t)
	body := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T09:00:00Z&marker=alpha"))
	if body["status"] != "unconfigured" || body["reason"] != "unconfigured" {
		t.Errorf("status/reason = %v/%v, want unconfigured/unconfigured", body["status"], body["reason"])
	}
	if body["config"] != nil {
		t.Errorf("config = %v, want null", body["config"])
	}
}

func TestExplainSelectsLastVersionNotLaterThanAt(t *testing.T) {
	h := seededHarness(t)
	// Between v2 (03:00) and the tombstone (08:00): v2 at 50% must win.
	body := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T05:00:00Z&marker=alpha"))
	cfg := asMap(t, body["config"])
	if cfg["version"] != h.v2["version"] {
		t.Fatalf("version = %v, want %s", cfg["version"], h.v2["version"])
	}
	if number(cfg["percentage"]) != 50 {
		t.Errorf("percentage = %v, want 50", cfg["percentage"])
	}
	// changed_at <= at boundary selects the new version.
	atChange := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T03:00:00Z&marker=alpha"))
	if asMap(t, atChange["config"])["version"] != h.v2["version"] {
		t.Errorf("at changed_at should select v2")
	}
}

func TestExplainSameInstantTieBreaksByWriteOrder(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T04:00:00Z")
	first := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)
	second := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":20}`)

	body := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T04:00:00Z&marker=alpha"))
	cfg := asMap(t, body["config"])
	if cfg["version"] != second["version"] {
		t.Errorf("version = %v, want last write %s (first %s)", cfg["version"], second["version"], first["version"])
	}
}

func TestExplainNormalizesOffsetToUTC(t *testing.T) {
	h := seededHarness(t)
	body := explainBody(t, h, explainURL("prod", "checkout", "at=2026-01-01T13:00:00%2B08:00&marker=alpha"))
	if body["evaluated_at"] != "2026-01-01T05:00:00Z" {
		t.Errorf("evaluated_at = %v, want canonical UTC", body["evaluated_at"])
	}
	if asMap(t, body["config"])["version"] != h.v2["version"] {
		t.Errorf("offset instant should restore v2: %#v", body["config"])
	}
}

func TestExplainIsStableAndWritesNoHistory(t *testing.T) {
	h := seededHarness(t)
	url := explainURL("prod", "checkout", "at=2026-01-01T05:00:00Z&marker=alpha")
	first := h.do(t, http.MethodGet, url, "").Body.String()
	second := h.do(t, http.MethodGet, url, "").Body.String()
	if first != second {
		t.Fatalf("repeat explain differs:\n%s\n%s", first, second)
	}
	historyBefore := h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	_ = h.do(t, http.MethodGet, url, "")
	historyAfter := h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	if historyBefore.Body.String() != historyAfter.Body.String() {
		t.Fatalf("explain wrote history")
	}
}

func TestExplainValidation(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)

	// Invalid path is rejected first, even with other bad parameters.
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("BadEnv", "checkout", "at=bad&marker="), ""),
		http.StatusBadRequest, "InvalidRequest")
	// at, when supplied, must not be empty or malformed.
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=&marker=alpha"), ""),
		http.StatusBadRequest, "InvalidTimestamp")
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=not-a-time&marker=alpha"), ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// marker: missing, empty, malformed.
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T02:00:00Z"), ""),
		http.StatusBadRequest, "InvalidMarker")
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T02:00:00Z&marker="), ""),
		http.StatusBadRequest, "InvalidMarker")
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T02:00:00Z&marker=Bad/Marker"), ""),
		http.StatusBadRequest, "InvalidMarker")
	// Environment before flag in the resource order.
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("missing", "checkout", "marker=alpha"), ""),
		http.StatusNotFound, "EnvironmentNotFound")
	assertErrorCode(t,
		h.do(t, http.MethodGet, explainURL("prod", "missing", "marker=alpha"), ""),
		http.StatusNotFound, "FlagNotFound")
}

func TestExplainExistingSurfacesUnchanged(t *testing.T) {
	h := seededHarness(t)
	// The real-time entry and history entry still behave as before after the
	// new route was registered.
	rt := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=alpha", ""))
	if findFlag(t, asFlags(t, rt), "checkout")["status"] != "unconfigured" {
		t.Errorf("real-time evaluate behavior changed")
	}
	history := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	if len(asAnySlice(t, history["versions"])) != 3 {
		t.Errorf("history chain changed: %#v", history["versions"])
	}
}

func asAnySlice(t *testing.T, v any) []any {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", v)
	}
	return items
}
