package runtimeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func grafanaTestBody(expr string) string {
	return fmt.Sprintf(`{"from":"%d","to":"%d","queries":[{"refId":"A","datasource":{"uid":"prometheus","type":"prometheus"},"expr":%q,"range":true,"intervalMs":1000,"maxDataPoints":100000,"datasourceId":99,"url":"http://foreign"}]}`, time.Now().Add(-time.Hour).UnixMilli(), time.Now().UnixMilli(), expr)
}

func TestTenantGrafanaQueryBoundary(t *testing.T) {
	t.Setenv("GRAFANA_SCOPED_USER_ACCESS", "true")
	owned := ownedTestMCPServer("demo", "mcp-servers", "alice")
	foreign := ownedTestMCPServer("foreign", "mcp-servers", "bob")
	team := ownedTestMCPServer("team", "mcp-team-acme", "bob")
	server := newRuntimeServerWithMCPServers(t, owned, foreign, team)
	p := principal{Role: roleUser, Subject: "alice", Namespace: "alice", AllowedNamespaces: []string{"alice", "mcp-servers", "mcp-team-acme"}, Teams: []principalTeam{{Namespace: "mcp-team-acme", Role: "member"}}}
	queries := scopedPrometheusQueries(owned.Namespace, owned.Name, string(owned.UID))
	foreignQuery := scopedPrometheusQueries(foreign.Namespace, foreign.Name, string(foreign.UID))[0].Query
	teamQuery := scopedPrometheusQueries(team.Namespace, team.Name, string(team.UID))[0].Query
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/grafana/api/ds/query" || r.URL.RawQuery != "" {
			t.Errorf("unexpected Grafana target: %s", r.URL)
		}
		for _, header := range []string{"Authorization", "Cookie", "X-Grafana-Org-Id", "X-Forwarded-User"} {
			if r.Header.Get(header) != "" {
				t.Errorf("forwarded untrusted header %s", header)
			}
		}
		if r.Header.Get("X-WEBAUTH-USER") == "admin" || !strings.HasPrefix(r.Header.Get("X-WEBAUTH-USER"), "mcp-") || r.Header.Get("X-WEBAUTH-ROLE") != "Viewer" {
			t.Errorf("untrusted Grafana identity: %v", r.Header)
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		query := raw["queries"].([]any)[0].(map[string]any)
		if query["datasourceId"] != nil || query["url"] != nil || query["maxDataPoints"] != float64(2000) || query["intervalMs"] != float64(15000) {
			t.Errorf("unsafe query options survived reconstruction: %v", query)
		}
		w.Header().Set("Set-Cookie", "grafana_session=admin-secret")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":{"A":{"frames":[]}}}`)
	}))
	defer upstream.Close()
	t.Setenv("GRAFANA_UPSTREAM", upstream.URL)
	for _, tc := range []struct {
		name, body string
		p          principal
		want       int
	}{
		{"owner", grafanaTestBody(queries[0].Query), p, 200},
		{"team-member", grafanaTestBody(teamQuery), p, 200},
		{"foreign-owner", grafanaTestBody(foreignQuery), p, 403},
		{"foreign-team", grafanaTestBody(teamQuery), principal{Role: roleUser, Subject: "other", Namespace: "other"}, 403},
		{"old-instance", grafanaTestBody(strings.Replace(queries[0].Query, string(owned.UID), "old-instance", 1)), p, 403},
		{"query-union", grafanaTestBody(queries[0].Query + " or " + foreignQuery), p, 403},
		{"query-comment", grafanaTestBody(queries[0].Query + " # " + foreignQuery), p, 403},
		{"regex-broaden", grafanaTestBody(strings.Replace(queries[0].Query, `server="demo"`, `server=~".+"`, 1)), p, 403},
		{"missing-uid", grafanaTestBody(strings.Replace(queries[0].Query, `,server_uid="`+string(owned.UID)+`"`, "", 1)), p, 403},
		{"other-datasource", strings.Replace(grafanaTestBody(queries[0].Query), `"uid":"prometheus"`, `"uid":"loki"`, 1), p, 403},
		{"expression-datasource", strings.Replace(grafanaTestBody(queries[0].Query), `"uid":"prometheus"`, `"uid":"__expr__"`, 1), p, 403},
		{"empty-expr", grafanaTestBody(""), p, 403},
		{"malformed", "{", p, 400},
		{"two-json-values", grafanaTestBody(queries[0].Query) + `{}`, p, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := requests
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/observability/grafana/api/ds/query?orgId=2&ds_type=loki", strings.NewReader(tc.body))
			r = r.WithContext(withPrincipal(r.Context(), tc.p))
			r.Header.Set("X-WEBAUTH-USER", "admin")
			r.Header.Set("X-WEBAUTH-ROLE", "Admin")
			r.Header.Set("X-Grafana-Org-Id", "2")
			r.Header.Set("Cookie", "grafana_session=forged")
			r.Header.Set("Authorization", "Bearer foreign")
			w := httptest.NewRecorder()
			server.HandleRuntimeGrafana(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want != 200 && requests != before {
				t.Fatal("rejected query reached shared Grafana datasource")
			}
			if w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "admin-secret") {
				t.Fatal("Grafana login state escaped the platform session boundary")
			}
		})
	}
	// Authorize every target in a batch; accepting the first must not permit
	// the second to select another shared-catalog owner.
	var batch grafanaQueryRequest
	if err := json.Unmarshal([]byte(grafanaTestBody(queries[0].Query)), &batch); err != nil {
		t.Fatal(err)
	}
	second := batch.Queries[0]
	second.RefID, second.Expr = "B", foreignQuery
	batch.Queries = append(batch.Queries, second)
	data, _ := json.Marshal(batch)
	r := httptest.NewRequest("POST", "/api/v1/runtime/observability/grafana/api/ds/query", bytes.NewReader(data))
	r = r.WithContext(withPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	server.HandleRuntimeGrafana(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign second batch target accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestTenantGrafanaRejectsAlternateDataRoutes(t *testing.T) {
	t.Setenv("GRAFANA_SCOPED_USER_ACCESS", "true")
	server := newRuntimeServerWithMCPServers(t, ownedTestMCPServer("demo", "mcp-servers", "alice"))
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("alternate data route reached shared Grafana") }))
	defer upstream.Close()
	t.Setenv("GRAFANA_UPSTREAM", upstream.URL)
	for _, suffix := range []string{
		"/api/datasources/proxy/1/api/v1/query", "/api/datasources/proxy/uid/prometheus/api/v1/query", "/api/datasources/uid/prometheus/resources/api/v1/query",
		"/api/plugins/prometheus/resources/api/v1/query", "/api/plugins/loki/resources/label", "/api/tsdb/query", "/api/ds/query",
		"/api/live/ws", "/api/live/push/foreign", "/api/snapshots", "/api/public/dashboards/foreign", "/api/dashboards/uid/private", "/render/d/private", "/api/query-history",
		"/api/datasources/uid/loki", "/api/datasources/uid/tempo", "/api/admin/settings", "/api/datasources/../ds/query", "/api/plugins/foreign/settings",
	} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			if suffix == "/api/ds/query" && method == "POST" {
				continue // tested separately with forged payloads
			}
			r := httptest.NewRequest(method, "/api/v1/runtime/observability/grafana"+suffix, nil)
			r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleUser, Subject: "alice"}))
			w := httptest.NewRecorder()
			server.HandleRuntimeGrafana(w, r)
			if w.Code != 403 && w.Code != 404 {
				t.Errorf("%s %s status=%d", method, suffix, w.Code)
			}
		}
	}
}

func TestGrafanaRechecksOwnershipAndAdminAccess(t *testing.T) {
	t.Setenv("GRAFANA_SCOPED_USER_ACCESS", "true")
	owned := ownedTestMCPServer("demo", "mcp-servers", "alice")
	query := scopedPrometheusQueries(owned.Namespace, owned.Name, string(owned.UID))[0].Query
	server := newRuntimeServerWithMCPServers(t, owned)
	// A subsequent request resolves the live MCPServer after ownership changes.
	owned.Labels[platformUserIDLabel] = "bob"
	server = newRuntimeServerWithMCPServers(t, owned)
	r := httptest.NewRequest("POST", "/api/v1/runtime/observability/grafana/api/ds/query", strings.NewReader(grafanaTestBody(query)))
	r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleUser, Subject: "alice", AllowedNamespaces: []string{"mcp-servers"}}))
	w := httptest.NewRecorder()
	server.HandleRuntimeGrafana(w, r)
	if w.Code != 403 {
		t.Fatalf("revoked owner still has access: %d", w.Code)
	}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-WEBAUTH-ROLE") != "Admin" || r.URL.Path != "/grafana/api/datasources/proxy/uid/prometheus/api/v1/query" || r.URL.Query().Get("query") != "up" {
			t.Errorf("admin was unexpectedly scoped: %s %v", r.URL, r.Header)
		}
		fmt.Fprint(w, `{"status":"success"}`)
	}))
	defer admin.Close()
	t.Setenv("GRAFANA_UPSTREAM", admin.URL)
	r = httptest.NewRequest("GET", "/api/v1/runtime/observability/grafana/api/datasources/proxy/uid/prometheus/api/v1/query?query=up", nil)
	r = r.WithContext(withPrincipal(r.Context(), principal{Role: roleAdmin, Subject: "admin"}))
	w = httptest.NewRecorder()
	server.HandleRuntimeGrafana(w, r)
	if w.Code != 200 {
		t.Fatalf("admin access: %d %s", w.Code, w.Body.String())
	}
}
