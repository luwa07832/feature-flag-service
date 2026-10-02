package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// changesHarness seeds two flags with a create, an update, an all-empty
// create, a tombstone and a re-create so every action kind appears.
func changesHarness(t *testing.T) *apiHarness {
	t.Helper()
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.createFlag(t, "coupon")

	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)

	h.setTime("2026-01-01T03:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":50,"window":{"starts_at":"2026-01-01T04:00:00Z"}}`)

	h.setTime("2026-01-01T04:00:00Z")
	h.putConfig(t, "prod", "coupon", `{"enabled":false,"percentage":0}`)

	h.setTime("2026-01-01T05:00:00Z")
	h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")

	h.setTime("2026-01-01T06:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":false,"percentage":25}`)
	return h
}

func TestChangesFeedClassifiesActionsAcrossFlags(t *testing.T) {
	h := changesHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", ""))
	if body["environment"] != "prod" {
		t.Fatalf("environment = %v, want prod", body["environment"])
	}
	if len(body) != 2 {
		t.Fatalf("top-level keys = %v, want exactly environment and items", body)
	}
	items := asItems(t, body)
	if len(items) != 5 {
		t.Fatalf("items len = %d, want 5", len(items))
	}

	created := asMap(t, items[0])
	assertItem(t, created, "checkout", "created", "2026-01-01T02:00:00Z")
	assertFields(t, created, []string{"enabled", "percentage"})
	assertJSON(t, created["before"], `null`)
	assertJSON(t, created["after"], `{"enabled":true,"percentage":10,"window":{"ends_at":null,"starts_at":null}}`)

	updated := asMap(t, items[1])
	assertItem(t, updated, "checkout", "updated", "2026-01-01T03:00:00Z")
	assertFields(t, updated, []string{"percentage", "window.starts_at"})
	assertJSON(t, updated["before"], `{"enabled":true,"percentage":10,"window":{"ends_at":null,"starts_at":null}}`)
	assertJSON(t, updated["after"], `{"enabled":true,"percentage":50,"window":{"ends_at":null,"starts_at":"2026-01-01T04:00:00Z"}}`)

	emptyCreate := asMap(t, items[2])
	assertItem(t, emptyCreate, "coupon", "created", "2026-01-01T04:00:00Z")
	assertFields(t, emptyCreate, []string{})
	assertJSON(t, emptyCreate["after"], `{"enabled":false,"percentage":0,"window":{"ends_at":null,"starts_at":null}}`)

	deleted := asMap(t, items[3])
	assertItem(t, deleted, "checkout", "deleted", "2026-01-01T05:00:00Z")
	assertFields(t, deleted, []string{"enabled", "percentage", "window.starts_at"})
	assertJSON(t, deleted["before"], `{"enabled":true,"percentage":50,"window":{"ends_at":null,"starts_at":"2026-01-01T04:00:00Z"}}`)
	assertJSON(t, deleted["after"], `null`)

	recreated := asMap(t, items[4])
	assertItem(t, recreated, "checkout", "created", "2026-01-01T06:00:00Z")
	assertFields(t, recreated, []string{"percentage"})
	assertJSON(t, recreated["before"], `null`)
}

func TestChangesItemVersionMatchesHistory(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")
	put := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", ""))
	items := asItems(t, body)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	item := asMap(t, items[0])
	if item["version"] != put["version"] {
		t.Errorf("version = %v, want %v from the write response", item["version"], put["version"])
	}
}

func TestChangesFilterByFlagKey(t *testing.T) {
	h := changesHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?flagKey=checkout", ""))
	items := asItems(t, body)
	if len(items) != 4 {
		t.Fatalf("checkout items len = %d, want 4", len(items))
	}
	for _, raw := range items {
		if item := asMap(t, raw); item["flag_key"] != "checkout" {
			t.Fatalf("flag_key = %v, want checkout", item["flag_key"])
		}
	}
	body = decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?flagKey=coupon", ""))
	if items := asItems(t, body); len(items) != 1 {
		t.Fatalf("coupon items len = %d, want 1", len(items))
	}
}

func TestChangesTimeRangeFilters(t *testing.T) {
	h := changesHarness(t)

	// from is inclusive and classification still sees the full history.
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?from=2026-01-01T03:00:00Z", ""))
	items := asItems(t, body)
	if len(items) != 4 {
		t.Fatalf("from-filter items len = %d, want 4", len(items))
	}
	first := asMap(t, items[0])
	if first["changed_at"] != "2026-01-01T03:00:00Z" || first["action"] != "updated" {
		t.Fatalf("first item = %v %v, want updated at 03:00", first["action"], first["changed_at"])
	}
	assertJSON(t, first["before"], `{"enabled":true,"percentage":10,"window":{"ends_at":null,"starts_at":null}}`)

	// to is exclusive.
	body = decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?to=2026-01-01T03:00:00Z", ""))
	items = asItems(t, body)
	if len(items) != 1 {
		t.Fatalf("to-filter items len = %d, want 1", len(items))
	}

	body = decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?from=2026-01-01T03:00:00Z&to=2026-01-01T05:00:00Z", ""))
	items = asItems(t, body)
	if len(items) != 2 {
		t.Fatalf("range items len = %d, want 2 (03:00 and 04:00)", len(items))
	}
}

func TestChangesRejectsInvalidQueries(t *testing.T) {
	h := changesHarness(t)
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/environments/prod/changes?from=nope", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?from=", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?to=2026-13-01T00:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?from=2026-01-01T05:00:00Z&to=2026-01-01T02:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?from=2026-01-01T05:00:00Z&to=2026-01-01T05:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?flagKey=BAD%20KEY", http.StatusBadRequest, "InvalidRequest"},
		{"/api/v1/environments/prod/changes?flagKey=", http.StatusBadRequest, "InvalidRequest"},
		{"/api/v1/environments/BAD%20ENV/changes", http.StatusBadRequest, "InvalidRequest"},
		// Validation order: path, from, to, flagKey.
		{"/api/v1/environments/BAD%20ENV/changes?from=nope", http.StatusBadRequest, "InvalidRequest"},
		{"/api/v1/environments/prod/changes?from=nope&flagKey=BAD%20KEY", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?to=nope&flagKey=BAD%20KEY", http.StatusBadRequest, "InvalidTimestamp"},
		{"/api/v1/environments/prod/changes?from=2026-01-01T05:00:00Z&to=2026-01-01T02:00:00Z&flagKey=BAD%20KEY", http.StatusBadRequest, "InvalidTimestamp"},
	}
	for _, tc := range cases {
		rec := h.do(t, http.MethodGet, tc.path, "")
		assertErrorCode(t, rec, tc.status, tc.code)
	}
}

func TestChangesNotFound(t *testing.T) {
	h := changesHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/nope/changes", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
	rec = h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?flagKey=ghost", "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
	// Environment existence is checked before flag existence.
	rec = h.do(t, http.MethodGet, "/api/v1/environments/nope/changes?flagKey=ghost", "")
	assertErrorCode(t, rec, http.StatusNotFound, "EnvironmentNotFound")
	// A malformed flagKey never becomes FlagNotFound, even for unknown environments.
	rec = h.do(t, http.MethodGet, "/api/v1/environments/nope/changes?flagKey=BAD%20KEY", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
}

func TestChangesEmptyItems(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("items must be an empty array, body = %s", rec.Body.String())
	}
	// A registered flag without any configuration also yields an empty feed.
	rec = h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?flagKey=checkout", "")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("items must be an empty array, body = %s", rec.Body.String())
	}
}

func TestChangesUpdateWithoutDifferences(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)
	h.setTime("2026-01-01T03:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "")
	body := decodeBody(t, rec)
	items := asItems(t, body)
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	second := asMap(t, items[1])
	if second["action"] != "updated" {
		t.Fatalf("action = %v, want updated", second["action"])
	}
	assertFields(t, second, []string{})
	if !strings.Contains(rec.Body.String(), `"changed_fields":[]`) {
		t.Fatalf("changed_fields must serialize as an empty array, body = %s", rec.Body.String())
	}
}

func TestChangesIsStableAndReadOnly(t *testing.T) {
	h := changesHarness(t)
	first := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "").Body.String()
	second := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "").Body.String()
	if first != second {
		t.Fatalf("repeated queries differ:\n%s\n%s", first, second)
	}
	// The audit query must not append any history.
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	if versions := body["versions"].([]any); len(versions) != 4 {
		t.Fatalf("checkout history len = %d after audit query, want 4", len(versions))
	}
}

func asItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %#v", body["items"])
	}
	return items
}

func assertItem(t *testing.T, item map[string]any, flagKey, action, changedAt string) {
	t.Helper()
	if item["flag_key"] != flagKey {
		t.Errorf("flag_key = %v, want %s", item["flag_key"], flagKey)
	}
	if item["action"] != action {
		t.Errorf("action = %v, want %s", item["action"], action)
	}
	if item["changed_at"] != changedAt {
		t.Errorf("changed_at = %v, want %s", item["changed_at"], changedAt)
	}
	if version, ok := item["version"].(string); !ok || version == "" {
		t.Errorf("version missing: %#v", item["version"])
	}
}

func assertFields(t *testing.T, item map[string]any, want []string) {
	t.Helper()
	raw, ok := item["changed_fields"].([]any)
	if !ok {
		t.Fatalf("changed_fields not an array: %#v", item["changed_fields"])
	}
	if len(raw) != len(want) {
		t.Fatalf("changed_fields = %v, want %v", raw, want)
	}
	for i, field := range want {
		if raw[i] != field {
			t.Fatalf("changed_fields[%d] = %v, want %s (all: %v)", i, raw[i], field, raw)
		}
	}
}

func assertJSON(t *testing.T, value any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != want {
		t.Fatalf("json = %s, want %s", raw, want)
	}
}
