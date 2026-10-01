package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/luwa07832/feature-flag-service/internal/store"
)

type apiHarness struct {
	router http.Handler
	db     *store.Store
	clock  *testClock
}

type testClock struct{ t time.Time }

func (c *testClock) advance(to time.Time) { c.t = to }

func newHarness(t *testing.T) *apiHarness {
	t.Helper()
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st, err := store.OpenWithClock(filepath.Join(t.TempDir(), "service.db"), func() time.Time {
		return clock.t
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &apiHarness{router: NewRouter(st), db: st, clock: clock}
}

func (h *apiHarness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *apiHarness) must(t *testing.T, method, path, body string) map[string]any {
	t.Helper()
	rec := h.do(t, method, path, body)
	if rec.Code < 200 || rec.Code > 299 {
		t.Fatalf("%s %s -> %d %s", method, path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return out
}

func (h *apiHarness) createEnv(t *testing.T, key string) {
	t.Helper()
	h.must(t, http.MethodPost, "/api/v1/environments", `{"key":"`+key+`"}`)
}

func (h *apiHarness) createFlag(t *testing.T, key string) {
	t.Helper()
	h.must(t, http.MethodPost, "/api/v1/flags", `{"key":"`+key+`"}`)
}

func (h *apiHarness) putConfig(t *testing.T, env, flag, payload string) map[string]any {
	t.Helper()
	return h.must(t, http.MethodPut, "/api/v1/environments/"+env+"/flags/"+flag+"/config", payload)
}

func (h *apiHarness) setTime(ts string) {
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		panic(err)
	}
	h.clock.advance(parsed)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return out
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %#v", v)
	}
	return m
}
