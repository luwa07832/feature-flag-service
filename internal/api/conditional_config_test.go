package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const conditionalConfigPath = "/api/v1/environments/prod/flags/checkout/config/conditional"

func (h *apiHarness) conditional(t *testing.T, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPost, conditionalConfigPath, payload)
}

func decodeJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return out
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	out := decodeJSON(t, rec.Body.Bytes())
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing: %s", rec.Body.String())
	}
	code, _ := errObj["code"].(string)
	if code == "" {
		t.Fatalf("error code missing: %s", rec.Body.String())
	}
	return code
}

func assertErrorShape(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	out := decodeJSON(t, rec.Body.Bytes())
	if len(out) != 1 {
		t.Fatalf("top-level keys = %v, want only error", out)
	}
	errObj, ok := out["error"].(map[string]any)
	if !ok || len(errObj) != 2 {
		t.Fatalf("error must contain only code and message: %s", rec.Body.String())
	}
	if _, ok := errObj["code"].(string); !ok {
		t.Fatalf("code must be a string: %s", rec.Body.String())
	}
	if _, ok := errObj["message"].(string); !ok {
		t.Fatalf("message must be a string: %s", rec.Body.String())
	}
}

func TestConditionalConfigCreatesWhenNoLiveVersion(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")

	rec := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":25}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", rec.Code, rec.Body.String())
	}
	out := decodeJSON(t, rec.Body.Bytes())
	if out["flag_key"] != "checkout" || out["environment"] != "prod" {
		t.Fatalf("identity = %v/%v", out["flag_key"], out["environment"])
	}
	if out["enabled"] != true || out["percentage"] != float64(25) {
		t.Fatalf("payload = enabled %v percentage %v", out["enabled"], out["percentage"])
	}
	if out["tombstone"] != false {
		t.Fatalf("tombstone = %v, want false", out["tombstone"])
	}
	if out["changed_at"] != "2026-01-01T02:00:00Z" {
		t.Fatalf("changed_at = %v", out["changed_at"])
	}
	if version, _ := out["version"].(string); version == "" || !strings.HasPrefix(version, "cfg-") {
		t.Fatalf("version = %v, want cfg-* string", out["version"])
	}
	window, ok := out["window"].(map[string]any)
	if !ok || window["starts_at"] != nil || window["ends_at"] != nil {
		t.Fatalf("window = %v, want null endpoints", out["window"])
	}
}

func TestConditionalConfigCASChain(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")

	first := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d %s", first.Code, first.Body.String())
	}
	v1 := decodeJSON(t, first.Body.Bytes())["version"].(string)

	stale := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":20}`)
	if stale.Code != http.StatusConflict || errorCode(t, stale) != "VersionConflict" {
		t.Fatalf("stale null expected status = %d body %s", stale.Code, stale.Body.String())
	}

	h.setTime("2026-01-01T03:00:00Z")
	second := h.conditional(t, `{"expected_version":"`+v1+`","enabled":false,"percentage":20}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("cas update status = %d %s", second.Code, second.Body.String())
	}
	v2 := decodeJSON(t, second.Body.Bytes())
	if v2["version"] == v1 {
		t.Fatalf("new version equals old: %v", v2["version"])
	}
	if v2["enabled"] != false || v2["percentage"] != float64(20) {
		t.Fatalf("second payload = %v", v2)
	}

	replayed := h.conditional(t, `{"expected_version":"`+v1+`","enabled":true,"percentage":30}`)
	if replayed.Code != http.StatusConflict || errorCode(t, replayed) != "VersionConflict" {
		t.Fatalf("replayed v1 status = %d body %s", replayed.Code, replayed.Body.String())
	}

	h.setTime("2026-01-01T04:00:00Z")
	third := h.conditional(t, `{"expected_version":"`+v2["version"].(string)+`","enabled":true,"percentage":30,
		"window":{"starts_at":"2026-01-01T00:00:00Z","ends_at":"2026-02-01T00:00:00Z"}}`)
	if third.Code != http.StatusCreated {
		t.Fatalf("third status = %d %s", third.Code, third.Body.String())
	}
	v3 := decodeJSON(t, third.Body.Bytes())
	window := v3["window"].(map[string]any)
	if window["starts_at"] != "2026-01-01T00:00:00Z" || window["ends_at"] != "2026-02-01T00:00:00Z" {
		t.Fatalf("window = %v", window)
	}

	history := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	versions := history["versions"].([]any)
	if len(versions) != 3 {
		t.Fatalf("history length = %d, want 3 (no failed records)", len(versions))
	}
	for _, item := range versions {
		if item.(map[string]any)["tombstone"] != false {
			t.Fatalf("unexpected tombstone: %v", item)
		}
	}
}

func TestConditionalConfigNullAfterTombstone(t *testing.T) {
	h := seededHarness(t)
	v2 := h.v2["version"].(string)

	if rec := h.do(t, http.MethodPost, "/api/v1/environments/prod/flags/checkout/config/conditional",
		`{"expected_version":"`+v2+`","enabled":true,"percentage":10}`); rec.Code != http.StatusConflict {
		t.Fatalf("tombstoned live state vs string expected: status = %d %s", rec.Code, rec.Body.String())
	}

	h.setTime("2026-01-02T01:00:00Z")
	rec := h.do(t, http.MethodPost, "/api/v1/environments/prod/flags/checkout/config/conditional",
		`{"expected_version":null,"enabled":true,"percentage":77}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("recreate after tombstone: status = %d %s", rec.Code, rec.Body.String())
	}
	out := decodeJSON(t, rec.Body.Bytes())
	if out["percentage"] != float64(77) {
		t.Fatalf("percentage = %v", out["percentage"])
	}
}

func TestConditionalConfigConcurrentSameExpected(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	first := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("seed create: %d %s", first.Code, first.Body.String())
	}
	version := decodeJSON(t, first.Body.Bytes())["version"].(string)

	const n = 16
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := `{"expected_version":"` + version + `","enabled":true,"percentage":20}`
			req := httptest.NewRequest(http.MethodPost, conditionalConfigPath, strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.router.ServeHTTP(rec, req)
			results[i] = rec.Code
		}(i)
	}
	wg.Wait()

	created, conflicts := 0, 0
	for _, code := range results {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("created = %d, conflicts = %d, want 1 and %d", created, conflicts, n-1)
	}

	history := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	if got := len(history["versions"].([]any)); got != 2 {
		t.Fatalf("history length = %d, want 2", got)
	}
}

func TestConditionalConfigConcurrentNullExpected(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")

	const n = 12
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, conditionalConfigPath,
				strings.NewReader(`{"expected_version":null,"enabled":true,"percentage":5}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.router.ServeHTTP(rec, req)
			results[i] = rec.Code
		}(i)
	}
	wg.Wait()

	created := 0
	for _, code := range results {
		if code == http.StatusCreated {
			created++
		} else if code != http.StatusConflict {
			t.Fatalf("unexpected status %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
}

func TestConditionalConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		code string
	}{
		{"body required", ``, http.StatusBadRequest, "InvalidRequest"},
		{"expected missing", `{"enabled":true,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"expected number", `{"expected_version":3,"enabled":true,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"expected bool", `{"expected_version":true,"enabled":true,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"expected object", `{"expected_version":{},"enabled":true,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"enabled missing", `{"expected_version":null,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"enabled null", `{"expected_version":null,"enabled":null,"percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"enabled string", `{"expected_version":null,"enabled":"yes","percentage":10}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage missing", `{"expected_version":null,"enabled":true}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage null", `{"expected_version":null,"enabled":true,"percentage":null}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage float", `{"expected_version":null,"enabled":true,"percentage":10.5}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage string", `{"expected_version":null,"enabled":true,"percentage":"10"}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage too high", `{"expected_version":null,"enabled":true,"percentage":101}`, http.StatusBadRequest, "InvalidRequest"},
		{"percentage negative", `{"expected_version":null,"enabled":true,"percentage":-1}`, http.StatusBadRequest, "InvalidRequest"},
		{"unknown field", `{"expected_version":null,"enabled":true,"percentage":10,"extra":1}`, http.StatusBadRequest, "InvalidRequest"},
		{"window bad starts", `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"not-a-time"}}`, http.StatusBadRequest, "InvalidTimestamp"},
		{"window bad ends", `{"expected_version":null,"enabled":true,"percentage":10,"window":{"ends_at":"2026-13-01T00:00:00Z"}}`, http.StatusBadRequest, "InvalidTimestamp"},
		{"window ends before starts", `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-02T00:00:00Z","ends_at":"2026-01-01T00:00:00Z"}}`, http.StatusBadRequest, "InvalidRequest"},
		{"window ends equal starts", `{"expected_version":null,"enabled":true,"percentage":10,"window":{"starts_at":"2026-01-01T00:00:00Z","ends_at":"2026-01-01T00:00:00Z"}}`, http.StatusBadRequest, "InvalidRequest"},
		{"unknown version", `{"expected_version":"cfg-does-not-exist","enabled":true,"percentage":10}`, http.StatusConflict, "VersionConflict"},
		{"empty string version", `{"expected_version":"","enabled":true,"percentage":10}`, http.StatusConflict, "VersionConflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.createEnv(t, "prod")
			h.createFlag(t, "checkout")
			rec := h.conditional(t, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if got := errorCode(t, rec); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
			assertErrorShape(t, rec)
		})
	}
}

func TestConditionalConfigNotFoundAndKeyValidation(t *testing.T) {
	h := newHarness(t)
	h.createFlag(t, "checkout")

	rec := h.do(t, http.MethodPost, "/api/v1/environments/missing/flags/checkout/config/conditional",
		`{"expected_version":null,"enabled":true,"percentage":10}`)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "EnvironmentNotFound" {
		t.Fatalf("missing env: %d %s", rec.Code, rec.Body.String())
	}
	assertErrorShape(t, rec)

	h.createEnv(t, "prod")
	rec = h.do(t, http.MethodPost, "/api/v1/environments/prod/flags/missing/config/conditional",
		`{"expected_version":null,"enabled":true,"percentage":10}`)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "FlagNotFound" {
		t.Fatalf("missing flag: %d %s", rec.Code, rec.Body.String())
	}
	assertErrorShape(t, rec)

	rec = h.do(t, http.MethodPost, "/api/v1/environments/BAD/flags/checkout/config/conditional",
		`{"expected_version":null,"enabled":true,"percentage":10}`)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("bad env key: %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do(t, http.MethodPost, "/api/v1/environments/prod/flags/BAD.FLAG/config/conditional",
		`{"expected_version":null,"enabled":true,"percentage":10}`)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("bad flag key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConditionalConfigBodyCheckedBeforeExistence(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/v1/environments/missing/flags/missing/config/conditional",
		`{"expected_version":null,"enabled":true}`)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("body should win over existence: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConditionalConfigWindowNullMeansOmitted(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	rec := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10,"window":null}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	window := decodeJSON(t, rec.Body.Bytes())["window"].(map[string]any)
	if window["starts_at"] != nil || window["ends_at"] != nil {
		t.Fatalf("window = %v, want null endpoints", window)
	}
}

func TestConditionalConfigRepeatWithoutContentionCreatesVersions(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")

	first := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10}`)
	v1 := decodeJSON(t, first.Body.Bytes())["version"].(string)
	h.setTime("2026-01-01T03:00:00Z")
	second := h.conditional(t, `{"expected_version":"`+v1+`","enabled":true,"percentage":10}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("identical resubmit: %d %s", second.Code, second.Body.String())
	}
	v2 := decodeJSON(t, second.Body.Bytes())["version"].(string)
	if v1 == v2 {
		t.Fatalf("repeat produced same version %s", v1)
	}

	explain := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/explain?marker=alpha", "")
	explainConfig := explain["config"].(map[string]any)
	if explainConfig["version"] != v2 {
		t.Fatalf("explain version = %v, want latest %s", explainConfig["version"], v2)
	}

	evaluate := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/evaluate?marker=alpha", "")
	flags := evaluate["flags"].([]any)
	var found map[string]any
	for _, f := range flags {
		fm := f.(map[string]any)
		if fm["flag_key"] == "checkout" {
			found = fm
		}
	}
	if found == nil {
		t.Fatalf("checkout missing from evaluate: %v", flags)
	}
	if found["version"] != v2 {
		t.Fatalf("evaluate version = %v, want %s", found["version"], v2)
	}
}

func TestConditionalConfigParticipatesInHistoricalSurfaces(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createEnv(t, "staging")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")
	prod := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":100}`)
	prodVersion := decodeJSON(t, prod.Body.Bytes())["version"].(string)

	h.setTime("2026-01-01T05:00:00Z")
	stagingRec := h.do(t, http.MethodPost,
		"/api/v1/environments/staging/flags/checkout/config/conditional",
		`{"expected_version":null,"enabled":false,"percentage":0}`)
	if stagingRec.Code != http.StatusCreated {
		t.Fatalf("staging create: %d %s", stagingRec.Code, stagingRec.Body.String())
	}
	stagingVersion := decodeJSON(t, stagingRec.Body.Bytes())["version"].(string)

	at := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/evaluate-at?at=2026-01-01T03:00:00Z&marker=alpha", "")
	flags := at["flags"].([]any)
	var prodAt map[string]any
	for _, f := range flags {
		fm := f.(map[string]any)
		if fm["flag_key"] == "checkout" {
			prodAt = fm
		}
	}
	if prodAt == nil || prodAt["version"] != prodVersion {
		t.Fatalf("evaluate-at version = %v, want %s", prodAt, prodVersion)
	}
	if prodAt["status"] != "on" {
		t.Fatalf("evaluate-at status = %v, want on (percentage 100)", prodAt["status"])
	}

	before := h.do(t, http.MethodGet,
		"/api/v1/environments/prod/evaluate-at?at=2026-01-01T01:00:00Z&marker=alpha", "")
	if before.Code != http.StatusNotFound || errorCode(t, before) != "EnvironmentNotFound" {
		t.Fatalf("evaluate-at before any config: %d %s", before.Code, before.Body.String())
	}

	compare := h.must(t, http.MethodGet,
		"/api/v1/compare/checkout?source=prod&target=staging&at=2026-01-01T06:00:00Z&marker=alpha", "")
	if compare["comparison"] != "different" {
		t.Fatalf("comparison = %v, want different", compare["comparison"])
	}
	source := compare["source"].(map[string]any)
	target := compare["target"].(map[string]any)
	if source["version"] != prodVersion || target["version"] != stagingVersion {
		t.Fatalf("compare versions source=%v target=%v", source["version"], target["version"])
	}

	changes := h.must(t, http.MethodGet, "/api/v1/environments/prod/changes", "")
	items := changes["items"].([]any)
	last := items[len(items)-1].(map[string]any)
	if last["version"] != prodVersion {
		t.Fatalf("environment changes last version = %v, want %s", last["version"], prodVersion)
	}
}

func TestConditionalConfigFailedRequestAppendsNothing(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.setTime("2026-01-01T02:00:00Z")
	created := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10}`)
	version := decodeJSON(t, created.Body.Bytes())["version"].(string)

	for i := 0; i < 5; i++ {
		rec := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":20}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
		rec = h.conditional(t, `{"expected_version":"bogus","enabled":true,"percentage":20}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("attempt %d bogus: %d", i, rec.Code)
		}
	}
	history := h.must(t, http.MethodGet, "/api/v1/environments/prod/flags/checkout/history", "")
	versions := history["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("history length = %d, want 1", len(versions))
	}
	if versions[0].(map[string]any)["version"] != version {
		t.Fatalf("history version = %v, want %s", versions[0], version)
	}

	latest := h.must(t, http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/explain?marker=alpha", "")
	latestConfig := latest["config"].(map[string]any)
	if latestConfig["version"] != version {
		t.Fatalf("live version = %v, want %s", latestConfig["version"], version)
	}
}

func TestConditionalConfigNullExpectedConflictsWhenLive(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	h.putConfig(t, "prod", "checkout", `{"enabled":true,"percentage":99}`)

	rec := h.conditional(t, `{"expected_version":null,"enabled":true,"percentage":10}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "VersionConflict" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	assertErrorShape(t, rec)
}

func TestConditionalConfigUnknownStringVersionConflicts(t *testing.T) {
	h := newHarness(t)
	h.createEnv(t, "prod")
	h.createFlag(t, "checkout")
	for _, payload := range []string{
		`{"expected_version":"cfg-does-not-exist","enabled":true,"percentage":10}`,
		`{"expected_version":"","enabled":true,"percentage":10}`,
	} {
		rec := h.conditional(t, payload)
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "VersionConflict" {
			t.Fatalf("payload %s -> %d %s", payload, rec.Code, rec.Body.String())
		}
		assertErrorShape(t, rec)
	}
}
