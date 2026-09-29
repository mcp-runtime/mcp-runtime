package controlplane

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/k8sclient"
)

func TestListServersProjectsMCPServerInventoryAndDeploymentStatus(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	replicas := int32(2)
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "demo-one",
			Namespace:         "mcp-servers",
			CreationTimestamp: metav1.Now(),
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Description:      "Demo server",
			PublicPathPrefix: "demo-one",
			Prompts:          []mcpv1alpha1.InventoryItem{{Name: "summarize"}},
		},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-one",
			Namespace: "mcp-servers",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":     "mcp-runtime",
				"mcpruntime.org/rollout-track":     "stable",
				"app.kubernetes.io/part-of":        "test",
				"app.kubernetes.io/component-test": "server",
			},
		},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	mgr := New(&k8sclient.Clients{
		Dynamic:   fake.NewSimpleDynamicClient(scheme, server),
		Clientset: kubernetesfake.NewSimpleClientset(deployment),
	})

	result, err := mgr.ListServers(context.Background(), "mcp-servers")
	if err != nil {
		t.Fatalf("ListServers returned error: %v", err)
	}
	if result.UsedDeploymentFallback {
		t.Fatal("ListServers used deployment fallback unexpectedly")
	}
	if len(result.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(result.Servers))
	}
	got := result.Servers[0]
	if got.Name != "demo-one" || got.Ready != "1/2" || got.Status != "Degraded" {
		t.Fatalf("server summary = %#v", got)
	}
	if got.Endpoint != "/demo-one/mcp" {
		t.Fatalf("endpoint = %q, want /demo-one/mcp", got.Endpoint)
	}
	if len(got.Prompts) != 1 || got.Prompts[0].Name != "summarize" {
		t.Fatalf("prompts = %#v", got.Prompts)
	}
}

func TestListServersEmptyCatalogDoesNotRequireDeploymentList(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	clientset := kubernetesfake.NewSimpleClientset()
	clientset.PrependReactor("list", "deployments", func(kubetesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
	})
	mgr := New(&k8sclient.Clients{
		Dynamic:   fake.NewSimpleDynamicClient(scheme),
		Clientset: clientset,
	})

	result, err := mgr.ListServers(context.Background(), "mcp-servers")
	if err != nil {
		t.Fatalf("ListServers returned error for empty catalog: %v", err)
	}
	if len(result.Servers) != 0 {
		t.Fatalf("servers = %#v, want empty", result.Servers)
	}
}

func TestListServersWithOptionsFiltersMCPServersByLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	owned := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned",
			Namespace: "mcp-servers",
			Labels: map[string]string{
				"platform.mcpruntime.org/user-id": "user-1",
			},
		},
		Spec: mcpv1alpha1.MCPServerSpec{Image: "demo:latest"},
	}
	other := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other",
			Namespace: "mcp-servers",
			Labels: map[string]string{
				"platform.mcpruntime.org/user-id": "user-2",
			},
		},
		Spec: mcpv1alpha1.MCPServerSpec{Image: "demo:latest"},
	}
	mgr := New(&k8sclient.Clients{
		Dynamic:   fake.NewSimpleDynamicClient(scheme, owned, other),
		Clientset: kubernetesfake.NewSimpleClientset(),
	})

	result, err := mgr.ListServersWithOptions(context.Background(), "mcp-servers", ListServersOptions{
		LabelSelector:        "platform.mcpruntime.org/user-id=user-1",
		SkipDeploymentStatus: true,
	})
	if err != nil {
		t.Fatalf("ListServersWithOptions returned error: %v", err)
	}
	if len(result.Servers) != 1 || result.Servers[0].Name != "owned" {
		t.Fatalf("servers = %#v, want only owned", result.Servers)
	}
}

func TestListServersFallsBackToDeploymentsWhenMCPServerListFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "legacy-demo",
			Namespace:         "mcp-servers",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mcp-runtime",
				"mcpruntime.org/rollout-track": "stable",
			},
		},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		MCPServerGVR: "MCPServerList",
	})
	dynamicClient.PrependReactor("list", "mcpservers", func(action kubetesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: mcpv1alpha1.Group, Resource: mcpv1alpha1.MCPServerResource}, "")
	})
	mgr := New(&k8sclient.Clients{
		Dynamic:   dynamicClient,
		Clientset: kubernetesfake.NewSimpleClientset(deployment),
	})

	result, err := mgr.ListServers(context.Background(), "mcp-servers")
	if err != nil {
		t.Fatalf("ListServers returned error: %v", err)
	}
	if !result.UsedDeploymentFallback || result.CRDError == nil {
		t.Fatalf("fallback metadata = %#v", result)
	}
	if len(result.Servers) != 1 || result.Servers[0].Name != "legacy-demo" || result.Servers[0].Status != "Ready" {
		t.Fatalf("servers = %#v", result.Servers)
	}
}

func TestApplyServerCreatesAndUpdatesMCPServer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	mgr := New(&k8sclient.Clients{
		Dynamic:   fake.NewSimpleDynamicClient(scheme),
		Clientset: kubernetesfake.NewSimpleClientset(),
	})

	created, err := mgr.ApplyServer(context.Background(), &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{APIVersion: mcpv1alpha1.GroupVersion.String(), Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "mcp-servers",
		},
		Spec: mcpv1alpha1.MCPServerSpec{Image: "registry.example.com/core/demo", Description: "before"},
	})
	if err != nil {
		t.Fatalf("ApplyServer create returned error: %v", err)
	}
	if created.Spec.Description != "before" {
		t.Fatalf("created description = %q", created.Spec.Description)
	}

	updated, err := mgr.ApplyServer(context.Background(), &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{APIVersion: mcpv1alpha1.GroupVersion.String(), Kind: "MCPServer"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "mcp-servers",
		},
		Spec: mcpv1alpha1.MCPServerSpec{Image: "registry.example.com/core/demo", Description: "after"},
	})
	if err != nil {
		t.Fatalf("ApplyServer update returned error: %v", err)
	}
	if updated.Spec.Description != "after" {
		t.Fatalf("updated description = %q", updated.Spec.Description)
	}
}

func TestPublicMCPEndpointHonorsPlatformDomain(t *testing.T) {
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")

	got := PublicMCPEndpoint(mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-one", Namespace: "mcp-servers"},
	})
	if got != "https://mcp.example.com/demo-one/mcp" {
		t.Fatalf("endpoint = %q, want platform domain MCP URL", got)
	}
}

func TestPublicMCPEndpointPrefersOperatorPublishedURL(t *testing.T) {
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")

	got := PublicMCPEndpoint(mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-one", Namespace: "mcp-servers"},
		Status:     mcpv1alpha1.MCPServerStatus{URL: "http://mcp.internal.example/demo-one/mcp"},
	})
	if got != "http://mcp.internal.example/demo-one/mcp" {
		t.Fatalf("endpoint = %q, want the operator-published status.url", got)
	}
}

// The ingress routes on publicPathPrefix when it is set, so the fallback must
// resolve the path the same way instead of preferring spec.ingressPath.
func TestPublicMCPEndpointFallbackFollowsIngressPathPrecedence(t *testing.T) {
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")

	got := PublicMCPEndpoint(mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-one", Namespace: "mcp-servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			IngressPath:      "/demo-one/mcp",
			PublicPathPrefix: "tools/demo",
		},
	})
	if got != "https://mcp.example.com/tools/demo/mcp" {
		t.Fatalf("endpoint = %q, want the publicPathPrefix route", got)
	}
}

func TestMCPServerGVR(t *testing.T) {
	want := schema.GroupVersionResource{Group: "mcpruntime.org", Version: "v1alpha1", Resource: "mcpservers"}
	if MCPServerGVR != want {
		t.Fatalf("MCPServerGVR = %#v, want %#v", MCPServerGVR, want)
	}
}

func TestServerInfoProjectsGatewayEnabled(t *testing.T) {
	tests := []struct {
		name    string
		gateway *mcpv1alpha1.GatewayConfig
		want    bool
	}{
		{name: "omitted", want: true},
		{name: "empty", gateway: &mcpv1alpha1.GatewayConfig{}, want: true},
		{name: "disabled", gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(false)}},
		{name: "enabled", gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServerInfoFromMCPServer(mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team-demo"},
				Spec:       mcpv1alpha1.MCPServerSpec{Gateway: tt.gateway},
			}, ServerDeploymentStatus{})
			if got.GatewayEnabled != tt.want {
				t.Fatalf("GatewayEnabled = %t, want %t", got.GatewayEnabled, tt.want)
			}
		})
	}
}

func TestServerInfoStatusWaitsForOperatorPhaseWhenGatewayEnabled(t *testing.T) {
	deploymentReady := ServerDeploymentStatus{Ready: "1/1", Status: "Ready"}
	tests := []struct {
		name    string
		gateway *mcpv1alpha1.GatewayConfig
		phase   string
		want    string
	}{
		{name: "gateway on, phase Ready", gateway: &mcpv1alpha1.GatewayConfig{}, phase: "Ready", want: "Ready"},
		{name: "gateway on, phase PartiallyReady", gateway: &mcpv1alpha1.GatewayConfig{}, phase: "PartiallyReady", want: "PartiallyReady"},
		{name: "gateway on, phase empty", gateway: &mcpv1alpha1.GatewayConfig{}, phase: "", want: "Pending"},
		{name: "gateway off, phase empty", gateway: &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(false)}, phase: "", want: "Ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServerInfoFromMCPServer(mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "team-demo"},
				Spec:       mcpv1alpha1.MCPServerSpec{Gateway: tt.gateway},
				Status:     mcpv1alpha1.MCPServerStatus{Phase: tt.phase},
			}, deploymentReady)
			if got.Status != tt.want {
				t.Fatalf("Status = %q, want %q", got.Status, tt.want)
			}
			if got.Ready != "1/1" {
				t.Fatalf("Ready = %q, want 1/1", got.Ready)
			}
		})
	}
}
