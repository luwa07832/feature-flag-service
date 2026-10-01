package api

import (
	"net/http"
	"testing"
)

func TestEvaluateRealtimeUsesCurrentConfig(t *testing.T) {
	h := seededHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=alpha", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	checkout := findFlag(t, asFlags(t, body), "checkout")
	if checkout["status"] != "unconfigured" {
		t.Errorf("checkout was deleted at current time, status = %v, want unconfigured", checkout["status"])
	}
	coupon := findFlag(t, asFlags(t, body), "coupon")
	if coupon["status"] != "on" {
		t.Errorf("coupon 100%% status = %v, want on", coupon["status"])
	}
}

func TestEvaluateRealtimeMarkerValidation(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	if rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing marker status = %d", rec.Code)
	} else {
		assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
	}
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=Bad%20Marker", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
}

func TestEvaluateRealtimeUnknownEnvironment(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/nope/evaluate?marker=u1", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestEvaluateRealtimeDeterministicAcrossRepeatedCalls(t *testing.T) {
	h := seededHarness(t)
	first := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=alpha", "").Body.String()
	second := h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=alpha", "").Body.String()
	if first != second {
		t.Fatalf("real-time evaluation not stable:\n%s\n%s", first, second)
	}
}

func TestHistoryEndpointReturnsFullVersionChain(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	versions, ok := body["versions"].([]any)
	if !ok {
		t.Fatalf("versions not an array: %#v", body["versions"])
	}
	if len(versions) != 3 {
		t.Fatalf("versions len = %d, want 3 (2 puts + 1 tombstone)", len(versions))
	}
	last := asMap(t, versions[2])
	if last["tombstone"] != true {
		t.Errorf("last version tombstone = %v, want true", last["tombstone"])
	}
}

func TestHistoryEndpointUnknownEnvironment(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/nope/flags/x/history", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
}

func TestConfigLifecycleValidation(t *testing.T) {
	h := newHarness(t)
	// Unknown resources.
	rec := h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":10}`)
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
	h.createEnv(t, "prod")
	rec = h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":10}`)
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")

	h.createFlag(t, "checkout")
	rec = h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":101}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	rec = h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":-1}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	rec = h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":10,"window":{"starts_at":"2026-13-01T00:00:00Z"}}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
	rec = h.do(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":10,"extra":1}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")

	created := h.must(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config", `{"enabled":true,"percentage":10}`)
	if created["version"] == nil || created["version"] == "" {
		t.Fatalf("created version missing: %#v", created)
	}

	// Delete is idempotent-checked: second delete reports not found.
	h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")
	rec = h.do(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
}

func TestCreateEnvironmentAndFlagIdempotency(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/environments", `{"key":"prod"}`)
	rec := h.do(t, http.MethodPost, "/api/v1/environments", `{"key":"prod"}`)
	assertErrorCode(t, rec, http.StatusConflict, "AlreadyExists")
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	rec = h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	assertErrorCode(t, rec, http.StatusConflict, "AlreadyExists")
	rec = h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"BAD KEY"}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
}
