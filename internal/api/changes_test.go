package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func asItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %#v", body["items"])
	}
	return items
}

func assertChangeFields(t *testing.T, item map[string]any, want []string) {
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
			t.Fatalf("changed_fields = %v, want %v", raw, want)
		}
	}
}

func TestChangesListsAllFlagsOrderedByChangedAt(t *testing.T) {
	h := seededHarness(t)
	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", ""))
	if body["environment"] != "prod" {
		t.Fatalf("environment = %v, want prod", body["environment"])
	}
	items := asItems(t, body)
	if len(items) != 4 {
		t.Fatalf("items len = %d, want 4", len(items))
	}
	wantActions := []string{"created", "updated", "created", "deleted"}
	wantFlags := []string{"checkout", "checkout", "coupon", "checkout"}
	for i, item := range items {
		row := asMap(t, item)
		if row["action"] != wantActions[i] {
			t.Fatalf("item %d action = %v, want %s", i, row["action"], wantActions[i])
		}
		if row["flag_key"] != wantFlags[i] {
			t.Fatalf("item %d flag_key = %v, want %s", i, row["flag_key"], wantFlags[i])
		}
	}
}

func TestChangesCreatedUpdatedDeletedShapes(t *testing.T) {
	h := seededHarness(t)
	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?flagKey=checkout", "")))
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}

	created := asMap(t, items[0])
	if created["before"] != nil {
		t.Fatalf("created before = %v, want nil", created["before"])
	}
	after := asMap(t, created["after"])
	if after["enabled"] != true || number(after["percentage"]) != 10 {
		t.Fatalf("created after = %v", after)
	}
	assertChangeFields(t, created, []string{"enabled", "percentage"})

	updated := asMap(t, items[1])
	before := asMap(t, updated["before"])
	after = asMap(t, updated["after"])
	if number(before["percentage"]) != 10 || number(after["percentage"]) != 50 {
		t.Fatalf("updated before/after = %v / %v", before, after)
	}
	assertChangeFields(t, updated, []string{"percentage"})

	deleted := asMap(t, items[2])
	if deleted["after"] != nil {
		t.Fatalf("deleted after = %v, want nil", deleted["after"])
	}
	before = asMap(t, deleted["before"])
	if number(before["percentage"]) != 50 {
		t.Fatalf("deleted before = %v", before)
	}
	assertChangeFields(t, deleted, []string{"enabled", "percentage"})
}

func TestChangesWindowFieldsReportedAndSnapshotted(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "windowed")
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "windowed",
		`{"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)
	h.setTime("2026-01-01T04:00:00Z")
	h.putConfig(t, "prod", "windowed",
		`{"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T03:30:00Z","ends_at":null}}`)

	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?flagKey=windowed", "")))
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	created := asMap(t, items[0])
	assertChangeFields(t, created, []string{"enabled", "percentage", "window.starts_at", "window.ends_at"})
	updated := asMap(t, items[1])
	assertChangeFields(t, updated, []string{"window.starts_at", "window.ends_at"})
	afterWindow := asMap(t, asMap(t, updated["after"])["window"])
	if afterWindow["starts_at"] != "2026-01-01T03:30:00Z" || afterWindow["ends_at"] != nil {
		t.Fatalf("updated after window = %v", afterWindow)
	}
	beforeWindow := asMap(t, asMap(t, updated["before"])["window"])
	if beforeWindow["ends_at"] != "2026-01-01T06:00:00Z" {
		t.Fatalf("updated before window = %v", beforeWindow)
	}
}

func TestChangesRecreateAfterTombstoneIsCreated(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "cyclic")
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "cyclic", `{"enabled":true,"percentage":10}`)
	h.setTime("2026-01-01T03:00:00Z")
	h.must(t, http.MethodDelete, "/api/v1/environments/prod/flags/cyclic/config", "")
	h.setTime("2026-01-01T04:00:00Z")
	h.putConfig(t, "prod", "cyclic", `{"enabled":false,"percentage":20}`)

	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?flagKey=cyclic", "")))
	wantActions := []string{"created", "deleted", "created"}
	if len(items) != len(wantActions) {
		t.Fatalf("items len = %d, want %d", len(items), len(wantActions))
	}
	for i, item := range items {
		row := asMap(t, item)
		if row["action"] != wantActions[i] {
			t.Fatalf("item %d action = %v, want %s", i, row["action"], wantActions[i])
		}
	}
	recreated := asMap(t, items[2])
	if recreated["before"] != nil {
		t.Fatalf("recreated before = %v, want nil", recreated["before"])
	}
	if number(asMap(t, recreated["after"])["percentage"]) != 20 {
		t.Fatalf("recreated after = %v", recreated["after"])
	}
}

func TestChangesFromFilterIsInclusive(t *testing.T) {
	h := seededHarness(t)
	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=2026-01-01T03:00:00Z", "")))
	want := [][2]string{
		{"checkout", "updated"},
		{"coupon", "created"},
		{"checkout", "deleted"},
	}
	if len(items) != len(want) {
		t.Fatalf("items len = %d, want %d", len(items), len(want))
	}
	for i, item := range items {
		row := asMap(t, item)
		if row["flag_key"] != want[i][0] || row["action"] != want[i][1] {
			t.Fatalf("item %d = %v/%v, want %s/%s", i, row["flag_key"], row["action"], want[i][0], want[i][1])
		}
	}
}

func TestChangesToFilterIsExclusive(t *testing.T) {
	h := seededHarness(t)
	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?to=2026-01-01T06:00:00Z", "")))
	want := [][2]string{
		{"checkout", "created"},
		{"checkout", "updated"},
	}
	if len(items) != len(want) {
		t.Fatalf("items len = %d, want %d", len(items), len(want))
	}
	for i, item := range items {
		row := asMap(t, item)
		if row["flag_key"] != want[i][0] || row["action"] != want[i][1] {
			t.Fatalf("item %d = %v/%v, want %s/%s", i, row["flag_key"], row["action"], want[i][0], want[i][1])
		}
	}
}

func TestChangesFromToWindow(t *testing.T) {
	h := seededHarness(t)
	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=2026-01-01T03:00:00Z&to=2026-01-01T08:00:00Z", "")))
	want := [][2]string{
		{"checkout", "updated"},
		{"coupon", "created"},
	}
	if len(items) != len(want) {
		t.Fatalf("items len = %d, want %d", len(items), len(want))
	}
	for i, item := range items {
		row := asMap(t, item)
		if row["flag_key"] != want[i][0] || row["action"] != want[i][1] {
			t.Fatalf("item %d = %v/%v, want %s/%s", i, row["flag_key"], row["action"], want[i][0], want[i][1])
		}
	}
}

func TestChangesWindowedItemUsesFullChainForAction(t *testing.T) {
	// The created change at 02:00 is outside the window, but the 03:00
	// update must still classify as updated rather than created.
	h := seededHarness(t)
	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?flagKey=checkout&from=2026-01-01T03:00:00Z&to=2026-01-01T04:00:00Z", "")))
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	row := asMap(t, items[0])
	if row["action"] != "updated" {
		t.Fatalf("action = %v, want updated", row["action"])
	}
	before := asMap(t, row["before"])
	if number(before["percentage"]) != 10 {
		t.Fatalf("before percentage = %v, want 10", before["percentage"])
	}
}

func TestChangesSameInstantUsesWriteOrder(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "alpha")
	h.createFlag(t, "beta")
	h.setTime("2026-01-01T05:00:00Z")
	h.putConfig(t, "prod", "alpha", `{"enabled":true,"percentage":10}`)
	h.putConfig(t, "prod", "beta", `{"enabled":true,"percentage":20}`)

	items := asItems(t, decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=2026-01-01T05:00:00Z", "")))
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if asMap(t, items[0])["flag_key"] != "alpha" || asMap(t, items[1])["flag_key"] != "beta" {
		t.Fatalf("order = %v then %v, want alpha then beta",
			asMap(t, items[0])["flag_key"], asMap(t, items[1])["flag_key"])
	}
}

func TestChangesNoMatchesReturnsEmptyArray(t *testing.T) {
	h := seededHarness(t)
	rec := h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=2030-01-01T00:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"environment":"prod","items":[]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestChangesDeterministicRepeatedReads(t *testing.T) {
	h := seededHarness(t)
	first := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "").Body.String()
	second := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes", "").Body.String()
	if first != second {
		t.Fatalf("changes query not stable:\n%s\n%s", first, second)
	}
}

func TestChangesValidationOrder(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{"bad environment path", "/api/v1/environments/BAD/changes", http.StatusBadRequest, "InvalidRequest"},
		{"bad from", "/api/v1/environments/prod/changes?from=not-a-time", http.StatusBadRequest, "InvalidTimestamp"},
		{"empty from", "/api/v1/environments/prod/changes?from=", http.StatusBadRequest, "InvalidTimestamp"},
		{"bad to", "/api/v1/environments/prod/changes?to=not-a-time", http.StatusBadRequest, "InvalidTimestamp"},
		{"from equal to", "/api/v1/environments/prod/changes?from=2026-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		{"from after to", "/api/v1/environments/prod/changes?from=2026-01-01T02:00:00Z&to=2026-01-01T01:00:00Z", http.StatusBadRequest, "InvalidTimestamp"},
		{"bad flagKey", "/api/v1/environments/prod/changes?flagKey=BAD%20KEY", http.StatusBadRequest, "InvalidRequest"},
		{"empty flagKey", "/api/v1/environments/prod/changes?flagKey=", http.StatusBadRequest, "InvalidRequest"},
		{"unknown environment", "/api/v1/environments/nope/changes", http.StatusNotFound, "EnvironmentNotFound"},
		{"unknown flag", "/api/v1/environments/prod/changes?flagKey=missing", http.StatusNotFound, "FlagNotFound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertErrorCode(t, h.do(t, http.MethodGet, tc.path, ""), tc.status, tc.code)
		})
	}
}

func TestChangesFromCheckedBeforeToAndFlagKey(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	// Invalid from must surface as InvalidTimestamp even though to and
	// flagKey are also invalid in other categories.
	rec := h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=nope&to=nope&flagKey=BAD", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")
}

func TestChangesUnknownFlagNotMaskedByInvalidTimestamps(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	rec := h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z&flagKey=missing", "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
}

func TestChangesKnownFlagWithoutHistoryReturnsEmpty(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "silent")
	rec := h.do(t, http.MethodGet,
		"/api/v1/environments/prod/changes?flagKey=silent", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if len(asItems(t, body)) != 0 {
		t.Fatalf("items = %v, want empty", body["items"])
	}
}

func TestChangesErrorShapeHidesInternals(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	rec := h.do(t, http.MethodGet, "/api/v1/environments/prod/changes?from=bad", "")
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
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 1 || len(asMap(t, raw["error"])) != 2 {
		t.Fatalf("error envelope must contain only code/message: %s", rec.Body.String())
	}
}
