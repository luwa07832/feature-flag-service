package api

import (
	"net/http"
	"testing"
)

func TestDefinitionBodyShapeEdgeCases(t *testing.T) {
	h := newHarness(t)
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"checkout"}`)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   string
	}{
		{"null body", http.MethodPost, "/api/v1/flags", `null`, "InvalidRequest"},
		{"two objects", http.MethodPost, "/api/v1/flags", `{"key":"a"}{"key":"b"}`, "InvalidRequest"},
		{"empty body", http.MethodPost, "/api/v1/flags", ``, "InvalidRequest"},
		{"array", http.MethodPost, "/api/v1/flags", `[]`, "InvalidRequest"},
		{"number", http.MethodPost, "/api/v1/flags", `42`, "InvalidRequest"},
		{"put null", http.MethodPut, "/api/v1/flags/checkout/definition", `null`, "InvalidRequest"},
		{"put two", http.MethodPut, "/api/v1/flags/checkout/definition", `{"description":"d","labels":[]}{}`, "InvalidRequest"},
		{"put array", http.MethodPut, "/api/v1/flags/checkout/definition", `[]`, "InvalidRequest"},
		{"put empty", http.MethodPut, "/api/v1/flags/checkout/definition", ``, "InvalidRequest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertErrorCode(t, h.do(t, tc.method, tc.path, tc.body), http.StatusBadRequest, tc.want)
		})
	}

	flags := decodeBody(t, h.do(t, http.MethodGet, "/api/v1/flags", ""))
	if keys := flags["flags"].([]any); len(keys) != 1 || keys[0] != "checkout" {
		t.Fatalf("flags = %v, only checkout should exist", keys)
	}
}
