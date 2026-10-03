package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const conditionalPath = "/api/v1/environments/prod/flags/checkout/config/conditional"

func TestConditionalConfigValidation(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	// Identifiers are validated before the body.
	assertErrorCode(t, h.do(t, http.MethodPost, "/api/v1/environments/PROD/flags/checkout/config/conditional", `{"expected_version":null,"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, "/api/v1/environments/prod/flags/BAD_KEY/config/conditional", `{"expected_version":null,"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")

	// The body must be a single object carrying every required field.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, ""), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `not json`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":"yes","percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":"10"}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"extra":1}`), http.StatusBadRequest, "InvalidRequest")

	// expected_version accepts only a string or null.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":1,"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":true,"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":["cfg-x"],"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":{"v":"cfg-x"},"enabled":true,"percentage":10}`), http.StatusBadRequest, "InvalidRequest")

	// Percentage stays within 0..100.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":-1}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":101}`), http.StatusBadRequest, "InvalidRequest")

	// Window timestamps and ordering follow the existing write entry.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"soon"}}`), http.StatusBadRequest, "InvalidTimestamp")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"window":{"ends_at":"2026-13-01T00:00:00Z"}}`), http.StatusBadRequest, "InvalidTimestamp")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-02T00:00:00Z","ends_at":"2026-01-01T00:00:00Z"}}`), http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T00:00:00Z","ends_at":"2026-01-01T00:00:00Z"}}`), http.StatusBadRequest, "InvalidRequest")

	// Failing requests appended nothing.
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	if versions := body["versions"].([]any); len(versions) != 0 {
		t.Fatalf("failing requests appended versions: %#v", versions)
	}
}

func TestConditionalConfigUnknownResources(t *testing.T) {
	h := newHarness(t)
	payload := `{"expected_version":null,"enabled":true,"percentage":10}`
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, payload), http.StatusNotFound, "EnvironmentNotFound")
	h.createEnv(t, "prod")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, payload), http.StatusNotFound, "FlagNotFound")
}

func TestConditionalConfigLifecycle(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	// With no effective configuration, only a null expectation matches.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":"cfg-00000000000000000001-0001","enabled":true,"percentage":10}`), http.StatusConflict, "VersionConflict")

	h.setTime("2026-01-01T02:00:00Z")
	first := h.must(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T03:00:00Z"}}`)
	for _, field := range []string{"flag_key", "environment", "version", "enabled", "percentage", "window", "changed_at", "tombstone"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("response missing %s: %#v", field, first)
		}
	}
	if first["flag_key"] != "checkout" || first["environment"] != "prod" {
		t.Fatalf("unexpected identity: %#v", first)
	}
	if first["enabled"] != true || number(first["percentage"]) != 10 {
		t.Fatalf("unexpected payload: %#v", first)
	}
	if first["tombstone"] != false {
		t.Fatalf("tombstone = %v, want false", first["tombstone"])
	}
	if first["changed_at"] != "2026-01-01T02:00:00Z" {
		t.Fatalf("changed_at = %v", first["changed_at"])
	}
	window := asMap(t, first["window"])
	if window["starts_at"] != "2026-01-01T03:00:00Z" || window["ends_at"] != nil {
		t.Fatalf("window = %#v, want starts_at set and ends_at null", window)
	}

	// The effective version moved on: a stale null expectation conflicts and
	// appends nothing.
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":20}`), http.StatusConflict, "VersionConflict")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, `{"expected_version":"cfg-00000000000000000009-0001","enabled":true,"percentage":20}`), http.StatusConflict, "VersionConflict")

	// The current version unlocks the next append; versions always differ.
	h.setTime("2026-01-01T04:00:00Z")
	second := h.must(t, http.MethodPost, conditionalPath, fmt.Sprintf(`{"expected_version":%q,"enabled":false,"percentage":80}`, first["version"]))
	if second["version"] == first["version"] {
		t.Fatalf("repeated submission reused version %v", second["version"])
	}
	if second["enabled"] != false || number(second["percentage"]) != 80 {
		t.Fatalf("unexpected second payload: %#v", second)
	}
	secondWindow := asMap(t, second["window"])
	if secondWindow["starts_at"] != nil || secondWindow["ends_at"] != nil {
		t.Fatalf("omitted window must stay null: %#v", secondWindow)
	}

	// A tombstone clears the effective version: the tombstone's own version
	// does not count, only null matches again.
	tombstone := h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, fmt.Sprintf(`{"expected_version":%q,"enabled":true,"percentage":5}`, tombstone["version"])), http.StatusConflict, "VersionConflict")
	assertErrorCode(t, h.do(t, http.MethodPost, conditionalPath, fmt.Sprintf(`{"expected_version":%q,"enabled":true,"percentage":5}`, second["version"])), http.StatusConflict, "VersionConflict")
	third := h.must(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":5}`)
	if third["version"] == first["version"] || third["version"] == second["version"] {
		t.Fatalf("version not regenerated: %#v", third["version"])
	}

	// Exactly the successful appends (plus the tombstone) were recorded.
	history := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	versions := history["versions"].([]any)
	if len(versions) != 4 {
		t.Fatalf("history len = %d, want 4 (3 appends + 1 tombstone): %#v", len(versions), versions)
	}
	for i, want := range []any{first["version"], second["version"], tombstone["version"], third["version"]} {
		if got := asMap(t, versions[i])["version"]; got != want {
			t.Fatalf("history[%d] version = %v, want %v", i, got, want)
		}
	}
}

func TestConditionalConfigConcurrentSameVersion(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	// First wave: everyone expects "no effective configuration".
	submitConcurrently(t, h, `{"expected_version":null,"enabled":true,"percentage":10}`, 8, 1)

	history := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	versions := history["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("history len = %d, want exactly 1 appended version", len(versions))
	}
	winner := asMap(t, versions[0])["version"].(string)

	// Second wave: everyone expects the winning version; again at most one
	// append may succeed.
	submitConcurrently(t, h, fmt.Sprintf(`{"expected_version":%q,"enabled":true,"percentage":20}`, winner), 8, 1)

	history = decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", ""))
	if versions := history["versions"].([]any); len(versions) != 2 {
		t.Fatalf("history len = %d, want exactly 2 appended versions", len(versions))
	}
}

// submitConcurrently fires contenders identical conditional writes and
// asserts exactly wantCreated of them succeed while the rest conflict.
func submitConcurrently(t *testing.T, h *apiHarness, payload string, contenders, wantCreated int) {
	t.Helper()
	statuses := make([]int, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, conditionalPath, strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.router.ServeHTTP(rec, req)
			statuses[slot] = rec.Code
		}(i)
	}
	wg.Wait()

	created := 0
	for _, code := range statuses {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("unexpected status %d, want 201 or 409", code)
		}
	}
	if created != wantCreated {
		t.Fatalf("created = %d, want exactly %d (statuses %v)", created, wantCreated, statuses)
	}
}

func TestConditionalConfigJoinsExistingSurfaces(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createEnv(t, "staging")
	h.createFlag(t, "checkout")

	h.setTime("2026-01-01T02:00:00Z")
	created := h.must(t, http.MethodPost, conditionalPath, `{"expected_version":null,"enabled":true,"percentage":100}`)

	// Real-time evaluation sees the new version immediately.
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate?marker=alpha", ""))
	checkout := findFlag(t, asFlags(t, body), "checkout")
	if checkout["status"] != "on" || checkout["version"] != created["version"] {
		t.Fatalf("evaluate = %#v, want on at %v", checkout, created["version"])
	}

	// The environment change audit classifies the append as created.
	changes := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?flagKey=checkout", ""))
	items := changes["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("changes len = %d, want 1", len(items))
	}
	item := asMap(t, items[0])
	if item["action"] != "created" || item["version"] != created["version"] {
		t.Fatalf("change item = %#v", item)
	}

	// Historical evaluation and explain restore the version at later instants.
	h.setTime("2026-01-01T05:00:00Z")
	at := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/evaluate-at?at=2026-01-01T03:00:00Z&marker=alpha", ""))
	checkout = findFlag(t, asFlags(t, at), "checkout")
	if checkout["status"] != "on" || checkout["version"] != created["version"] {
		t.Fatalf("evaluate-at = %#v", checkout)
	}
	explain := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/explain?marker=alpha", ""))
	if explain["status"] != "on" || explain["reason"] != "enabled" {
		t.Fatalf("explain = %#v", explain)
	}

	// Cross-environment comparison sees only the source side configured.
	compare := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/compare/checkout?source=prod&target=staging&at=2026-01-01T06:00:00Z", ""))
	if compare["comparison"] != "only_source" {
		t.Fatalf("compare = %#v, want only_source", compare)
	}
}
