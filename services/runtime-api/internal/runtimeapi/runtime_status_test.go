package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/k8sclient"
)

func TestLatestServerSessionFromEvents(t *testing.T) {
	usedAt := time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	got, state := latestServerSessionFromEvents("team-a", []clickhouse.EventRow{
		{Timestamp: usedAt, SessionID: "session-new", ToolName: "lookup"},
		{Timestamp: usedAt.Add(-time.Minute), SessionID: "session-old", ToolName: "search"},
	})
	if state != "observed" || got == nil {
		t.Fatalf("latestServerSessionFromEvents() = (%#v, %q), want observed session", got, state)
	}
	if got.ID != "session-new" || got.Namespace != "team-a" || got.ToolName != "lookup" || got.UsedAt != usedAt.Format(time.RFC3339Nano) {
		t.Fatalf("last session = %#v, want newest event summary", got)
	}
	if session, state := latestServerSessionFromEvents("team-a", []clickhouse.EventRow{{Timestamp: usedAt}}); session != nil || state != "none" {
		t.Fatalf("empty session history = (%#v, %q), want no session", session, state)
	}
}

func TestRuntimeServerGetExposesRuntimeStatus(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	serverObject := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "mcp-team-a", UID: "server-uid"},
		Spec:       mcpv1alpha1.MCPServerSpec{PublicPathPrefix: "demo"},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: "Ready",
			LastError: &mcpv1alpha1.MCPServerErrorEvent{
				Reason: "DeploymentNotReady", Message: "container=server reason=CrashLoopBackOff",
				ObservedAt: metav1.NewTime(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)),
			},
		},
	}
	api := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Dynamic:   dynamicfake.NewSimpleDynamicClient(scheme, serverObject),
			Clientset: kubernetesfake.NewSimpleClientset(),
		},
		liveInventoryProbe: staticLiveInventoryProber{inventory: &liveInventory{
			FetchedAt: time.Date(2026, 10, 16, 12, 15, 0, 0, time.UTC),
		}},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/servers/mcp-team-a/demo", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
	recorder := httptest.NewRecorder()
	api.HandleRuntimeServerItem(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Server serverInfo `json:"server"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Server.RuntimeStatus.Transport != "streamable-http" || payload.Server.RuntimeStatus.ConnectionState != "ready" {
		t.Fatalf("runtime status transport/connection = %q/%q", payload.Server.RuntimeStatus.Transport, payload.Server.RuntimeStatus.ConnectionState)
	}
	if payload.Server.RuntimeStatus.LastError == nil || payload.Server.RuntimeStatus.LastError.Reason != "DeploymentNotReady" {
		t.Fatalf("runtime status lastError = %#v", payload.Server.RuntimeStatus.LastError)
	}
	if payload.Server.RuntimeStatus.LastSessionState != "unavailable" {
		t.Fatalf("lastSessionState = %q, want unavailable without analytics store", payload.Server.RuntimeStatus.LastSessionState)
	}
	if _, err := api.controlPlane().GetServer(context.Background(), "mcp-team-a", "demo"); err != nil {
		t.Fatalf("server was unexpectedly mutated: %v", err)
	}
}
