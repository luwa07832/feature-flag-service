package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/luwa07832/feature-flag-service/internal/eval"
)

func explainURL(env, flag, query string) string {
	return "/api/v1/environments/" + env + "/flags/" + flag + "/explain?" + query
}

func TestExplainInvalidPathKey(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, explainURL("Prod", "checkout", "marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	rec = h.do(t, http.MethodGet, explainURL("prod", "Checkout", "marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
}

func TestExplainEmptyTimestamp(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	rec := h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=&marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
}

func TestExplainInvalidTimestamp(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	rec := h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=not-a-time&marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
}

func TestExplainMarkerValidation(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	rec := h.do(t, http.MethodGet, explainURL("prod", "checkout", ""), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
	rec = h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker="), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
	rec = h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker=Bad%20Marker"), "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
}

func TestExplainUnknownEnvironment(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, explainURL("missing", "checkout", "marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestExplainUnknownFlag(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	rec := h.do(t, http.MethodGet, explainURL("prod", "missing", "marker=alpha"), "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
}

func TestExplainValidationOrder(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	cases := []struct {
		name   string
		url    string
		status int
		code   string
	}{
		{"path before at", explainURL("Prod", "checkout", "at=bad&marker=Bad%20Marker"), http.StatusBadRequest, "InvalidRequest"},
		{"at before marker", explainURL("prod", "checkout", "at=bad&marker=Bad%20Marker"), http.StatusBadRequest, "InvalidTimestamp"},
		{"marker before environment", explainURL("missing", "checkout", "marker=Bad%20Marker"), http.StatusBadRequest, "InvalidMarker"},
		{"environment before flag", explainURL("missing", "missing", "marker=alpha"), http.StatusNotFound, "EnvironmentNotFound"},
	}
	for _, tc := range cases {
		rec := h.do(t, http.MethodGet, tc.url, "")
		assertErrorCode(t, rec, tc.status, tc.code)
	}
}

func TestExplainEnabled(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	created := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)

	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T02:00:00Z&marker=alpha"), ""))
	if body["environment"] != "prod" || body["flag_key"] != "checkout" || body["marker"] != "alpha" {
		t.Errorf("echo fields wrong: %#v", body)
	}
	if body["evaluated_at"] != "2026-01-01T02:00:00Z" {
		t.Errorf("evaluated_at = %v", body["evaluated_at"])
	}
	if body["status"] != "on" || body["reason"] != "enabled" {
		t.Errorf("status/reason = %v/%v, want on/enabled", body["status"], body["reason"])
	}
	config := asMap(t, body["config"])
	if config["version"] != created["version"] {
		t.Errorf("config.version = %v, want %s", config["version"], created["version"])
	}
	if config["enabled"] != true || number(config["percentage"]) != 100 {
		t.Errorf("config enabled/percentage wrong: %#v", config)
	}
	window := asMap(t, config["window"])
	if window["starts_at"] != nil || window["ends_at"] != nil {
		t.Errorf("empty window endpoints must stay null: %#v", window)
	}
}

func TestExplainDisabled(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":false,"percentage":100}`)

	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker=alpha"), ""))
	if body["status"] != "off" || body["reason"] != "disabled" {
		t.Errorf("status/reason = %v/%v, want off/disabled", body["status"], body["reason"])
	}
	if body["config"] == nil {
		t.Errorf("config must be present for a disabled flag")
	}
}

func TestExplainWindowInactive(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)

	before := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T02:59:59Z&marker=alpha"), ""))
	if before["status"] != "off" || before["reason"] != "window_inactive" {
		t.Errorf("before window = %v/%v, want off/window_inactive", before["status"], before["reason"])
	}
	atStart := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T03:00:00Z&marker=alpha"), ""))
	if atStart["status"] != "on" || atStart["reason"] != "enabled" {
		t.Errorf("at window start (inclusive) = %v/%v, want on/enabled", atStart["status"], atStart["reason"])
	}
	atEnd := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T06:00:00Z&marker=alpha"), ""))
	if atEnd["status"] != "off" || atEnd["reason"] != "window_inactive" {
		t.Errorf("at window end (exclusive) = %v/%v, want off/window_inactive", atEnd["status"], atEnd["reason"])
	}
	window := asMap(t, asMap(t, before["config"])["window"])
	if window["starts_at"] != "2026-01-01T03:00:00Z" || window["ends_at"] != "2026-01-01T06:00:00Z" {
		t.Errorf("config window wrong: %#v", window)
	}
}

func TestExplainRolloutMiss(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":50}`)

	rollout := eval.Rollout{FlagKey: "checkout", Environment: "prod", Percentage: 50}
	var served, missed string
	for i := 0; served == "" || missed == ""; i++ {
		candidate := fmt.Sprintf("user-%d", i)
		if rollout.Served(candidate) && served == "" {
			served = candidate
		}
		if !rollout.Served(candidate) && missed == "" {
			missed = candidate
		}
	}
	onBody := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker="+served), ""))
	if onBody["status"] != "on" || onBody["reason"] != "enabled" {
		t.Errorf("served marker = %v/%v, want on/enabled", onBody["status"], onBody["reason"])
	}
	offBody := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker="+missed), ""))
	if offBody["status"] != "off" || offBody["reason"] != "rollout_miss" {
		t.Errorf("missed marker = %v/%v, want off/rollout_miss", offBody["status"], offBody["reason"])
	}
}

func TestExplainUnconfiguredWithoutAnyConfig(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker=alpha"), ""))
	if body["status"] != "unconfigured" || body["reason"] != "unconfigured" {
		t.Errorf("status/reason = %v/%v, want unconfigured/unconfigured", body["status"], body["reason"])
	}
	if body["config"] != nil {
		t.Errorf("config = %#v, want null", body["config"])
	}
}

func TestExplainUnconfiguredAfterTombstone(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T09:00:00Z&marker=alpha"), ""))
	if body["status"] != "unconfigured" || body["reason"] != "unconfigured" {
		t.Errorf("status/reason = %v/%v, want unconfigured/unconfigured", body["status"], body["reason"])
	}
	if body["config"] != nil {
		t.Errorf("config = %#v, want null after tombstone", body["config"])
	}
}

func TestExplainBeforeFirstConfigDoesNotBackfill(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at=2026-01-01T01:00:00Z&marker=alpha"), ""))
	if body["status"] != "unconfigured" || body["reason"] != "unconfigured" {
		t.Errorf("status/reason = %v/%v, want unconfigured/unconfigured", body["status"], body["reason"])
	}
	if body["config"] != nil {
		t.Errorf("config = %#v, want null (later versions must not backfill)", body["config"])
	}
}

func TestExplainRestoresVersionAt(t *testing.T) {
	h := seededHarness(t)
	cases := []struct {
		at      string
		version any
	}{
		{"2026-01-01T02:30:00Z", h.v1["version"]},
		{"2026-01-01T03:00:00Z", h.v2["version"]},
		{"2026-01-01T05:00:00Z", h.v2["version"]},
	}
	for _, tc := range cases {
		body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "at="+tc.at+"&marker=alpha"), ""))
		config := asMap(t, body["config"])
		if config["version"] != tc.version {
			t.Errorf("at %s: version = %v, want %v", tc.at, config["version"], tc.version)
		}
	}
}

func TestExplainDefaultsToCurrentTime(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)
	h.setTime("2026-01-01T05:00:00Z")

	body := decodeBody(t, h.do(t, http.MethodGet, explainURL("prod", "checkout", "marker=alpha"), ""))
	if body["evaluated_at"] != "2026-01-01T05:00:00Z" {
		t.Errorf("evaluated_at = %v, want service current time", body["evaluated_at"])
	}
	if body["status"] != "on" || body["reason"] != "enabled" {
		t.Errorf("status/reason = %v/%v, want on/enabled", body["status"], body["reason"])
	}
}

func TestExplainIsIdempotentAndDeterministic(t *testing.T) {
	h := seededHarness(t)
	url := explainURL("prod", "checkout", "at=2026-01-01T05:00:00Z&marker=alpha")
	first := h.do(t, http.MethodGet, url, "").Body.String()
	second := h.do(t, http.MethodGet, url, "").Body.String()
	if first != second {
		t.Fatalf("repeat queries differ:\n%s\n%s", first, second)
	}
	historyBefore := h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	_ = h.do(t, http.MethodGet, url, "")
	_ = h.do(t, http.MethodGet, url, "")
	historyAfter := h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	if historyBefore.Body.String() != historyAfter.Body.String() {
		t.Fatalf("explain wrote history:\n%s\n%s", historyBefore.Body.String(), historyAfter.Body.String())
	}
}
