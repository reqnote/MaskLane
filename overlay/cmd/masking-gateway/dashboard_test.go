package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskingGatewayDashboardWorkflow(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "mappings.json")
	store, err := openMappings(filename)
	if err != nil {
		t.Fatal(err)
	}
	const host = "127.0.0.1:40000"
	const token = "test-session"
	handler := newDashboard(host, token, "127.0.0.1:18888", store, &activityLog{})
	call := func(method, route, body, session, origin string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+host+route, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Masking-Session", session)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != expected {
			t.Fatalf("%s %s returned %d, wanted %d: %s", method, route, w.Code, expected, w.Body.String())
		}
		return w
	}
	call(http.MethodGet, "/api/mappings", "", "", "", http.StatusForbidden)
	call(http.MethodGet, "/api/mappings", "", token, "https://external.invalid", http.StatusForbidden)
	first := `{"revision":1,"rules":[{"kind":"company","original":"青葉商事","replacement":"顧客A"}]}`
	call(http.MethodPut, "/api/mappings", first, token, "", http.StatusOK)
	call(http.MethodPut, "/api/mappings", first, token, "", http.StatusConflict)
	invalid := `{"revision":2,"rules":[{"kind":"company","original":"青葉商事","replacement":"青葉商事向け"}]}`
	call(http.MethodPut, "/api/mappings", invalid, token, "", http.StatusBadRequest)
	incoming := "{\"version\":1,\"rules\":[{\"kind\":\"company\",\"original\":\"青葉商事\",\"replacement\":\"顧客B\"},{\"kind\":\"project\",\"original\":\"刷新計画\",\"replacement\":\"\"}]}"
	preview := call(http.MethodPost, "/api/import-preview", incoming, token, "", http.StatusOK)
	var plan importPlan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || plan.Duplicate != 1 || len(plan.Added) != 1 || plan.Added[0].Replacement != "" {
		t.Fatalf("unexpected import plan: %+v", plan)
	}
	if len(store.active.Load().Rules) != 1 {
		t.Fatal("preview changed saved mappings")
	}
	merged, err := store.update(plan.Revision, append([]mapping{store.active.Load().Rules[0]}, plan.Added...))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openMappings(filename)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.active.Load().Rules
	if len(got) != 2 || got[0].Replacement != "顧客A" || got[1] != merged.Rules[1] {
		t.Fatalf("unexpected persisted mappings: %+v", got)
	}
}

func TestMaskingGatewayDestinationPolicy(t *testing.T) {
	handler := kiroConnections(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), modelHosts)
	for _, test := range []struct {
		method, target string
		status         int
	}{
		{"CONNECT", "runtime.us-east-1.kiro.dev:443", http.StatusNoContent},
		{"CONNECT", "runtime.us-east-1.kiro.dev:80", http.StatusForbidden},
		{"CONNECT", "127.0.0.1:443", http.StatusForbidden},
		{"GET", "/fetch", http.StatusForbidden},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(test.method, test.target, nil))
		if w.Code != test.status {
			t.Errorf("%s %s: got %d, want %d", test.method, test.target, w.Code, test.status)
		}
	}
}
