package api

import (
	"net/http"
	"testing"
)

func definitionHistoryItems(t *testing.T, h *apiHarness, path string) []any {
	t.Helper()
	rec := h.do(t, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s -> %d %s", path, rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["flag_key"].(string); !ok {
		t.Fatalf("flag_key missing: %s", rec.Body.String())
	}
	if len(body) != 2 {
		t.Fatalf("response must contain only flag_key and items: %v", body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %#v", body["items"])
	}
	return items
}

func TestCreateRecordsDefinitionCreatedEvent(t *testing.T) {
	h := newHarness(t)
	h.setTime("2026-04-01T00:00:00Z")
	created := h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"Payments","labels":["b","a","a"]}`)

	items := definitionHistoryItems(t, h, "/api/v1/flags/checkout/definition-history")
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := asMap(t, items[0])
	if item["action"] != "created" {
		t.Fatalf("action = %v", item["action"])
	}
	if item["changed_at"] != created["created_at"] {
		t.Fatalf("changed_at = %v, want %v", item["changed_at"], created["created_at"])
	}
	assertStringSlice(t, item["changed_fields"], []string{"description", "labels"})
	if item["before"] != nil {
		t.Fatalf("before = %v, want null", item["before"])
	}
	after := asMap(t, item["after"])
	if after["description"] != "Payments" {
		t.Fatalf("after description = %v", after["description"])
	}
	assertStringSlice(t, after["labels"], []string{"a", "b"})
	if _, ok := item["event_id"].(float64); !ok {
		t.Fatalf("event_id missing or not a number: %#v", item["event_id"])
	}
}

func TestUpdateRecordsBeforeAfterAndDeltas(t *testing.T) {
	h := newHarness(t)
	h.setTime("2026-04-02T00:00:00Z")
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"Old","labels":["a"]}`)

	h.setTime("2026-04-02T01:00:00Z")
	updated := h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"Old","labels":["b","a"]}`)

	h.setTime("2026-04-02T02:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"New","labels":["a","b"]}`)

	items := definitionHistoryItems(t, h, "/api/v1/flags/checkout/definition-history")
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}

	first := asMap(t, items[1])
	if first["action"] != "updated" {
		t.Fatalf("action = %v", first["action"])
	}
	if first["changed_at"] != updated["updated_at"] {
		t.Fatalf("changed_at = %v, want updated_at %v", first["changed_at"], updated["updated_at"])
	}
	assertStringSlice(t, first["changed_fields"], []string{"labels"})
	before := asMap(t, first["before"])
	after := asMap(t, first["after"])
	if before["description"] != "Old" || after["description"] != "Old" {
		t.Fatalf("snapshots = %#v / %#v", before, after)
	}
	assertStringSlice(t, before["labels"], []string{"a"})
	assertStringSlice(t, after["labels"], []string{"a", "b"})

	second := asMap(t, items[2])
	assertStringSlice(t, second["changed_fields"], []string{"description"})

	ids := []float64{
		asMap(t, items[0])["event_id"].(float64),
		first["event_id"].(float64),
		second["event_id"].(float64),
	}
	if !(ids[0] < ids[1] && ids[1] < ids[2]) {
		t.Fatalf("event ids not strictly increasing: %v", ids)
	}
}

func TestIdenticalReplaceProducesEmptyChangedFields(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"f","description":"Same","labels":["a"]}`)
	h.setTime("2026-04-03T01:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition",
		`{"description":"Same","labels":["a"]}`)

	items := definitionHistoryItems(t, h, "/api/v1/flags/f/definition-history")
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	updated := asMap(t, items[1])
	if updated["action"] != "updated" {
		t.Fatalf("action = %v", updated["action"])
	}
	assertStringSlice(t, updated["changed_fields"], []string{})
}

func TestDefinitionHistoryWindowFromInclusiveToExclusive(t *testing.T) {
	h := newHarness(t)
	h.setTime("2026-04-04T00:00:00Z")
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"f","description":"v1"}`)
	h.setTime("2026-04-04T01:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition", `{"description":"v2","labels":[]}`)
	h.setTime("2026-04-04T02:00:00Z")
	h.must(t, http.MethodPut, "/api/v1/flags/f/definition", `{"description":"v3","labels":[]}`)

	items := definitionHistoryItems(t, h,
		"/api/v1/flags/f/definition-history?from=2026-04-04T01:00:00Z&to=2026-04-04T02:00:00Z")
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 in half-open window", len(items))
	}
	if asMap(t, asMap(t, items[0])["after"])["description"] != "v2" {
		t.Fatalf("item = %#v", items[0])
	}

	fromOnly := definitionHistoryItems(t, h,
		"/api/v1/flags/f/definition-history?from=2026-04-04T02:00:00Z")
	if len(fromOnly) != 1 || asMap(t, asMap(t, fromOnly[0])["after"])["description"] != "v3" {
		t.Fatalf("from-only = %#v", fromOnly)
	}

	toOnly := definitionHistoryItems(t, h,
		"/api/v1/flags/f/definition-history?to=2026-04-04T01:00:00Z")
	if len(toOnly) != 1 || asMap(t, asMap(t, toOnly[0])["after"])["description"] != "v1" {
		t.Fatalf("to-only = %#v", toOnly)
	}
}

func TestDefinitionHistoryUnknownFlagAndEmptyResult(t *testing.T) {
	h := newHarness(t)
	assertErrorCode(t, h.do(t, http.MethodGet, "/api/v1/flags/missing/definition-history", ""),
		http.StatusNotFound, "FlagNotFound")

	h.createFlag(t, "fresh")
	items := definitionHistoryItems(t, h, "/api/v1/flags/fresh/definition-history")
	if len(items) != 1 {
		t.Fatalf("backfilled-or-created flag must expose its created event, got %d", len(items))
	}
}

func TestDefinitionHistoryValidation(t *testing.T) {
	h := newHarness(t)
	h.createFlag(t, "checkout")
	cases := map[string]struct {
		path   string
		status int
		code   string
	}{
		"bad flagKey":         {"/api/v1/flags/BAD%20KEY/definition-history", http.StatusBadRequest, "InvalidRequest"},
		"empty flagKey":       {"/api/v1/flags//definition-history", http.StatusBadRequest, "InvalidRequest"},
		"empty from":          {"/api/v1/flags/checkout/definition-history?from=", http.StatusBadRequest, "InvalidTimestamp"},
		"bad from":            {"/api/v1/flags/checkout/definition-history?from=nope", http.StatusBadRequest, "InvalidTimestamp"},
		"empty to":            {"/api/v1/flags/checkout/definition-history?to=", http.StatusBadRequest, "InvalidTimestamp"},
		"bad to":              {"/api/v1/flags/checkout/definition-history?to=nope", http.StatusBadRequest, "InvalidTimestamp"},
		"from equals to":      {"/api/v1/flags/checkout/definition-history?from=2026-04-01T00:00:00Z&to=2026-04-01T00:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		"from after to":       {"/api/v1/flags/checkout/definition-history?from=2026-04-02T00:00:00Z&to=2026-04-01T00:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		"unknown flag":        {"/api/v1/flags/missing/definition-history", http.StatusNotFound, "FlagNotFound"},
		"unknown with window": {"/api/v1/flags/missing/definition-history?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z", http.StatusNotFound, "FlagNotFound"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assertErrorCode(t, h.do(t, http.MethodGet, tc.path, ""), tc.status, tc.code)
		})
	}
}

func TestDefinitionHistoryValidationOrder(t *testing.T) {
	h := newHarness(t)
	// Invalid flagKey wins over invalid timestamps and missing flag.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/BAD/definition-history?from=nope&to=nope", ""),
		http.StatusBadRequest, "InvalidRequest")
	// Invalid from wins over invalid to.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/checkout/definition-history?from=nope&to=nope", ""),
		http.StatusBadRequest, "InvalidTimestamp")
	// Well-formed but inverted window wins over the unknown flag.
	assertErrorCode(t, h.do(t, http.MethodGet,
		"/api/v1/flags/missing/definition-history?from=2026-04-02T00:00:00Z&to=2026-04-01T00:00:00Z", ""),
		http.StatusBadRequest, "InvalidTimestamp")
}

func TestDefinitionHistoryIsReadOnlyAndRepeatable(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"f","description":"v1"}`)
	path := "/api/v1/flags/f/definition-history"
	first := h.do(t, http.MethodGet, path, "")
	second := h.do(t, http.MethodGet, path, "")
	if first.Body.String() != second.Body.String() {
		t.Fatalf("repeated queries differ:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d", first.Code)
	}
}
