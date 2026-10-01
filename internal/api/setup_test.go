package api

import "testing"

type seededHarnessT struct {
	*apiHarness
	v1 map[string]any
	v2 map[string]any
	v3 map[string]any
}

func seededHarness(t *testing.T) *seededHarnessT {
	t.Helper()
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	h.setTime("2026-01-01T02:00:00Z")
	v1 := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)

	h.setTime("2026-01-01T03:00:00Z")
	v2 := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":50}`)

	h.setTime("2026-01-01T06:00:00Z")
	h.createFlag(t, "coupon")
	h.putConfig(t, "prod", "coupon", `{"enabled":true,"percentage":100}`)

	h.setTime("2026-01-01T08:00:00Z")
	v3 := h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")

	h.setTime("2026-01-02T00:00:00Z")
	return &seededHarnessT{apiHarness: h, v1: v1, v2: v2, v3: v3}
}
