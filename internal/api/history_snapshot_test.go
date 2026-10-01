package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/luwa07832/feature-flag-service/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func putConfig(t *testing.T, router http.Handler, env, flag, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/environments/"+env+"/flags/"+flag+"/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("put config %s/%s status=%d body=%s", env, flag, rec.Code, rec.Body.String())
	}
}

func snapshot(t *testing.T, router http.Handler, env string, query url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/environments/"+env+"/snapshot?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func findFlag(t *testing.T, body map[string]any, flagID string) map[string]any {
	t.Helper()
	flags, _ := body["flags"].([]any)
	for _, f := range flags {
		entry, _ := f.(map[string]any)
		if entry["flag_id"] == flagID {
			return entry
		}
	}
	t.Fatalf("flag %q not in response: %v", flagID, body)
	return nil
}

func TestPointInTimeSnapshotLifecycle(t *testing.T) {
	st := newTestStore(t)
	router := NewRouter(st)

	firstAt := "2026-03-01T10:00:00Z"
	secondAt := "2026-03-01T12:00:00Z"
	laterFlagAt := "2026-03-01T14:00:00Z"

	putConfig(t, router, "prod", "checkout",
		`{"enabled":true,"rollout_percentage":100,"changed_at":"`+firstAt+`","note":"on"}`)
	putConfig(t, router, "prod", "checkout",
		`{"enabled":false,"rollout_percentage":0,"changed_at":"`+secondAt+`","note":"off"}`)
	putConfig(t, router, "prod", "newer_flag",
		`{"enabled":true,"rollout_percentage":100,"changed_at":"`+laterFlagAt+`"}`)

	t.Run("invalid timestamp", func(t *testing.T) {
		code, body := snapshot(t, router, "prod", url.Values{"at": {"not-a-time"}})
		if code != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "InvalidTimestamp" {
			t.Fatalf("code=%d body=%v", code, body)
		}
	})

	t.Run("missing environment", func(t *testing.T) {
		code, body := snapshot(t, router, "staging", url.Values{"at": {firstAt}})
		if code != http.StatusNotFound || body["error"].(map[string]any)["code"] != "EnvironmentNotFound" {
			t.Fatalf("code=%d body=%v", code, body)
		}
	})

	t.Run("before anything existed", func(t *testing.T) {
		code, body := snapshot(t, router, "prod", url.Values{"at": {"2026-03-01T09:00:00Z"}})
		if code != http.StatusNotFound || body["error"].(map[string]any)["code"] != "EnvironmentNotFound" {
			t.Fatalf("code=%d body=%v", code, body)
		}
	})

	t.Run("invalid marker", func(t *testing.T) {
		code, body := snapshot(t, router, "prod",
			url.Values{"at": {firstAt}, "marker": {"bad marker!"}})
		if code != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "InvalidMarker" {
			t.Fatalf("code=%d body=%v", code, body)
		}
	})

	t.Run("snapshot without marker is unevaluated", func(t *testing.T) {
		code, body := snapshot(t, router, "prod", url.Values{"at": {secondAt}})
		if code != http.StatusOK {
			t.Fatalf("body=%v", body)
		}
		if body["at"] != secondAt {
			t.Fatalf("at = %v", body["at"])
		}
		checkout := findFlag(t, body, "checkout")
		if checkout["evaluated"] != false || checkout["result"] != "unevaluated" {
			t.Fatalf("checkout = %v", checkout)
		}
		if checkout["enabled"] != false || checkout["rollout_percentage"].(float64) != 0 {
			t.Fatalf("restored config wrong: %v", checkout)
		}
		if checkout["version_id"] != "v2" {
			t.Fatalf("version_id = %v", checkout["version_id"])
		}
	})

	t.Run("snapshot at first version ignores later changes", func(t *testing.T) {
		code, body := snapshot(t, router, "prod", url.Values{
			"at": {"2026-03-01T11:00:00Z"}, "marker": {"user-1"}})
		if code != http.StatusOK {
			t.Fatalf("body=%v", body)
		}
		checkout := findFlag(t, body, "checkout")
		if checkout["version_id"] != "v1" || checkout["result"] != "on" || checkout["enabled"] != true {
			t.Fatalf("checkout = %v", checkout)
		}
		newer := findFlag(t, body, "newer_flag")
		if newer["version_id"] != nil || newer["result"] != "unconfigured" ||
			newer["rollout_percentage"] != nil || newer["effective_window"] != nil ||
			newer["evaluated"] != true {
			t.Fatalf("newer flag at the time = %v", newer)
		}
	})

	t.Run("repeated queries are identical", func(t *testing.T) {
		q := url.Values{"at": {secondAt}, "marker": {"user-1"}}
		_, first := snapshot(t, router, "prod", q)
		_, second := snapshot(t, router, "prod", q)
		if !equalJSON(first, second) {
			t.Fatalf("responses differ:\n%v\n%v", first, second)
		}
	})

	t.Run("query does not write history", func(t *testing.T) {
		historyReq := httptest.NewRequest(http.MethodGet,
			"/api/v1/environments/prod/flags/checkout/changes", nil)
		before := httptest.NewRecorder()
		router.ServeHTTP(before, historyReq)
		var beforeBody map[string]any
		_ = json.Unmarshal(before.Body.Bytes(), &beforeBody)
		if len(beforeBody["changes"].([]any)) != 2 {
			t.Fatalf("changes = %v", beforeBody)
		}
		for range 3 {
			snapshot(t, router, "prod", url.Values{"at": {secondAt}, "marker": {"user-1"}})
		}
		historyReq2 := httptest.NewRequest(http.MethodGet,
			"/api/v1/environments/prod/flags/checkout/changes", nil)
		after := httptest.NewRecorder()
		router.ServeHTTP(after, historyReq2)
		if after.Body.String() != before.Body.String() {
			t.Fatalf("history changed after read-only queries:\n%s\n%s", before.Body.String(), after.Body.String())
		}
	})
}

func TestRealTimeEvaluationUsesCurrentConfig(t *testing.T) {
	st := newTestStore(t)
	router := NewRouter(st)
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	putConfig(t, router, "prod", "checkout",
		`{"enabled":true,"rollout_percentage":100,"changed_at":"`+at+`"}`)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/evaluate?marker=user-7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["result"] != "on" || body["evaluated"] != true || body["version_id"] != "v1" {
		t.Fatalf("body=%v", body)
	}

	badReq := httptest.NewRequest(http.MethodGet,
		"/api/v1/environments/prod/flags/checkout/evaluate?marker=%21bad", nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", badRec.Code)
	}
}

func equalJSON(a, b map[string]any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return bytes.Equal(aj, bj)
}
