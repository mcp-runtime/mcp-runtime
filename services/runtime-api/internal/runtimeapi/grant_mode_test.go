package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpaccess "mcp-runtime/pkg/access"
)

func TestRuntimeGrantModeManagement(t *testing.T) {
	mgr := newTestAccessManager(t)
	s := (&RuntimeServer{accessMgr: mgr}).Access()
	apply := func(mode string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/runtime/grants", strings.NewReader(`{"name":"monitor-grant","namespace":"mcp-servers","serverRef":{"name":"demo"},"subject":{"humanID":"user-1"},"maxTrust":"low","allowedSideEffects":["read"],"toolRules":[{"name":"echo","decision":"deny"}]`+mode+`}`))
		s.handleRuntimeGrantApply(w, r)
		return w
	}
	for _, mode := range []string{`,"mode":"invalid"`, `,"mode":""`} {
		if w := apply(mode); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid mode status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if w := apply(`,"mode":"monitor"`); w.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", w.Code, w.Body.String())
	}
	if w := apply(""); w.Code != http.StatusOK {
		t.Fatalf("reapply status=%d body=%s", w.Code, w.Body.String())
	}
	grant, err := mgr.GetGrant(context.Background(), "monitor-grant", "mcp-servers")
	if err != nil || grant.Spec.Mode != mcpaccess.GrantModeMonitor {
		t.Fatalf("omitted mode not preserved: grant=%#v err=%v", grant, err)
	}
	w := httptest.NewRecorder()
	s.handleGrantGet(w, httptest.NewRequest(http.MethodGet, "/api/runtime/grants/mcp-servers/monitor-grant", nil), "mcp-servers", "monitor-grant")
	var response struct {
		Grant mcpaccess.GrantSummary `json:"grant"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.Grant.Mode != mcpaccess.GrantModeMonitor {
		t.Fatalf("GET lost mode: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if err := mgr.DisableGrant(context.Background(), "monitor-grant", "mcp-servers"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"mode":"invalid"}`, 400}, {`{"mode":""}`, 400}, {`{"mode":"enforce","disabled":true}`, 400}, {`{"mode":"enforce"}`, 200}} {
		w = httptest.NewRecorder()
		s.handleGrantPatch(w, httptest.NewRequest(http.MethodPatch, "/api/runtime/grants/mcp-servers/monitor-grant", strings.NewReader(tc.body)), "mcp-servers", "monitor-grant")
		if w.Code != tc.status {
			t.Fatalf("PATCH %s = %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	grant, err = mgr.GetGrant(context.Background(), "monitor-grant", "mcp-servers")
	if err != nil || grant.Spec.Mode != mcpaccess.GrantModeEnforce || !grant.Spec.Disabled || len(grant.Spec.ToolRules) != 1 || grant.Spec.ToolRules[0].Decision != mcpaccess.DecisionDeny {
		t.Fatalf("PATCH lost grant policy: %#v err=%v", grant, err)
	}
}

func TestRuntimeGrantModePatchRejectsNonOwner(t *testing.T) {
	mgr := newTestAccessManager(t)
	s := (&RuntimeServer{accessMgr: mgr}).Access()
	apply := httptest.NewRecorder()
	s.handleRuntimeGrantApply(apply, httptest.NewRequest(http.MethodPost, "/api/runtime/grants", strings.NewReader(`{"name":"monitor-grant","namespace":"mcp-servers","serverRef":{"name":"demo"},"subject":{"humanID":"user-1"},"maxTrust":"low","allowedSideEffects":["read"],"mode":"enforce"}`)))
	if apply.Code != http.StatusOK {
		t.Fatalf("apply=%d %s", apply.Code, apply.Body.String())
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/runtime/grants/mcp-servers/monitor-grant", strings.NewReader(`{"mode":"monitor"}`))
	r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleUser, Subject: "other", Namespace: "other-namespace"}))
	w := httptest.NewRecorder()
	s.handleGrantPatch(w, r, "mcp-servers", "monitor-grant")
	if w.Code != http.StatusForbidden {
		t.Fatalf("mode update=%d %s", w.Code, w.Body.String())
	}
	grant, err := mgr.GetGrant(context.Background(), "monitor-grant", "mcp-servers")
	if err != nil || grant.Spec.Mode != mcpaccess.GrantModeEnforce {
		t.Fatalf("unauthorized update changed grant: %#v (%v)", grant, err)
	}
}
