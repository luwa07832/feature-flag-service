package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEvaluateAtInvalidTimestamp(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=not-a-time", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
}

func TestEvaluateAtMissingTimestamp(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
}

func TestEvaluateAtUnknownEnvironment(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/missing/evaluate-at?at=2026-01-01T00:00:00Z", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestEvaluateAtEnvironmentWithoutRestorableConfig(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "empty")
	rec := h.do(t, http.MethodGet, "/api/v1/environments/empty/evaluate-at?at=2026-01-01T00:00:00Z&marker=u1", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestEvaluateAtInvalidMarker(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)

	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-02T00:00:00Z&marker=Bad/Marker", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
}

func TestEvaluateAtWithoutMarkerReturnsSnapshotNotEvaluated(t *testing.T) {
	h := seededHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T05:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["environment"] != "prod" {
		t.Errorf("environment = %v", body["environment"])
	}
	if body["at"] != "2026-01-01T05:00:00Z" {
		t.Errorf("at = %v", body["at"])
	}
	if body["marker"] != nil {
		t.Errorf("marker = %v, want null", body["marker"])
	}
	flags := asFlags(t, body)
	checkout := findFlag(t, flags, "checkout")
	if checkout["status"] != "not_evaluated" {
		t.Errorf("status = %v, want not_evaluated", checkout["status"])
	}
	if checkout["enabled"] != true {
		t.Errorf("enabled = %v, want true", checkout["enabled"])
	}
	if number(checkout["percentage"]) != 50 {
		t.Errorf("percentage = %v, want 50", checkout["percentage"])
	}
	if checkout["version"] != h.v2["version"] {
		t.Errorf("version = %v, want %s", checkout["version"], h.v2["version"])
	}
}

func TestEvaluateAtWithMarkerRestoresAndEvaluates(t *testing.T) {
	h := seededHarness(t)
	url := "/api/v1/environments/prod/evaluate-at?at=2026-01-01T05:00:00Z&marker=alpha"
	body := decodeBody(t, h.do(t, http.MethodGet, url, ""))
	flags := asFlags(t, body)

	checkout := findFlag(t, flags, "checkout")
	if checkout["version"] != h.v2["version"] {
		t.Errorf("checkout version = %v, want %s (later v3 must not be used)", checkout["version"], h.v2["version"])
	}
	if number(checkout["percentage"]) != 50 {
		t.Errorf("checkout percentage = %v, want 50", checkout["percentage"])
	}
	assertStatusOneOf(t, checkout, "on", "off")

	// Flag added later must not leak into the historical snapshot: it is
	// reported with the definitive unconfigured status and null snapshot.
	coupon := findFlag(t, flags, "coupon")
	if coupon["version"] != nil || coupon["percentage"] != nil || coupon["enabled"] != nil || coupon["window"] != nil {
		t.Errorf("later flag snapshot not null: %#v", coupon)
	}
	if coupon["status"] != "unconfigured" {
		t.Errorf("later flag status = %v, want unconfigured", coupon["status"])
	}
}

func TestEvaluateAtIsIdempotentAndDeterministic(t *testing.T) {
	h := seededHarness(t)
	url := "/api/v1/environments/prod/evaluate-at?at=2026-01-01T05:00:00Z&marker=alpha"
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
		t.Fatalf("queries wrote history:\n%s\n%s", historyBefore.Body.String(), historyAfter.Body.String())
	}
}

func TestEvaluateAtRespectsWindow(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T01:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)

	before := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T02:59:59Z&marker=alpha", ""))
	if findFlag(t, asFlags(t, before), "checkout")["status"] != "off" {
		t.Errorf("before window status not off")
	}
	during := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T04:00:00Z&marker=alpha", ""))
	if findFlag(t, asFlags(t, during), "checkout")["status"] != "on" {
		t.Errorf("during window status not on")
	}
	after := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T06:00:00Z&marker=alpha", ""))
	if findFlag(t, asFlags(t, after), "checkout")["status"] != "off" {
		t.Errorf("after window status not off (end is exclusive)")
	}
}

func TestEvaluateAtBeforeAnyConfigVersionUsesEarlierVersion(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T02:30:00Z&marker=alpha", ""))
	checkout := findFlag(t, asFlags(t, body), "checkout")
	if checkout["version"] != h.v1["version"] {
		t.Errorf("version = %v, want %s", checkout["version"], h.v1["version"])
	}
	if number(checkout["percentage"]) != 10 {
		t.Errorf("percentage = %v, want 10", checkout["percentage"])
	}
	assertStatusOneOf(t, checkout, "on", "off")
}

func TestEvaluateAtBoundaryIsInclusive(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T03:00:00Z&marker=alpha", ""))
	if findFlag(t, asFlags(t, body), "checkout")["version"] != h.v2["version"] {
		t.Errorf("at exactly changed_at should select v2 (changed_at <= at)")
	}
}

func TestEvaluateAtAfterDeletionRestoresUnconfigured(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T09:00:00Z&marker=alpha", ""))
	checkout := findFlag(t, asFlags(t, body), "checkout")
	if checkout["version"] != nil || checkout["enabled"] != nil || checkout["percentage"] != nil {
		t.Errorf("deleted config snapshot should be null: %#v", checkout)
	}
	if checkout["status"] != "unconfigured" {
		t.Errorf("status = %v, want unconfigured", checkout["status"])
	}
}

func TestEvaluateAtErrorShape(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/missing/evaluate-at?at=bad", "")
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != "InvalidTimestamp" || envelope.Error.Message == "" {
		t.Fatalf("unexpected error shape: %s", rec.Body.String())
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	body := decodeBody(t, rec)
	errObj := asMap(t, body["error"])
	if errObj["code"] != code {
		t.Fatalf("code = %v, want %s; body=%s", errObj["code"], code, rec.Body.String())
	}
}

func asFlags(t *testing.T, body map[string]any) []any {
	t.Helper()
	flags, ok := body["flags"].([]any)
	if !ok {
		t.Fatalf("flags not an array: %#v", body["flags"])
	}
	return flags
}

func findFlag(t *testing.T, flags []any, key string) map[string]any {
	t.Helper()
	for _, item := range flags {
		flag := asMap(t, item)
		if flag["flag_key"] == key {
			return flag
		}
	}
	t.Fatalf("flag %s not found in %#v", key, flags)
	return nil
}

func number(v any) float64 {
	n, _ := v.(float64)
	return n
}

func assertStatusOneOf(t *testing.T, flag map[string]any, options ...string) {
	t.Helper()
	got, _ := flag["status"].(string)
	for _, option := range options {
		if got == option {
			return
		}
	}
	t.Fatalf("status = %q, want one of %v", got, options)
}
