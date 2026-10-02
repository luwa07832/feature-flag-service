package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestCreateFlagLegacyRequestGetsEmptyDefinition(t *testing.T) {
	h := newHarness(t)
	body := h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	if body["description"] != "" {
		t.Errorf("description = %v, want empty", body["description"])
	}
	labels, ok := body["labels"].([]any)
	if !ok || len(labels) != 0 {
		t.Errorf("labels = %#v, want empty array", body["labels"])
	}
	if body["created_at"] == nil || body["created_at"] != body["updated_at"] {
		t.Errorf("created_at=%v updated_at=%v, want equal", body["created_at"], body["updated_at"])
	}
}

func TestCreateFlagWithDefinitionNormalizes(t *testing.T) {
	h := newHarness(t)
	body := h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"  Payments flow  ","labels":["beta","alpha","beta","ui"]}`)
	if body["description"] != "Payments flow" {
		t.Errorf("description = %v, want trimmed", body["description"])
	}
	labels := asStringSlice(t, body["labels"])
	want := []string{"alpha", "beta", "ui"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("labels = %v, want deduped+sorted %v", labels, want)
	}
	if body["created_at"] != body["updated_at"] {
		t.Errorf("created_at=%v updated_at=%v, want equal", body["created_at"], body["updated_at"])
	}
}

func TestCreateFlagDefinitionValidation(t *testing.T) {
	h := newHarness(t)
	cases := []string{
		`{"description":"x"}`,                                          // missing key
		`{"key":"a","description":"   "}`,                              // empty after trim
		`{"key":"a","description":"` + strings.Repeat("字", 513) + `"}`, // too long
		`{"key":"a","labels":["BAD"]}`,                                 // illegal label
		`{"key":"a","labels":[""]}`,                                    // empty label
		`{"key":"a","labels":["` + strings.Repeat("x", 33) + `"]}`,     // label too long
		`{"key":"a","labels":` + repeatedLabelsJSON(21) + `}`,          // over 20 after dedupe
		`{"key":"a","description":1}`,                                  // wrong type
		`{"key":"a","labels":"beta"}`,                                  // wrong type
		`{"key":"a","unknown":1}`,                                      // unknown field
	}
	for _, payload := range cases {
		rec := h.do(t, http.MethodPost, "/api/v1/flags", payload)
		assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	}
	rec := h.do(t, http.MethodPost, "/api/v1/flags", `[{"key":"a"}]`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	rec = h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"a"} {"key":"b"}`)
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")

	// Boundary values are accepted.
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"ok-boundary","description":"`+strings.Repeat("字", 512)+`","labels":`+repeatedLabelsJSON(20)+`}`)
	// Failed requests wrote nothing: only the boundary flag exists.
	list := h.must(t, http.MethodGet, "/api/v1/flags", "")
	flags := asFlags(t, list)
	if len(flags) != 1 || flags[0] != "ok-boundary" {
		t.Errorf("flags = %v, want only ok-boundary", flags)
	}
}

func TestGetFlagDetail(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout","description":"Payments","labels":["beta"]}`)
	body := h.must(t, http.MethodGet, "/api/v1/flags/checkout", "")
	if body["key"] != "checkout" || body["description"] != "Payments" {
		t.Errorf("unexpected detail: %#v", body)
	}
	labels := asStringSlice(t, body["labels"])
	if len(labels) != 1 || labels[0] != "beta" {
		t.Errorf("labels = %v, want [beta]", labels)
	}
	rec := h.do(t, http.MethodGet, "/api/v1/flags/missing", "")
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")
	rec = h.do(t, http.MethodGet, "/api/v1/flags/BAD%20KEY", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
}

func TestListFlagsStillReturnsKeysOnly(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"b-flag","description":"B","labels":["x"]}`)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"a-flag"}`)
	body := h.must(t, http.MethodGet, "/api/v1/flags", "")
	flags := asFlags(t, body)
	if len(flags) != 2 || flags[0] != "a-flag" || flags[1] != "b-flag" {
		t.Errorf("flags = %v, want [a-flag b-flag]", flags)
	}
}

func TestPutFlagDefinitionReplacesAndBumpsUpdatedAt(t *testing.T) {
	h := newHarness(t)
	created := h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout","description":"old","labels":["old"]}`)

	h.setTime("2026-01-02T00:00:00Z")
	updated := h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":" new description ","labels":["zeta","alpha","zeta"]}`)
	if updated["description"] != "new description" {
		t.Errorf("description = %v, want replaced+trimmed", updated["description"])
	}
	labels := asStringSlice(t, updated["labels"])
	if strings.Join(labels, ",") != "alpha,zeta" {
		t.Errorf("labels = %v, want [alpha zeta]", labels)
	}
	if updated["created_at"] != created["created_at"] {
		t.Errorf("created_at changed: %v -> %v", created["created_at"], updated["created_at"])
	}
	if updated["updated_at"] == created["updated_at"] {
		t.Errorf("updated_at not bumped: %v", updated["updated_at"])
	}

	// Repeating the same replacement keeps the same content.
	h.setTime("2026-01-03T00:00:00Z")
	again := h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"new description","labels":["alpha","zeta"]}`)
	if again["description"] != updated["description"] || again["created_at"] != updated["created_at"] {
		t.Errorf("repeat replace diverged: %#v vs %#v", again, updated)
	}
	got := h.must(t, http.MethodGet, "/api/v1/flags/checkout", "")
	if got["description"] != "new description" {
		t.Errorf("stored description = %v", got["description"])
	}
}

func TestPutFlagDefinitionValidation(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	cases := []string{
		`{"labels":[]}`,                             // missing description
		`{"description":"x"}`,                       // missing labels
		`{}`,                                        // missing both
		`{"description":"  ","labels":[]}`,          // empty after trim
		`{"description":"x","labels":["BAD"]}`,      // illegal label
		`{"description":"x","labels":[],"extra":1}`, // unknown field
		`{"description":1,"labels":[]}`,             // wrong type
	}
	for _, payload := range cases {
		rec := h.do(t, http.MethodPut, "/api/v1/flags/checkout/definition", payload)
		assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	}
	rec := h.do(t, http.MethodPut, "/api/v1/flags/missing/definition", `{"description":"x","labels":[]}`)
	assertErrorCode(t, rec, http.StatusNotFound, "FlagNotFound")

	// Failed requests left the definition untouched.
	got := h.must(t, http.MethodGet, "/api/v1/flags/checkout", "")
	if got["description"] != "" {
		t.Errorf("description = %v, want still empty", got["description"])
	}
}

func TestPutFlagDefinitionDoesNotAppendConfigHistory(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)
	before := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")

	h.must(t, http.MethodPut, "/api/v1/flags/checkout/definition", `{"description":"d","labels":["a"]}`)

	after := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	if len(asVersions(t, before)) != len(asVersions(t, after)) {
		t.Errorf("history length changed: %d -> %d", len(asVersions(t, before)), len(asVersions(t, after)))
	}
}

func TestListFlagDefinitionsSearch(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout","description":"Payments Flow","labels":["core","ui"]}`)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"coupon","description":"Discounts","labels":["core"]}`)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"banner","description":"Homepage banner","labels":["ui"]}`)

	// No condition: everything, ordered by key.
	all := h.must(t, http.MethodGet, "/api/v1/flag-definitions", "")
	keys := definitionKeys(t, all)
	if strings.Join(keys, ",") != "banner,checkout,coupon" {
		t.Errorf("keys = %v, want sorted full set", keys)
	}

	// q matches key or description, case-insensitive.
	byKey := h.must(t, http.MethodGet, "/api/v1/flag-definitions?q=COUP", "")
	if got := definitionKeys(t, byKey); strings.Join(got, ",") != "coupon" {
		t.Errorf("q=COUP keys = %v, want [coupon]", got)
	}
	byDescription := h.must(t, http.MethodGet, "/api/v1/flag-definitions?q=payments", "")
	if got := definitionKeys(t, byDescription); strings.Join(got, ",") != "checkout" {
		t.Errorf("q=payments keys = %v, want [checkout]", got)
	}

	// Blank q counts as absent.
	blank := h.must(t, http.MethodGet, "/api/v1/flag-definitions?q=%20%20", "")
	if got := definitionKeys(t, blank); len(got) != 3 {
		t.Errorf("blank q keys = %v, want all", got)
	}

	// Multiple labels must all be present.
	both := h.must(t, http.MethodGet, "/api/v1/flag-definitions?label=core&label=ui", "")
	if got := definitionKeys(t, both); strings.Join(got, ",") != "checkout" {
		t.Errorf("label=core,ui keys = %v, want [checkout]", got)
	}
	combined := h.must(t, http.MethodGet, "/api/v1/flag-definitions?q=banner&label=ui", "")
	if got := definitionKeys(t, combined); strings.Join(got, ",") != "banner" {
		t.Errorf("q+label keys = %v, want [banner]", got)
	}

	rec := h.do(t, http.MethodGet, "/api/v1/flag-definitions?label=BAD", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
	rec = h.do(t, http.MethodGet, "/api/v1/flag-definitions?label=", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")
}

func repeatedLabelsJSON(count int) string {
	labels := make([]string, count)
	for i := range labels {
		labels[i] = `"l` + strconv.Itoa(i) + `"`
	}
	return "[" + strings.Join(labels, ",") + "]"
}

func asStringSlice(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", v)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("not a string: %#v", item)
		}
		out = append(out, s)
	}
	return out
}

func definitionKeys(t *testing.T, body map[string]any) []string {
	t.Helper()
	items, ok := body["flags"].([]any)
	if !ok {
		t.Fatalf("flags not an array: %#v", body)
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, asMap(t, item)["key"].(string))
	}
	return keys
}

func asVersions(t *testing.T, body map[string]any) []any {
	t.Helper()
	versions, ok := body["versions"].([]any)
	if !ok {
		t.Fatalf("versions not an array: %#v", body)
	}
	return versions
}
