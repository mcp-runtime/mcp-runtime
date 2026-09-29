package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
)

func TestMCPLiveInventoryProberFetchesLists(t *testing.T) {
	var sawProtocol atomic.Bool
	fakeMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(mcpProtocolHeader) == liveInventoryProtocolVersion {
			sawProtocol.Store(true)
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.Header().Set(mcpSessionHeader, "probe-session")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": liveInventoryProtocolVersion,
					"capabilities": map[string]any{
						"tools":     map[string]any{},
						"prompts":   map[string]any{},
						"resources": map[string]any{},
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "live-add",
						"description": "Add numbers",
						"inputSchema": map[string]any{"type": "object"},
					}},
				},
			})
		case "prompts/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"prompts": []map[string]any{{
						"name":        "summarize",
						"description": "Summarize text",
						"arguments": []map[string]any{{
							"name":     "text",
							"required": true,
						}},
					}},
				},
			})
		case "resources/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"resources": []map[string]any{{
						"uri":      "repo://README.md",
						"name":     "README",
						"mimeType": "text/markdown",
					}},
				},
			})
		default:
			t.Errorf("unexpected method %q", req.Method)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeMCP.Close()

	prober := &mcpLiveInventoryProber{
		client: fakeMCP.Client(),
		baseURLForServer: func(controlplane.ServerInfo) string {
			return fakeMCP.URL
		},
		now: func() time.Time {
			return time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC)
		},
	}
	got, err := prober.probe(context.Background(), controlplane.ServerInfo{
		Name: "demo", Namespace: "mcp-servers",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !sawProtocol.Load() {
		t.Fatal("probe did not send MCP protocol header")
	}
	if got.ProtocolVersion != liveInventoryProtocolVersion {
		t.Fatalf("protocolVersion = %q", got.ProtocolVersion)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "live-add" || len(got.Tools[0].InputSchema) == 0 {
		t.Fatalf("tools = %#v", got.Tools)
	}
	if len(got.Prompts) != 1 || got.Prompts[0].Arguments[0].Name != "text" {
		t.Fatalf("prompts = %#v", got.Prompts)
	}
	if len(got.Resources) != 1 || got.Resources[0].URI != "repo://README.md" {
		t.Fatalf("resources = %#v", got.Resources)
	}
	if got.FetchedAt.Format(time.RFC3339) != "2026-05-20T01:02:03Z" {
		t.Fatalf("fetchedAt = %s", got.FetchedAt.Format(time.RFC3339))
	}
}

func TestRuntimeServersReturnsLiveInventoryAfterAsyncProbe(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	srv := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-one",
			Namespace:  "mcp-servers",
			UID:        "server-uid-1",
			Generation: 7,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "demo:latest",
			ServicePort: 80,
			Tools: []mcpv1alpha1.ToolConfig{{
				Name:          "declared-only",
				RequiredTrust: mcpv1alpha1.TrustLevelHigh,
				SideEffect:    mcpv1alpha1.ToolSideEffectRead,
			}},
		},
	}
	fakeMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.Header().Set(mcpSessionHeader, "probe-session")
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result["protocolVersion"] = liveInventoryProtocolVersion
			result["capabilities"] = map[string]any{
				"tools":     map[string]any{},
				"prompts":   map[string]any{},
				"resources": map[string]any{},
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result["tools"] = []map[string]any{{"name": "live-add", "description": "Add numbers"}}
		case "prompts/list":
			result["prompts"] = []map[string]any{{"name": "summarize"}}
		case "resources/list":
			result["resources"] = []map[string]any{{"uri": "repo://README.md", "name": "README"}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
	}))
	defer fakeMCP.Close()

	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Dynamic:   dynamicfake.NewSimpleDynamicClient(scheme, srv),
			Clientset: kubernetesfake.NewSimpleClientset(),
		},
		liveInventoryProbe: &mcpLiveInventoryProber{
			client: fakeMCP.Client(),
			baseURLForServer: func(controlplane.ServerInfo) string {
				return fakeMCP.URL
			},
		},
	}

	first := requestRuntimeServers(t, server)
	if len(first.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(first.Servers))
	}
	if first.Servers[0].LiveInventory != nil {
		t.Fatalf("first response liveInventory = %#v, want nil cache miss", first.Servers[0].LiveInventory)
	}
	if first.Servers[0].LiveInventoryError != "live inventory pending" {
		t.Fatalf("liveInventoryError = %q", first.Servers[0].LiveInventoryError)
	}

	var got struct {
		Servers []serverInfo `json:"servers"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got = requestRuntimeServers(t, server)
		if len(got.Servers) == 1 && got.Servers[0].LiveInventory != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got.Servers) != 1 || got.Servers[0].LiveInventory == nil {
		t.Fatalf("live inventory was not cached: %#v", got.Servers)
	}
	info := got.Servers[0]
	if len(info.Tools) != 1 || info.Tools[0].Name != "declared-only" {
		t.Fatalf("CRD tools = %#v", info.Tools)
	}
	if len(info.LiveInventory.Tools) != 1 || info.LiveInventory.Tools[0].Name != "live-add" {
		t.Fatalf("live tools = %#v", info.LiveInventory.Tools)
	}
	if len(info.LiveInventory.Prompts) != 1 || info.LiveInventory.Prompts[0].Name != "summarize" {
		t.Fatalf("live prompts = %#v", info.LiveInventory.Prompts)
	}
	if len(info.LiveInventory.Resources) != 1 || info.LiveInventory.Resources[0].URI != "repo://README.md" {
		t.Fatalf("live resources = %#v", info.LiveInventory.Resources)
	}
}

func TestRuntimeServerGetReturnsSingleServerShape(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	srv := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-one", Namespace: "mcp-servers"},
		Spec:       mcpv1alpha1.MCPServerSpec{Image: "demo:latest", ServicePort: 80},
		// Gateway defaults on; control-plane Status stays non-Ready until the
		// operator phase is Ready (mTLS IngressRoute/Secrets included).
		Status: mcpv1alpha1.MCPServerStatus{Phase: "Ready"},
	}
	replicas := int32(2)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-one", Namespace: "mcp-servers"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Dynamic:   dynamicfake.NewSimpleDynamicClient(scheme, srv),
			Clientset: kubernetesfake.NewSimpleClientset(deployment),
		},
		liveInventoryProbe: &countingLiveInventoryProber{},
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/servers/mcp-servers/demo-one", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
	server.HandleRuntimeServerItem(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Server serverInfo `json:"server"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Server.Name != "demo-one" || payload.Server.Namespace != "mcp-servers" {
		t.Fatalf("server = %#v", payload.Server)
	}
	if payload.Server.Ready != "2/2" || payload.Server.Status != "Ready" {
		t.Fatalf("readiness = (%q, %q), want (2/2, Ready)", payload.Server.Ready, payload.Server.Status)
	}
	if payload.Server.LiveInventory != nil || payload.Server.LiveInventoryError != "live inventory pending" {
		t.Fatalf("live inventory = (%#v, %q), want pending", payload.Server.LiveInventory, payload.Server.LiveInventoryError)
	}
}

func TestMCPLiveInventoryProberEndpointPrefersPublicEndpoint(t *testing.T) {
	prober := &mcpLiveInventoryProber{}
	got, err := prober.endpoint(controlplane.ServerInfo{
		Name:        "acme-tools",
		Namespace:   "mcp-team-acme",
		Endpoint:    "https://mcp.mcpruntime.org/acme-tools/mcp",
		ServicePort: 80,
	})
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if got != "https://mcp.mcpruntime.org/acme-tools/mcp" {
		t.Fatalf("endpoint = %q, want public endpoint", got)
	}
}

func TestMCPLiveInventoryProberEndpointFallsBackForLocalhostEndpoint(t *testing.T) {
	prober := &mcpLiveInventoryProber{}
	got, err := prober.endpoint(controlplane.ServerInfo{
		Name:        "demo",
		Namespace:   "mcp-servers",
		Endpoint:    "http://localhost:18080/demo/mcp",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if got != "http://demo.mcp-servers.svc.cluster.local:8080/demo/mcp" {
		t.Fatalf("endpoint = %q, want service fallback", got)
	}
}

func TestLiveInventoryCacheInvalidatesOnGenerationBump(t *testing.T) {
	prober := &countingLiveInventoryProber{}
	cache := newLiveInventoryCache(time.Minute, prober)
	info := controlplane.ServerInfo{Name: "demo", Namespace: "mcp-servers", UID: "uid-1", Generation: 1}
	if inv, reason := cache.getOrStart(context.Background(), info); inv != nil || reason != "live inventory pending" {
		t.Fatalf("first get = (%#v, %q), want pending miss", inv, reason)
	}
	waitForCachedInventory(t, cache, info)
	if got := prober.calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	info.Generation = 2
	if inv, reason := cache.getOrStart(context.Background(), info); inv != nil || reason != "live inventory pending" {
		t.Fatalf("generation bump get = (%#v, %q), want pending miss", inv, reason)
	}
	waitForCachedInventory(t, cache, info)
	if got := prober.calls.Load(); got != 2 {
		t.Fatalf("probe calls = %d, want 2 after generation bump", got)
	}
}

func TestLiveInventoryCacheInvalidatesOnUIDChange(t *testing.T) {
	prober := &countingLiveInventoryProber{}
	cache := newLiveInventoryCache(time.Minute, prober)
	info := controlplane.ServerInfo{Name: "demo", Namespace: "mcp-servers", UID: "uid-1", Generation: 1}
	if inv, reason := cache.getOrStart(context.Background(), info); inv != nil || reason != "live inventory pending" {
		t.Fatalf("first get = (%#v, %q), want pending miss", inv, reason)
	}
	waitForCachedInventory(t, cache, info)
	info.UID = "uid-2"
	if inv, reason := cache.getOrStart(context.Background(), info); inv != nil || reason != "live inventory pending" {
		t.Fatalf("uid change get = (%#v, %q), want pending miss", inv, reason)
	}
	waitForCachedInventory(t, cache, info)
	if got := prober.calls.Load(); got != 2 {
		t.Fatalf("probe calls = %d, want 2 after UID change", got)
	}
	if got := len(cache.entries); got != 1 {
		t.Fatalf("cache entries = %d, want old UID pruned", got)
	}
}

func TestLiveInventoryCacheBoundsEntries(t *testing.T) {
	prober := &countingLiveInventoryProber{}
	cache := newLiveInventoryCache(time.Minute, prober)
	cache.maxEntries = 2
	for i := 0; i < 3; i++ {
		suffix := string(rune('a' + i))
		info := controlplane.ServerInfo{Name: "demo-" + suffix, Namespace: "mcp-servers", UID: "uid-" + suffix, Generation: 1}
		if inv, reason := cache.getOrStart(context.Background(), info); inv != nil || reason != "live inventory pending" {
			t.Fatalf("get %d = (%#v, %q), want pending miss", i, inv, reason)
		}
		waitForCachedInventory(t, cache, info)
	}
	if got := len(cache.entries); got != 2 {
		t.Fatalf("cache entries = %d, want bounded to 2", got)
	}
}

func TestShortLiveInventoryReasonTruncatesUTF8Safely(t *testing.T) {
	msg := shortLiveInventoryReason(errors.New(strings.Repeat("é", 220)))
	if !utf8.ValidString(msg) {
		t.Fatalf("message is not valid UTF-8: %q", msg)
	}
	if got := len([]rune(msg)); got != 180 {
		t.Fatalf("rune length = %d, want 180", got)
	}
}

func TestMCPLiveInventoryProberSkipsUnsupportedCapabilities(t *testing.T) {
	var methods []string
	fakeMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		methods = append(methods, req.Method)
		w.Header().Set("content-type", "application/json")
		w.Header().Set(mcpSessionHeader, "probe-session")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": liveInventoryProtocolVersion,
					"capabilities": map[string]any{
						"tools": map[string]any{},
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"tools": []map[string]any{{"name": "live-add"}}},
			})
		default:
			t.Errorf("unexpected method %q", req.Method)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeMCP.Close()

	prober := &mcpLiveInventoryProber{
		client: fakeMCP.Client(),
		baseURLForServer: func(controlplane.ServerInfo) string {
			return fakeMCP.URL
		},
	}
	got, err := prober.probe(context.Background(), controlplane.ServerInfo{Name: "demo", Namespace: "mcp-servers"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "live-add" {
		t.Fatalf("tools = %#v", got.Tools)
	}
	if len(got.Prompts) != 0 || len(got.Resources) != 0 {
		t.Fatalf("unsupported inventory = prompts %#v resources %#v", got.Prompts, got.Resources)
	}
	for _, method := range methods {
		if method == "prompts/list" || method == "resources/list" {
			t.Fatalf("called unsupported method %q; all methods: %#v", method, methods)
		}
	}
}

type countingLiveInventoryProber struct {
	calls atomic.Int32
}

func (p *countingLiveInventoryProber) probe(context.Context, controlplane.ServerInfo) (*liveInventory, error) {
	call := p.calls.Add(1)
	return &liveInventory{
		FetchedAt:       time.Now().UTC(),
		ProtocolVersion: liveInventoryProtocolVersion,
		Tools:           []liveInventoryTool{{Name: "tool-" + string(rune('0'+call))}},
		Prompts:         []liveInventoryPrompt{},
		Resources:       []liveInventoryResource{},
	}, nil
}

func waitForCachedInventory(t *testing.T, cache *liveInventoryCache, info controlplane.ServerInfo) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if inv, _ := cache.getOrStart(context.Background(), info); inv != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for live inventory cache")
}

func requestRuntimeServers(t *testing.T, server *RuntimeServer) struct {
	Servers []serverInfo `json:"servers"`
} {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/servers?namespace=mcp-servers", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{Role: roleAdmin, Subject: "admin-1"}))
	server.HandleRuntimeServers(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Servers []serverInfo `json:"servers"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload
}
