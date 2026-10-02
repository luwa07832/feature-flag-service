package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func createFlagFull(t *testing.T, h *apiHarness, payload string) map[string]any {
	t.Helper()
	return h.must(t, http.MethodPost, "/api/v1/flags", payload)
}

func TestDefinitionHistoryRecordsCreateAndUpdate(t *testing.T) {
	h := newHarness(t)
	created := createFlagFull(t, h,
		`{"key":"checkout","description":"First","labels":["zeta","alpha","alpha"]}`)

	h.setTime("2026-02-01T00:00:00Z")
	updated := h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"Second","labels":["beta"]}`)

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags/checkout/definition-history", ""))
	if body["flag_key"] != "checkout" {
		t.Fatalf("flag_key = %v", body["flag_key"])
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v, want 2", body["items"])
	}

	first := asMap(t, items[0])
	if first["action"] != "created" {
		t.Fatalf("first action = %v", first["action"])
	}
	if first["changed_at"] != created["created_at"] {
		t.Fatalf("created changed_at = %v, want %v", first["changed_at"], created["created_at"])
	}
	if first["before"] != nil {
		t.Fatalf("created before = %v, want null", first["before"])
	}
	after := asMap(t, first["after"])
	if after["description"] != "First" {
		t.Fatalf("created after description = %v", after["description"])
	}
	assertStringSlice(t, after["labels"], []string{"alpha", "zeta"})
	assertStringSlice(t, first["changed_fields"], []string{"description", "labels"})

	second := asMap(t, items[1])
	if second["action"] != "updated" {
		t.Fatalf("second action = %v", second["action"])
	}
	if second["changed_at"] != updated["updated_at"] {
		t.Fatalf("updated changed_at = %v, want %v", second["changed_at"], updated["updated_at"])
	}
	before := asMap(t, second["before"])
	if before["description"] != "First" {
		t.Fatalf("before description = %v", before["description"])
	}
	assertStringSlice(t, before["labels"], []string{"alpha", "zeta"})
	after = asMap(t, second["after"])
	if after["description"] != "Second" {
		t.Fatalf("after description = %v", after["description"])
	}
	assertStringSlice(t, after["labels"], []string{"beta"})
	assertStringSlice(t, second["changed_fields"], []string{"description", "labels"})

	firstID := first["event_id"].(float64)
	secondID := second["event_id"].(float64)
	if secondID <= firstID {
		t.Fatalf("event_id not increasing: %v -> %v", firstID, secondID)
	}
}

func TestDefinitionHistoryChangedFieldsOnlyActualChanges(t *testing.T) {
	h := newHarness(t)
	createFlagFull(t, h, `{"key":"f","description":"Same","labels":["a","b"]}`)

	h.setTime("2026-02-01T00:00:00Z")
	identical := h.must(t, http.MethodPut, "/api/v1/flags/f/definition",
		`{"description":"Same","labels":["b","a","a"]}`)

	h.setTime("2026-02-02T00:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition",
		`{"description":"Changed","labels":["a","b"]}`)

	h.setTime("2026-02-03T00:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition",
		`{"description":"Changed","labels":["a","c"]}`)

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags/f/definition-history", ""))
	items := body["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (identical replace still records)", len(items))
	}
	identicalEntry := asMap(t, items[1])
	if identicalEntry["action"] != "updated" {
		t.Fatalf("identical action = %v, want updated", identicalEntry["action"])
	}
	if values, _ := identicalEntry["changed_fields"].([]any); len(values) != 0 {
		t.Fatalf("identical changed_fields = %v, want []", identicalEntry["changed_fields"])
	}
	if identicalEntry["changed_at"] != identical["updated_at"] {
		t.Fatalf("identical changed_at must equal updated_at")
	}
	assertStringSlice(t, asMap(t, items[2])["changed_fields"], []string{"description"})
	assertStringSlice(t, asMap(t, items[3])["changed_fields"], []string{"labels"})
}

func TestDefinitionHistoryFailedWriteAppendsNothing(t *testing.T) {
	h := newHarness(t)
	createFlagFull(t, h, `{"key":"f"}`)

	// Invalid body: validation fails before the store write.
	assertErrorCode(t, h.do(t, http.MethodPut, "/api/v1/flags/f/definition",
		`{"description":""}`), http.StatusBadRequest, "InvalidRequest")
	// Unknown flag: no record must land in the unknown flag's history.
	assertErrorCode(t, h.do(t, http.MethodPut, "/api/v1/flags/missing/definition",
		`{"description":"d","labels":[]}`), http.StatusNotFound, "FlagNotFound")

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags/f/definition-history", ""))
	if items := body["items"].([]any); len(items) != 1 {
		t.Fatalf("items = %d, want only created", len(items))
	}
}

func TestDefinitionHistoryQueryIsPureRead(t *testing.T) {
	h := newHarness(t)
	createFlagFull(t, h, `{"key":"f"}`)
	path := "/api/v1/flags/f/definition-history"
	first := h.do(t, http.MethodGet, path, "")
	second := h.do(t, http.MethodGet, path, "")
	if first.Body.String() != second.Body.String() {
		t.Fatalf("repeated reads differ:\n%s\n%s", first.Body.String(), second.Body.String())
	}
}

func TestDefinitionHistoryFromToWindow(t *testing.T) {
	h := newHarness(t)
	h.setTime("2026-01-01T00:00:00Z")
	createFlagFull(t, h, `{"key":"f","description":"d0"}`)
	h.setTime("2026-01-02T00:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition", `{"description":"d1","labels":[]}`)
	h.setTime("2026-01-03T00:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition", `{"description":"d2","labels":[]}`)

	// from inclusive: matches the creation.
	body := decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/flags/f/definition-history?from=2026-01-01T00:00:00Z", ""))
	if items := body["items"].([]any); len(items) != 3 {
		t.Fatalf("from-inclusive: got %d items, want 3", len(items))
	}

	// to exclusive: excludes the update exactly at to.
	body = decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/flags/f/definition-history?to=2026-01-02T00:00:00Z", ""))
	if items := body["items"].([]any); len(items) != 1 {
		t.Fatalf("to-exclusive: got %d items, want 1", len(items))
	}

	// Half-open window [day2, day3) keeps only the middle update.
	body = decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/flags/f/definition-history?from=2026-01-02T00:00:00Z&to=2026-01-03T00:00:00Z", ""))
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("window: got %d items, want 1", len(items))
	}
	if asMap(t, items[0])["changed_at"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("window changed_at = %v", items[0])
	}

	// Offset timestamps compare as absolute instants.
	body = decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/flags/f/definition-history?from=2026-01-02T08:00:00%2B08:00&to=2026-01-03T08:00:00%2B08:00", ""))
	if items := body["items"].([]any); len(items) != 1 {
		t.Fatalf("offset window: got %d items, want 1", len(items))
	}

	// No match still returns 200 with an empty array.
	rec := h.do(t, http.MethodGet,
		"/api/v1/flags/f/definition-history?from=2027-01-01T00:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	empty := decodeBody(t, rec)
	if values, ok := empty["items"].([]any); !ok || len(values) != 0 {
		t.Fatalf("items = %#v, want []", empty["items"])
	}
}

func TestDefinitionHistoryValidationOrder(t *testing.T) {
	h := newHarness(t)
	createFlagFull(t, h, `{"key":"good"}`)

	// flagKey is checked first.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/BAD/definition-history?from=nope&to=also-nope", ""),
		http.StatusBadRequest, "InvalidRequest")
	// from before to.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?from=nope&to=nope2", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// to after from.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?from=2026-01-01T00:00:00Z&to=nope", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// from >= to.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?from=2026-01-02T00:00:00Z&to=2026-01-02T00:00:00Z", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?from=2026-01-03T00:00:00Z&to=2026-01-02T00:00:00Z", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// Explicit empty values are invalid.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?from=", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/good/definition-history?to=", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// Existence comes last.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/missing/definition-history", ""),
		http.StatusNotFound, "FlagNotFound")
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/missing/definition-history?from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z", ""),
		http.StatusNotFound, "FlagNotFound")
}

func TestDefinitionHistoryErrorShapeHidesInternals(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/v1/flags/missing/definition-history", "")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "FlagNotFound" || body.Error.Message == "" {
		t.Fatalf("error = %#v", body.Error)
	}
	for _, forbidden := range []string{"SELECT", "sqlite", ".go:", "goroutine", "/internal/"} {
		if containsInsensitive(rec.Body.String(), forbidden) {
			t.Fatalf("error body leaks %q: %s", forbidden, rec.Body.String())
		}
	}
}

func containsInsensitive(s, sub string) bool {
	return len(s) >= len(sub) && (indexInsensitive(s, sub) >= 0)
}

func indexInsensitive(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestDefinitionHistorySameInstantOrdersByEventID(t *testing.T) {
	h := newHarness(t)
	// Both writes happen at the same clock instant; event_id must decide order.
	createFlagFull(t, h, `{"key":"f","description":"d0"}`)
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition", `{"description":"d1","labels":[]}`)

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags/f/definition-history", ""))
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := asMap(t, items[0])
	second := asMap(t, items[1])
	if first["changed_at"] != second["changed_at"] {
		t.Fatalf("setup: instants differ %v vs %v", first["changed_at"], second["changed_at"])
	}
	if first["event_id"].(float64) >= second["event_id"].(float64) {
		t.Fatalf("event_id order wrong: %v then %v", first["event_id"], second["event_id"])
	}
	if first["action"] != "created" || second["action"] != "updated" {
		t.Fatalf("actions = %v, %v", first["action"], second["action"])
	}
}
