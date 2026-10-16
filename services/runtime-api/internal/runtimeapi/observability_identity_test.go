package runtimeapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestRuntimeObservabilityLogsExcludePreviousOwnerAfterNameReuse(t *testing.T) {
	created := time.Now().Add(-time.Minute)
	old := created.Add(-time.Minute).UnixNano()
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), `server_uid="new-instance"`) {
			t.Errorf("query omitted the current server identity: %s", r.URL.Query().Get("query"))
		}
		start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		if start <= old && r.URL.Query().Get("query") == `{namespace="mcp-servers",app="demo"}` {
			fmt.Fprintf(w, `{"status":"success","data":{"result":[{"stream":{"namespace":"mcp-servers","app":"demo"},"values":[["%d","previous-owner-private-log"]]}]}}`, old)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"result":[]}}`)
	}))
	defer loki.Close()
	t.Setenv(envLokiAPIURL, loki.URL)
	target := ownedTestMCPServer("demo", "mcp-servers", "new-owner")
	target.UID = types.UID("new-instance")
	target.CreationTimestamp = metav1.NewTime(created)
	server := newRuntimeServerWithMCPServers(t, target)
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/observability/logs/query?namespace=mcp-servers&server=demo", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{
		Role: roleUser, Subject: "new-owner", Namespace: "new-owner",
		AllowedNamespaces: []string{"new-owner", "mcp-servers"},
	}))
	recorder := httptest.NewRecorder()
	server.HandleRuntimeObservabilityLogs(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "previous-owner-private-log") {
		t.Fatalf("new server owner received its predecessor's logs: %s", recorder.Body.String())
	}
}

func TestRuntimeObservabilityRejectsMissingServerUIDBeforeQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server without an immutable identity reached the telemetry store")
	}))
	defer upstream.Close()
	t.Setenv(envLokiAPIURL, upstream.URL)
	t.Setenv(envMCPPrometheusAPIURL, upstream.URL)
	for _, tc := range []struct {
		name    string
		handler func(*RuntimeServer, http.ResponseWriter, *http.Request)
	}{
		{"logs", (*RuntimeServer).HandleRuntimeObservabilityLogs},
		{"metrics", (*RuntimeServer).HandleRuntimeObservabilityMetrics},
		{"prometheus", (*RuntimeServer).HandleRuntimeObservabilityPrometheusQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := ownedTestMCPServer("demo", "user-1", "user-1")
			target.UID = ""
			server := newRuntimeServerWithMCPServers(t, target)
			request := httptest.NewRequest(http.MethodGet, "/?namespace=user-1&server=demo", nil)
			request = request.WithContext(withPrincipal(request.Context(), principal{
				Role: roleUser, Subject: "user-1", Namespace: "user-1",
				AllowedNamespaces: []string{"user-1"},
			}))
			recorder := httptest.NewRecorder()
			tc.handler(server, recorder, request)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
