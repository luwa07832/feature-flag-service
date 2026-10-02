package api

import (
	"net/http"
	"reflect"
	"testing"
)

func compareURL(flag, query string) string {
	return "/api/v1/compare/" + flag + "?" + query
}

func compareQuery(source, target, at, marker string) string {
	query := "source=" + source + "&target=" + target + "&at=" + at
	if marker != "" {
		query += "&marker=" + marker
	}
	return query
}

func compareOK(t *testing.T, h requestDoer, url string) map[string]any {
	t.Helper()
	rec := h.do(t, http.MethodGet, url, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s -> %d %s", url, rec.Code, rec.Body.String())
	}
	return decodeBody(t, rec)
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}

func seedCompareEnvs(t *testing.T) *apiHarness {
	t.Helper()
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createEnv(t, "staging")
	h.createFlag(t, "checkout")
	return h
}

func compareConfig(t *testing.T, body map[string]any, side string) map[string]any {
	t.Helper()
	value := body[side]
	if value == nil {
		return nil
	}
	return asMap(t, value)
}

func assertCompareTopLevel(t *testing.T, body map[string]any) {
	t.Helper()
	keys := []string{"source", "target", "flag_key", "at", "marker",
		"changed_fields", "comparison", "source_status", "target_status"}
	want := map[string]bool{}
	for _, key := range keys {
		want[key] = true
	}
	for key := range body {
		if !want[key] {
			t.Fatalf("unexpected top-level key %q in %#v", key, body)
		}
	}
	if len(body) != len(keys) {
		t.Fatalf("top-level keys = %v, want exactly %v", body, keys)
	}
}

func TestCompareDifferentConfigsListsChangedFields(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	sourceVersion := h.putConfig(t, "prod", "checkout",
		`{"enabled":true,"percentage":50,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)["version"]
	h.setTime("2026-01-01T02:30:00Z")
	targetVersion := h.putConfig(t, "staging", "checkout",
		`{"enabled":false,"percentage":10,"window":{"starts_at":"2026-01-01T04:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)["version"]

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "")))
	assertCompareTopLevel(t, body)
	if body["flag_key"] != "checkout" || body["at"] != "2026-01-01T05:00:00Z" || body["marker"] != nil {
		t.Fatalf("identity fields wrong: %#v", body)
	}
	if body["comparison"] != "different" {
		t.Errorf("comparison = %v, want different", body["comparison"])
	}
	wantFields := []string{"enabled", "percentage", "window.starts_at"}
	if got := stringSlice(body["changed_fields"]); !reflect.DeepEqual(got, wantFields) {
		t.Errorf("changed_fields = %v, want %v", got, wantFields)
	}
	source := compareConfig(t, body, "source")
	target := compareConfig(t, body, "target")
	if source["version"] != sourceVersion || target["version"] != targetVersion {
		t.Errorf("versions = %v / %v", source["version"], target["version"])
	}
	if source["enabled"] != true || number(source["percentage"]) != 50 {
		t.Errorf("source config wrong: %#v", source)
	}
	if target["enabled"] != false || number(target["percentage"]) != 10 {
		t.Errorf("target config wrong: %#v", target)
	}
	sourceWindow := asMap(t, source["window"])
	if sourceWindow["starts_at"] != "2026-01-01T03:00:00Z" || sourceWindow["ends_at"] != "2026-01-01T06:00:00Z" {
		t.Errorf("source window wrong: %#v", sourceWindow)
	}
	if body["source_status"] != "not_evaluated" || body["target_status"] != "not_evaluated" {
		t.Errorf("statuses without marker = %v / %v", body["source_status"], body["target_status"])
	}
}

func TestCompareSameConfigsAndNullWindowEndpoints(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":42}`)
	h.setTime("2026-01-01T03:00:00Z")
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":42}`)

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "")))
	if body["comparison"] != "same" {
		t.Errorf("comparison = %v, want same", body["comparison"])
	}
	if got := stringSlice(body["changed_fields"]); len(got) != 0 {
		t.Errorf("changed_fields = %v, want empty array", got)
	}
	for _, side := range []string{"source", "target"} {
		cfg := compareConfig(t, body, side)
		window := asMap(t, cfg["window"])
		if window["starts_at"] != nil || window["ends_at"] != nil {
			t.Errorf("%s empty window endpoints must be null: %#v", side, window)
		}
	}
}

func TestCompareOnlySourceAndOnlyTarget(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout",
		`{"enabled":false,"percentage":0,"window":{"ends_at":"2026-01-01T06:00:00Z"}}`)

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "")))
	if body["comparison"] != "only_source" {
		t.Errorf("comparison = %v, want only_source", body["comparison"])
	}
	if compareConfig(t, body, "target") != nil {
		t.Errorf("target must be null: %#v", body["target"])
	}
	wantFields := []string{"enabled", "percentage", "window.ends_at"}
	if got := stringSlice(body["changed_fields"]); !reflect.DeepEqual(got, wantFields) {
		t.Errorf("changed_fields = %v, want %v", got, wantFields)
	}

	reversed := compareOK(t, h, compareURL("checkout",
		compareQuery("staging", "prod", "2026-01-01T05:00:00Z", "")))
	if reversed["comparison"] != "only_target" {
		t.Errorf("comparison = %v, want only_target", reversed["comparison"])
	}
	if compareConfig(t, reversed, "source") != nil {
		t.Errorf("source must be null when source has no config")
	}
}

func TestCompareUnconfiguredBoth(t *testing.T) {
	h := seedCompareEnvs(t)
	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "alpha")))
	if body["comparison"] != "unconfigured_both" {
		t.Errorf("comparison = %v, want unconfigured_both", body["comparison"])
	}
	if body["source"] != nil || body["target"] != nil {
		t.Errorf("both sides must be null: %#v %#v", body["source"], body["target"])
	}
	if got := stringSlice(body["changed_fields"]); len(got) != 0 {
		t.Errorf("changed_fields = %v, want empty array", got)
	}
	if body["source_status"] != "unconfigured" || body["target_status"] != "unconfigured" {
		t.Errorf("statuses = %v / %v, want unconfigured", body["source_status"], body["target_status"])
	}
}

func TestCompareTombstoneMeansNoEffectiveConfig(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":100}`)
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":100}`)
	h.setTime("2026-01-01T04:00:00Z")
	h.must(t, "DELETE", "/api/v1/environments/prod/flags/checkout/config", "")

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "alpha")))
	if body["comparison"] != "only_target" {
		t.Errorf("comparison = %v, want only_target after tombstone", body["comparison"])
	}
	if body["source"] != nil {
		t.Errorf("tombstoned source must be null: %#v", body["source"])
	}
	if body["source_status"] != "unconfigured" || body["target_status"] != "on" {
		t.Errorf("statuses = %v / %v, want unconfigured / on", body["source_status"], body["target_status"])
	}
}

func TestCompareRestoresVersionAsOfAtAndDoesNotLeakLaterWrites(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	oldSource := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)["version"]
	h.setTime("2026-01-01T02:00:00Z")
	oldTarget := h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":20}`)["version"]
	h.setTime("2026-01-01T08:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":false,"percentage":90}`)
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":100}`)

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "")))
	source := compareConfig(t, body, "source")
	target := compareConfig(t, body, "target")
	if source["version"] != oldSource || target["version"] != oldTarget {
		t.Errorf("restored versions = %v / %v, want %v / %v",
			source["version"], target["version"], oldSource, oldTarget)
	}
	if number(source["percentage"]) != 10 || number(target["percentage"]) != 20 {
		t.Errorf("restored percentages = %v / %v, want 10 / 20",
			source["percentage"], target["percentage"])
	}

	beforeAt := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T02:00:00Z", "")))
	if beforeAt["comparison"] != "different" {
		t.Errorf("comparison at exact changed_at = %v, want different (inclusive)", beforeAt["comparison"])
	}
	earlier := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T01:59:59Z", "")))
	if earlier["comparison"] != "unconfigured_both" {
		t.Errorf("comparison before first write = %v, want unconfigured_both", earlier["comparison"])
	}
}

func TestCompareSameInstantWritesTakeLastByInsertionOrder(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	first := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":10}`)["version"]
	second := h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":20}`)["version"]
	h.putConfig(t, "staging", "checkout", `{"enabled":true,"percentage":30}`)

	body := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "")))
	source := compareConfig(t, body, "source")
	if source["version"] != second || source["version"] == first {
		t.Errorf("source version = %v, want last same-instant write %v", source["version"], second)
	}
	if number(source["percentage"]) != 20 {
		t.Errorf("source percentage = %v, want 20 from the later write", source["percentage"])
	}
	wantFields := []string{"percentage"}
	if got := stringSlice(body["changed_fields"]); !reflect.DeepEqual(got, wantFields) {
		t.Errorf("changed_fields = %v, want %v", got, wantFields)
	}
}

func TestCompareMarkerEvaluatesBothSidesByEnvironmentBucket(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout",
		`{"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)
	h.putConfig(t, "staging", "checkout",
		`{"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}`)

	inside := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "alpha")))
	if inside["marker"] != "alpha" {
		t.Errorf("marker = %v, want alpha", inside["marker"])
	}
	if inside["source_status"] != "on" || inside["target_status"] != "off" {
		t.Errorf("statuses = %v / %v, want on / off from per-environment buckets",
			inside["source_status"], inside["target_status"])
	}
	outside := compareOK(t, h, compareURL("checkout",
		compareQuery("prod", "staging", "2026-01-01T02:59:59Z", "alpha")))
	if outside["source_status"] != "off" || outside["target_status"] != "off" {
		t.Errorf("statuses outside window = %v / %v, want off / off",
			outside["source_status"], outside["target_status"])
	}
}

func TestCompareIsDeterministicAndAppendsNoHistory(t *testing.T) {
	h := seedCompareEnvs(t)
	h.setTime("2026-01-01T02:00:00Z")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":50}`)
	url := compareURL("checkout", compareQuery("prod", "staging", "2026-01-01T05:00:00Z", "alpha"))

	first := h.must(t, http.MethodGet, url, "")
	second := h.must(t, http.MethodGet, url, "")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated queries differ:\n%#v\n%#v", first, second)
	}
	historyBefore := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/history", "")
	_ = compareOK(t, h, url)
	_ = compareOK(t, h, url)
	historyAfter := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/history", "")
	if !reflect.DeepEqual(historyBefore, historyAfter) {
		t.Fatalf("comparison appended history records:\nbefore=%#v\nafter=%#v",
			historyBefore, historyAfter)
	}
}

func TestCompareValidationErrors(t *testing.T) {
	h := seedCompareEnvs(t)
	good := "source=prod&target=staging&at=2026-01-01T05:00:00Z&marker=alpha"

	cases := []struct {
		name  string
		path  string
		query string
		code  string
	}{
		{"invalid flag key", "/api/v1/compare/BAD_KEY", good, "InvalidRequest"},
		{"missing source", "/api/v1/compare/checkout",
			"target=staging&at=2026-01-01T05:00:00Z", "InvalidRequest"},
		{"missing target", "/api/v1/compare/checkout",
			"source=prod&at=2026-01-01T05:00:00Z", "InvalidRequest"},
		{"invalid source", "/api/v1/compare/checkout",
			"source=BadEnv&target=staging&at=2026-01-01T05:00:00Z", "InvalidRequest"},
		{"same source and target", "/api/v1/compare/checkout",
			"source=prod&target=prod&at=2026-01-01T05:00:00Z", "InvalidRequest"},
		{"missing at", "/api/v1/compare/checkout",
			"source=prod&target=staging", "InvalidTimestamp"},
		{"invalid at", "/api/v1/compare/checkout",
			"source=prod&target=staging&at=not-a-time", "InvalidTimestamp"},
		{"invalid marker", "/api/v1/compare/checkout",
			"source=prod&target=staging&at=2026-01-01T05:00:00Z&marker=BAD", "InvalidMarker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, tc.path+"?"+tc.query, "")
			assertErrorCode(t, rec, http.StatusBadRequest, tc.code)
		})
	}
}

func TestCompareNotFoundErrors(t *testing.T) {
	h := seedCompareEnvs(t)

	missingSource := h.do(t, http.MethodGet, "/api/v1/compare/checkout?source=canary&target=staging&at=2026-01-01T05:00:00Z", "")
	assertErrorCode(t, missingSource, http.StatusNotFound, "EnvironmentNotFound")

	missingTarget := h.do(t, http.MethodGet, "/api/v1/compare/checkout?source=prod&target=canary&at=2026-01-01T05:00:00Z", "")
	assertErrorCode(t, missingTarget, http.StatusNotFound, "EnvironmentNotFound")

	missingFlag := h.do(t, http.MethodGet, "/api/v1/compare/missing?source=prod&target=staging&at=2026-01-01T05:00:00Z", "")
	assertErrorCode(t, missingFlag, http.StatusNotFound, "FlagNotFound")
}

func TestCompareRequestErrorsCheckedBeforeExistence(t *testing.T) {
	h := seedCompareEnvs(t)

	// An invalid request shape wins over unknown resources.
	rec := h.do(t, http.MethodGet, "/api/v1/compare/missing?source=Bad&target=missing2&at=2026-01-01T05:00:00Z", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidRequest")

	// A bad timestamp wins over unknown environments and flags.
	rec = h.do(t, http.MethodGet, "/api/v1/compare/missing?source=nope&target=nope2&at=bad", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidTimestamp")

	// A bad marker wins over unknown environments.
	rec = h.do(t, http.MethodGet, "/api/v1/compare/checkout?source=nope&target=nope2&at=2026-01-01T05:00:00Z&marker=BAD", "")
	assertErrorCode(t, rec, http.StatusBadRequest, "InvalidMarker")
}
