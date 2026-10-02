package api

import (
	"net/http"
	"testing"
)

func TestCreateFlagWithDefinitionDetails(t *testing.T) {
	h := newHarness(t)

	minimal := decodeBody(t, h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`))
	if minimal["key"] != "checkout" {
		t.Fatalf("key = %v", minimal["key"])
	}
	if minimal["description"] != "" {
		t.Fatalf("description = %v, want empty", minimal["description"])
	}
	if labels, ok := minimal["labels"].([]any); !ok || len(labels) != 0 {
		t.Fatalf("labels = %#v, want []", minimal["labels"])
	}
	if minimal["created_at"] != minimal["updated_at"] {
		t.Fatalf("created_at %v != updated_at %v", minimal["created_at"], minimal["updated_at"])
	}
	if minimal["created_at"] == nil {
		t.Fatalf("timestamps missing: %#v", minimal)
	}

	body := decodeBody(t, h.do(t, http.MethodPost, "/api/v1/flags",
		`{"key":"coupon","description":"  Coupon feature ","labels":["zeta","alpha","alpha","beta"]}`))
	if body["description"] != "Coupon feature" {
		t.Fatalf("description = %v, want trimmed", body["description"])
	}
	assertStringSlice(t, body["labels"], []string{"alpha", "beta", "zeta"})
	if body["created_at"] != body["updated_at"] {
		t.Fatalf("created_at %v != updated_at %v", body["created_at"], body["updated_at"])
	}
}

func TestCreateFlagValidation(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"missing key":          `{"description":"x"}`,
		"empty key":            `{"key":""}`,
		"bad key":              `{"key":"BAD KEY"}`,
		"key wrong type":       `{"key":5}`,
		"description wrong":    `{"key":"a","description":7}`,
		"description empty":    `{"key":"a","description":"   "}`,
		"description too long": `{"key":"a","description":"` + longString() + `"}`,
		"labels wrong type":    `{"key":"a","labels":"x"}`,
		"labels element wrong": `{"key":"a","labels":[1]}`,
		"label illegal":        `{"key":"a","labels":["BAD"]}`,
		"label too long":       `{"key":"a","labels":["` + longLabel() + `"]}`,
		"too many labels":      `{"key":"a","labels":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]}`,
		"unknown field":        `{"key":"a","owner":"me"}`,
		"not an object":        `[1,2,3]`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			assertErrorCode(t, h.do(t, http.MethodPost, "/api/v1/flags", payload),
				http.StatusBadRequest, "InvalidRequest")
		})
	}

	// Duplicate labels within the limit are accepted after deduplication.
	body := decodeBody(t, h.do(t, http.MethodPost, "/api/v1/flags",
		`{"key":"dup","description":"d","labels":["a","a","b","b"]}`))
	assertStringSlice(t, body["labels"], []string{"a", "b"})

	// Existing flags remain retrievable and creation still conflicts.
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"existing"}`)
	assertErrorCode(t, h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"existing"}`),
		http.StatusConflict, "AlreadyExists")
}

func TestGetFlagDefinition(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"Payments","labels":["pay","web"]}`)

	body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags/checkout", ""))
	if body["key"] != "checkout" || body["description"] != "Payments" {
		t.Fatalf("detail = %#v", body)
	}
	assertStringSlice(t, body["labels"], []string{"pay", "web"})
	if body["created_at"] == nil || body["updated_at"] == nil {
		t.Fatalf("timestamps missing: %#v", body)
	}

	assertErrorCode(t, h.do(t, http.MethodGet, "/api/v1/flags/missing", ""),
		http.StatusNotFound, "FlagNotFound")
	assertErrorCode(t, h.do(t, http.MethodGet, "/api/v1/flags/BAD", ""),
		http.StatusBadRequest, "InvalidRequest")
}

func TestPutDefinitionReplacesWholeDefinition(t *testing.T) {
	h := newHarness(t)
	created := decodeBody(t, h.do(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"First","labels":["a"]}`))

	h.setTime("2026-02-01T00:00:00Z")
	updated := decodeBody(t, h.do(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"Second","labels":["z","z","m"]}`))
	if updated["description"] != "Second" {
		t.Fatalf("description = %v", updated["description"])
	}
	assertStringSlice(t, updated["labels"], []string{"m", "z"})
	if updated["created_at"] != created["created_at"] {
		t.Fatalf("created_at moved: %v -> %v", created["created_at"], updated["created_at"])
	}
	if updated["updated_at"] == created["updated_at"] {
		t.Fatalf("updated_at did not move: %v", updated["updated_at"])
	}

	// Repeated identical replacement keeps the content identical.
	h.setTime("2026-02-02T00:00:00Z")
	again := decodeBody(t, h.do(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"Second","labels":["m","z"]}`))
	if again["description"] != "Second" {
		t.Fatalf("description = %v", again["description"])
	}
	assertStringSlice(t, again["labels"], []string{"m", "z"})
	if again["created_at"] != created["created_at"] {
		t.Fatalf("created_at moved on repeat: %v", again["created_at"])
	}
}

func TestPutDefinitionValidation(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)
	h.createEnv(t, "prod")
	h.must(t, http.MethodPut, "/api/v1/environments/prod/flags/checkout/config",
		`{"enabled":true,"percentage":10}`)

	cases := map[string]string{
		"missing description":  `{"labels":["a"]}`,
		"missing labels":       `{"description":"d"}`,
		"empty description":    `{"description":" ","labels":["a"]}`,
		"description too long": `{"description":"` + longString() + `","labels":[]}`,
		"illegal label":        `{"description":"d","labels":["BAD"]}`,
		"too many labels":      `{"description":"d","labels":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]}`,
		"with key field":       `{"key":"checkout","description":"d","labels":[]}`,
		"unknown field":        `{"description":"d","labels":[],"x":1}`,
		"not an object":        `"x"`,
		"null description":     `{"description":null,"labels":[]}`,
		"null labels":          `{"description":"d","labels":null}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			assertErrorCode(t, h.do(t, http.MethodPut, "/api/v1/flags/checkout/definition", payload),
				http.StatusBadRequest, "InvalidRequest")
		})
	}

	assertErrorCode(t, h.do(t, http.MethodPut, "/api/v1/flags/missing/definition",
		`{"description":"d","labels":[]}`), http.StatusNotFound, "FlagNotFound")
	assertErrorCode(t, h.do(t, http.MethodPut, "/api/v1/flags/BAD/definition",
		`{"description":"d","labels":[]}`), http.StatusBadRequest, "InvalidRequest")

	// Empty labels and empty-after-trim definition are valid on replacement.
	body := decodeBody(t, h.do(t, http.MethodPut, "/api/v1/flags/checkout/definition",
		`{"description":"d","labels":[]}`))
	assertStringSlice(t, body["labels"], []string{})

	// Definition updates never append configuration history.
	history := decodeBody(t, h.do(t, http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/history", ""))
	versions := history["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("history len = %d, want 1 (definition update must not append)", len(versions))
	}
}

func TestSearchFlagDefinitions(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"checkout","description":"Payments flow","labels":["pay","web"]}`)
	h.must(t, http.MethodPost, "/api/v1/flags",
		`{"key":"coupon","description":"Payment discounts","labels":["pay","mobile"]}`)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"abandoned-cart","description":"Recover carts"}`)

	get := func(query string) []any {
		t.Helper()
		body := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flag-definitions"+query, ""))
		defs, ok := body["definitions"].([]any)
		if !ok {
			t.Fatalf("definitions not an array: %#v", body)
		}
		return defs
	}
	defKeys := func(defs []any) []string {
		keys := make([]string, 0, len(defs))
		for _, def := range defs {
			keys = append(keys, asMap(t, def)["key"].(string))
		}
		return keys
	}

	if got := defKeys(get("")); len(got) != 3 || got[0] != "abandoned-cart" || got[1] != "checkout" || got[2] != "coupon" {
		t.Fatalf("unfiltered = %v", got)
	}
	if got := defKeys(get("?q=%20%20")); len(got) != 3 {
		t.Fatalf("blank q = %v, want all", got)
	}
	if got := defKeys(get("?q=PAY")); len(got) != 2 {
		t.Fatalf("case-insensitive q = %v, want 2 matches", got)
	}
	if got := defKeys(get("?q=carts")); len(got) != 1 || got[0] != "abandoned-cart" {
		t.Fatalf("description q = %v", got)
	}
	if got := defKeys(get("?label=pay")); len(got) != 2 {
		t.Fatalf("one label = %v, want 2", got)
	}
	if got := defKeys(get("?label=pay&label=web")); len(got) != 1 || got[0] != "checkout" {
		t.Fatalf("both labels = %v, want checkout", got)
	}
	if got := defKeys(get("?q=coupon&label=web")); len(got) != 0 {
		t.Fatalf("q + non-matching label = %v, want none", got)
	}
}

func TestSearchFlagDefinitionsRejectsIllegalLabel(t *testing.T) {
	h := newHarness(t)
	assertErrorCode(t, h.do(t, http.MethodGet, "/api/v1/flag-definitions?label=BAD", ""),
		http.StatusBadRequest, "InvalidRequest")
	assertErrorCode(t, h.do(t, http.MethodGet, "/api/v1/flag-definitions?label=", ""),
		http.StatusBadRequest, "InvalidRequest")
}

func TestFailedDefinitionRequestsDoNotWrite(t *testing.T) {
	h := newHarness(t)
	h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"a","description":"   "}`)
	h.do(t, http.MethodPost, "/api/v1/flags", `{"key":"b","labels":["BAD"]}`)
	h.do(t, http.MethodPost, "/api/v1/flags", `not json`)

	flags := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags", ""))
	if keys := flags["flags"].([]any); len(keys) != 0 {
		t.Fatalf("flags = %v, want none after failed creates", keys)
	}
}

func assertStringSlice(t *testing.T, got any, want []string) {
	t.Helper()
	values, ok := got.([]any)
	if !ok {
		t.Fatalf("not a slice: %#v", got)
	}
	if len(values) != len(want) {
		t.Fatalf("len = %d, want %d (%#v)", len(values), len(want), got)
	}
	for i, value := range values {
		if value.(string) != want[i] {
			t.Fatalf("[%d] = %v, want %s", i, value, want[i])
		}
	}
}

func longString() string {
	runes := make([]rune, 513)
	for i := range runes {
		runes[i] = 'x'
	}
	return string(runes)
}

func longLabel() string {
	runes := make([]rune, 33)
	for i := range runes {
		runes[i] = 'a'
	}
	return string(runes)
}
