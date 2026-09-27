package runtimeapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/sentinel"
)

type fakeAuditWriter struct {
	events []auditEvent
}

func (f *fakeAuditWriter) WriteAudit(_ context.Context, ev auditEvent) {
	f.events = append(f.events, ev)
}

func TestClientForPrincipalRequiresIdentityForUserRole(t *testing.T) {
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Clientset: kubernetesfake.NewSimpleClientset(),
		},
	}
	_, err := server.Deployments().clientForPrincipal(principal{
		Role:      roleUser,
		IsService: true,
	})
	if err == nil {
		t.Fatal("expected identity-required error")
	}
	if err != errPrincipalIdentityRequired {
		t.Fatalf("error = %v, want %v", err, errPrincipalIdentityRequired)
	}
}

func TestClientForPrincipalRejectsServiceAdminWithoutIdentity(t *testing.T) {
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Clientset: kubernetesfake.NewSimpleClientset(),
		},
	}
	_, err := server.Deployments().clientForPrincipal(principal{
		Role:      roleAdmin,
		IsService: true,
	})
	if err == nil {
		t.Fatal("expected identity-required error")
	}
	if err != errPrincipalIdentityRequired {
		t.Fatalf("error = %v, want %v", err, errPrincipalIdentityRequired)
	}
}

func TestClientForPrincipalUsesKubernetesImpersonation(t *testing.T) {
	var gotUser string
	var gotGroups []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("Impersonate-User")
		gotGroups = append([]string(nil), r.Header.Values("Impersonate-Group")...)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","metadata":{"resourceVersion":"1"},"items":[]}`))
	}))
	t.Cleanup(api.Close)

	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{
			Clientset: kubernetesfake.NewSimpleClientset(),
			Config:    &rest.Config{Host: api.URL},
		},
	}
	client, err := server.Deployments().clientForPrincipal(principal{
		Role:    roleUser,
		Subject: "user-123",
	})
	if err != nil {
		t.Fatalf("clientForPrincipal() error = %v", err)
	}
	if _, err := client.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("list pods with impersonated client: %v", err)
	}
	if gotUser != "platform:user:user-123" {
		t.Fatalf("Impersonate-User = %q, want %q", gotUser, "platform:user:user-123")
	}
	if !hasString(gotGroups, "platform:role:user") {
		t.Fatalf("Impersonate-Group values = %v, want platform:role:user", gotGroups)
	}
}

func TestEnsureDefaultDenyNetworkPolicyIncludesDNSEgress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "user-1"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("user-1").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if len(policy.Spec.Egress) == 0 {
		t.Fatalf("egress rules missing: %#v", policy.Spec)
	}
	foundDNS := false
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.NamespaceSelector == nil || peer.PodSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
				continue
			}
			if peer.PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
				continue
			}
			seen53 := map[int32]bool{}
			for _, port := range rule.Ports {
				if port.Port == nil {
					continue
				}
				if port.Port.Type == intstr.Int && port.Port.IntVal == 53 {
					seen53[53] = true
				}
			}
			if seen53[53] {
				foundDNS = true
			}
		}
	}
	if !foundDNS {
		t.Fatalf("expected kube-dns egress rule, got %#v", policy.Spec.Egress)
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsSentinelEgress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "user-1"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("user-1").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	foundSentinel := false
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "mcp-sentinel" {
				continue
			}
			seen := map[int32]bool{}
			for _, port := range rule.Ports {
				if port.Port != nil && port.Port.Type == intstr.Int {
					seen[port.Port.IntVal] = true
				}
			}
			if seen[sentinelIngestPort] && seen[sentinelOTLPPort] {
				foundSentinel = true
			}
		}
	}
	if !foundSentinel {
		t.Fatalf("expected sentinel egress rule, got %#v", policy.Spec.Egress)
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsRegistryEgress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-servers"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-servers").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !networkPolicyAllowsEgressToNamespacePort(policy, registryNamespace, registryPort) {
		t.Fatalf("expected registry egress rule, got %#v", policy.Spec.Egress)
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsSameNamespaceIngress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "user-1"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("user-1").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !networkPolicyAllowsSameNamespace(policy) {
		t.Fatalf("network policy does not allow same-namespace ingress: %#v", policy.Spec.Ingress)
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsSameNamespaceEgress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-servers"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-servers").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !networkPolicyAllowsSameNamespaceEgress(policy) {
		t.Fatalf("network policy does not allow same-namespace egress: %#v", policy.Spec.Egress)
	}
}

func TestEnsureDefaultDenyNetworkPolicyDoesNotAllowBroadHTTPEgress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "user-1"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("user-1").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	for _, rule := range policy.Spec.Egress {
		if len(rule.To) != 0 {
			continue
		}
		seen := map[int32]bool{}
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.Type == intstr.Int {
				seen[port.Port.IntVal] = true
			}
		}
		if seen[80] || seen[443] {
			t.Fatalf("found broad HTTP egress rule: %#v", rule)
		}
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsConfiguredIngressNamespace(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-servers-public", "kube-system"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-servers-public").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !networkPolicyAllowsNamespace(policy, "kube-system") {
		t.Fatalf("network policy does not allow ingress from kube-system: %#v", policy.Spec.Ingress)
	}
}

func TestManagedNamespaceHTTPSOnlyReachesConfiguredIngressController(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-default-deny", Namespace: "mcp-team-acme"},
		Spec:       desiredDefaultDenyNetworkPolicy("mcp-team-acme").Spec,
	})
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-team-acme", "kube-system", "traefik", " "); err != nil {
		t.Fatal(err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-team-acme").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, namespace string
		podLabels       map[string]string
		port            int
		want            bool
	}{
		{"k3s target HTTPS", "kube-system", map[string]string{"app.kubernetes.io/name": "traefik"}, 8443, true},
		{"k3s Service HTTPS", "kube-system", map[string]string{"app.kubernetes.io/name": "traefik"}, 443, true},
		{"bundled HTTPS", "traefik", map[string]string{"app": "traefik"}, 8443, true},
		{"unrelated system pod", "kube-system", map[string]string{"app.kubernetes.io/name": "unrelated"}, 8443, false},
		{"other namespace", "other", map[string]string{"app": "traefik"}, 8443, false},
		{"HTTP remains blocked", "kube-system", map[string]string{"app.kubernetes.io/name": "traefik"}, 8000, false},
		{"dashboard port remains blocked", "kube-system", map[string]string{"app.kubernetes.io/name": "traefik"}, 8080, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := false
			for _, rule := range policy.Spec.Egress {
				for _, port := range rule.Ports {
					if port.Port == nil || port.Port.IntVal != int32(tc.port) || port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
						continue
					}
					for _, peer := range rule.To {
						if peer.NamespaceSelector == nil || peer.PodSelector == nil {
							continue
						}
						ns, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
						if err != nil {
							t.Fatal(err)
						}
						pods, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
						if err != nil {
							t.Fatal(err)
						}
						allowed = allowed || ns.Matches(labels.Set{"kubernetes.io/metadata.name": tc.namespace}) && pods.Matches(labels.Set(tc.podLabels))
					}
				}
			}
			if allowed != tc.want {
				t.Fatalf("HTTPS policy allows destination = %t, want %t", allowed, tc.want)
			}
		})
	}
}

func TestEnsureDefaultDenyNetworkPolicyAllowsSentinelAPILiveInventoryIngress(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "mcp-team-acme", "kube-system"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() error = %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-team-acme").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !networkPolicyAllowsNamespace(policy, "mcp-sentinel") {
		t.Fatalf("network policy does not allow ingress from mcp-sentinel: %#v", policy.Spec.Ingress)
	}
}

func TestHandleDeploymentApplyAdminUsesRequestedNamespace(t *testing.T) {
	clearRegistryPullSecretEnv(t)
	client := kubernetesfake.NewSimpleClientset()
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/deployments", bytes.NewReader([]byte(`{
		"name": "demo-workload",
		"image": "registry.mcpruntime.org/mcp-servers/demo:latest",
		"namespace": "tenant-a",
		"replicas": 1,
		"port": 8088
	}`)))
	request = request.WithContext(withPrincipal(request.Context(), principal{
		Role:      roleAdmin,
		Subject:   "admin-1",
		Namespace: "admin-ns",
	}))
	recorder := httptest.NewRecorder()
	server.Deployments().handleDeploymentApply(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	ns, err := client.CoreV1().Namespaces().Get(context.Background(), "tenant-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("target namespace not ensured: %v", err)
	}
	for _, key := range []string{
		"pod-security.kubernetes.io/enforce",
		"pod-security.kubernetes.io/audit",
		"pod-security.kubernetes.io/warn",
	} {
		if ns.Labels[key] != "restricted" {
			t.Fatalf("namespace label %s = %q, want restricted", key, ns.Labels[key])
		}
	}
}

func TestResolveDeployImageReference(t *testing.T) {
	t.Setenv("MCP_REGISTRY_ENDPOINT", "10.96.223.152:5000")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
	t.Setenv("PLATFORM_MODE", "public")

	tests := []struct {
		name      string
		image     string
		namespace string
		teamSlug  string
		want      string
	}{
		{
			name:      "public short image",
			image:     "go-example",
			namespace: defaultPublicCatalogNamespace,
			want:      "10.96.223.152:5000/public/go-example",
		},
		{
			name:      "public scoped repository",
			image:     "public/go-example",
			namespace: defaultPublicCatalogNamespace,
			want:      "10.96.223.152:5000/public/go-example",
		},
		{
			name:      "team short image",
			image:     "go-example:v0.1.0",
			namespace: "mcp-team-acme",
			teamSlug:  "acme",
			want:      "10.96.223.152:5000/acme/go-example:v0.1.0",
		},
		{
			name:      "external explicit registry",
			image:     "registry.example.com/public/go-example:v0.1.0",
			namespace: defaultPublicCatalogNamespace,
			want:      "registry.example.com/public/go-example:v0.1.0",
		},
		{
			name:      "platform explicit public registry rewrites to internal endpoint",
			image:     "registry.mcpruntime.org/public/go-example:v0.1.0",
			namespace: defaultPublicCatalogNamespace,
			want:      "10.96.223.152:5000/public/go-example:v0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveDeployImageReference(tt.image, tt.namespace, tt.teamSlug); got != tt.want {
				t.Fatalf("ResolveDeployImageReference() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDeployImageReferenceFallsBackToInternalRegistryDNS(t *testing.T) {
	t.Setenv("MCP_REGISTRY_ENDPOINT", "")
	t.Setenv("PLATFORM_REGISTRY_URL", "registry.custom.example")
	t.Setenv("PLATFORM_MODE", "tenant")

	got := ResolveDeployImageReference("go-example:v0.1.0", "mcp-team-acme", "acme")
	if want := "registry.registry.svc.cluster.local:5000/acme/go-example:v0.1.0"; got != want {
		t.Fatalf("ResolveDeployImageReference() = %q, want %q", got, want)
	}
}

func TestResolveDeployImageReferencePrefersInternalEndpointOverPublicRegistryURL(t *testing.T) {
	t.Setenv("MCP_REGISTRY_ENDPOINT", "10.96.223.152:5000")
	t.Setenv("PLATFORM_REGISTRY_URL", "registry.custom.example")
	t.Setenv("PLATFORM_MODE", "tenant")

	got := ResolveDeployImageReference("go-example:v0.1.0", "mcp-team-acme", "acme")
	if want := "10.96.223.152:5000/acme/go-example:v0.1.0"; got != want {
		t.Fatalf("ResolveDeployImageReference() = %q, want %q", got, want)
	}
}

func TestResolveDeployImageReferencePrefersPullHostOverInternalEndpoint(t *testing.T) {
	t.Setenv("MCP_REGISTRY_ENDPOINT", "10.96.223.152:5000")
	t.Setenv("MCP_REGISTRY_PULL_HOST", "registry.registry.svc.cluster.local:5000")
	t.Setenv("PLATFORM_MODE", "tenant")

	got := ResolveDeployImageReference("go-example:v0.1.0", "mcp-team-acme", "acme")
	if want := "registry.registry.svc.cluster.local:5000/acme/go-example:v0.1.0"; got != want {
		t.Fatalf("ResolveDeployImageReference() = %q, want %q", got, want)
	}
}

// A tenant CLI that could not resolve the public registry wrote the bundled
// in-cluster registry DNS name into its metadata. On a platform whose pull
// host is a different registry (public k3s: MCP_REGISTRY_ENDPOINT is the
// public registry), kubelet cannot resolve that name, so the ref must be
// mapped onto the pull host rather than deployed as an ImagePullBackOff.
func TestResolveDeployImageReferenceMapsBundledRegistryHostToPullHost(t *testing.T) {
	t.Setenv("MCP_REGISTRY_PULL_HOST", "")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.mcpruntime.org")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
	t.Setenv("PLATFORM_MODE", "tenant")

	for _, image := range []string{
		"registry.registry.svc.cluster.local:5000/acme/workspace-demo:v1",
		"registry.registry.svc.cluster.local/acme/workspace-demo:v1",
		"registry.registry.svc:5000/acme/workspace-demo:v1",
	} {
		got := ResolveDeployImageReference(image, "mcp-team-acme", "acme")
		if want := "registry.mcpruntime.org/acme/workspace-demo:v1"; got != want {
			t.Fatalf("ResolveDeployImageReference(%q) = %q, want %q", image, got, want)
		}
	}
}

// Kind test-mode pulls the bundled registry DNS name through a containerd
// mirror, so when it is the pull host the ref must be left unchanged.
func TestResolveDeployImageReferenceKeepsBundledRegistryHostWhenItIsThePullHost(t *testing.T) {
	t.Setenv("MCP_REGISTRY_PULL_HOST", "")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.local")
	t.Setenv("PLATFORM_MODE", "tenant")

	image := "registry.registry.svc.cluster.local:5000/acme/workspace-demo:v1"
	if got := ResolveDeployImageReference(image, "mcp-team-acme", "acme"); got != image {
		t.Fatalf("ResolveDeployImageReference() = %q, want unchanged %q", got, image)
	}
	external := "ghcr.io/acme/workspace-demo:v1"
	if got := ResolveDeployImageReference(external, "mcp-team-acme", "acme"); got != external {
		t.Fatalf("ResolveDeployImageReference(external) = %q, want unchanged", got)
	}
}

func TestHandleDeploymentApplyRejectsInvalidVersionTag(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/deployments", bytes.NewReader([]byte(`{
		"name": "demo-workload",
		"image": "registry.mcpruntime.org/mcp-servers/demo",
		"version": "latest@sha256:abc",
		"namespace": "tenant-a",
		"replicas": 1,
		"port": 8088
	}`)))
	request = request.WithContext(withPrincipal(request.Context(), principal{
		Role:      roleAdmin,
		Subject:   "admin-1",
		Namespace: "admin-ns",
	}))
	recorder := httptest.NewRecorder()

	server.Deployments().handleDeploymentApply(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	wantBody := `{"error":"invalid_version_tag","message":"version must be a valid image tag"}`
	if strings.TrimSpace(recorder.Body.String()) != wantBody {
		t.Fatalf("body = %q, want %s", recorder.Body.String(), wantBody)
	}
}

func TestHandleDeploymentApplyWritesAuditEvent(t *testing.T) {
	clearRegistryPullSecretEnv(t)
	client := kubernetesfake.NewSimpleClientset()
	audit := &fakeAuditWriter{}
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
		audit:      audit,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/deployments", bytes.NewReader([]byte(`{
		"name": "demo-workload",
		"image": "registry.mcpruntime.org/team-a/demo:latest",
		"namespace": "team-a",
		"replicas": 1,
		"port": 8088
	}`)))
	request.Header.Set("x-mcp-source", "ui")
	request = request.WithContext(withPrincipal(request.Context(), principal{
		Role:      roleAdmin,
		Subject:   "admin-1",
		Email:     "admin@example.com",
		Namespace: "admin-ns",
		AuthType:  "platform_jwt",
	}))
	recorder := httptest.NewRecorder()
	server.Deployments().handleDeploymentApply(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(audit.events) == 0 {
		t.Fatal("expected audit event")
	}
	got := audit.events[len(audit.events)-1]
	if got.Action != "deployment_apply" || got.Status != "success" {
		t.Fatalf("audit event = %#v, want successful deployment_apply", got)
	}
	if got.UserID != "admin-1" || got.ImageRef != "registry.mcpruntime.org/team-a/demo:latest" || got.DeploymentTarget != "team-a/demo-workload" {
		t.Fatalf("audit event metadata = %#v", got)
	}
	if got.Source != "ui:platform_jwt" || got.AuthIdentity != "platform_jwt:admin@example.com" {
		t.Fatalf("audit identity = %#v", got)
	}
}

func TestEnsureNamespaceRegistryPullSecret(t *testing.T) {
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.local")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")
	t.Setenv("ADMIN_API_KEYS", "test-admin-key")
	client := kubernetesfake.NewSimpleClientset()
	if err := kubeworkload.EnsureServiceAccount(context.Background(), client, "mcp-team-acme"); err != nil {
		t.Fatalf("EnsureServiceAccount() error = %v", err)
	}
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ensureNamespaceRegistryPullSecret(context.Background(), client, "mcp-team-acme"); err != nil {
		t.Fatalf("ensureNamespaceRegistryPullSecret() error = %v", err)
	}
	secret, err := client.CoreV1().Secrets("mcp-team-acme").Get(context.Background(), registryPullSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("registry pull secret missing: %v", err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		t.Fatalf("secret type = %q, want %q", secret.Type, corev1.SecretTypeDockerConfigJson)
	}
	if !strings.Contains(string(secret.Data[corev1.DockerConfigJsonKey]), "registry.registry.svc.cluster.local:5000") {
		t.Fatalf("dockerconfig = %q, want internal registry host", string(secret.Data[corev1.DockerConfigJsonKey]))
	}
	sa, err := client.CoreV1().ServiceAccounts("mcp-team-acme").Get(context.Background(), kubeworkload.DefaultServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("workload service account missing: %v", err)
	}
	if len(sa.ImagePullSecrets) != 1 || sa.ImagePullSecrets[0].Name != registryPullSecretName {
		t.Fatalf("service account pull secrets = %#v", sa.ImagePullSecrets)
	}
}

func TestEnsureNamespaceRegistryPullSecretAllowsOptionalModeWithoutAPIKey(t *testing.T) {
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.example.com")
	t.Setenv("ADMIN_API_KEYS", "")
	t.Setenv("UI_API_KEY", "")
	t.Setenv("MCP_REGISTRY_PULL_SECRET_OPTIONAL", "true")
	client := kubernetesfake.NewSimpleClientset()
	if err := kubeworkload.EnsureServiceAccount(context.Background(), client, "mcp-team-acme"); err != nil {
		t.Fatalf("EnsureServiceAccount() error = %v", err)
	}
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ensureNamespaceRegistryPullSecret(context.Background(), client, "mcp-team-acme"); err != nil {
		t.Fatalf("ensureNamespaceRegistryPullSecret() optional mode error = %v", err)
	}
	secrets, err := client.CoreV1().Secrets("mcp-team-acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("expected no registry pull secret in optional mode, found %d", len(secrets.Items))
	}
}

func TestRegistryPullSecretHostPrefersInternalEndpointInDev(t *testing.T) {
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.local")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")
	if got := registryPullSecretHost(); got != "registry.registry.svc.cluster.local:5000" {
		t.Fatalf("registryPullSecretHost() = %q, want internal service endpoint", got)
	}
}

func TestTeamIngressAllowNamespacesUsesPlatformTraefikNamespaceWhenWatchDisabled(t *testing.T) {
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
	t.Setenv("PLATFORM_TRAEFIK_NAMESPACE", "kube-system")
	cfg := platformTeamTraefikWatchConfig()
	got := teamIngressAllowNamespaces(cfg)
	if len(got) != 1 || got[0] != "kube-system" {
		t.Fatalf("teamIngressAllowNamespaces() = %#v, want [kube-system]", got)
	}
}

func TestEnsureTeamNamespaceCreatesRegistryPullSecret(t *testing.T) {
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.local")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")
	t.Setenv("ADMIN_API_KEYS", "test-admin-key")
	client := kubernetesfake.NewSimpleClientset()
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ensureTeamNamespace(context.Background(), teamRecord{
		ID:        "team-acme-id",
		Slug:      "acme",
		Name:      "Acme",
		Namespace: "mcp-team-acme",
	}); err != nil {
		t.Fatalf("ensureTeamNamespace() error = %v", err)
	}
	if _, err := client.CoreV1().Secrets("mcp-team-acme").Get(context.Background(), registryPullSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("registry pull secret missing after ensureTeamNamespace: %v", err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-team-acme").Get(context.Background(), platformNamespaceAPISecretAccessName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace API secret access rolebinding missing after ensureTeamNamespace: %v", err)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != platformNamespaceAPISecretAccessName {
		t.Fatalf("namespace API secret access rolebinding ref = %#v", binding.RoleRef)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != rbacv1.ServiceAccountKind || binding.Subjects[0].Name != platformNamespaceAPIServiceAccountName || binding.Subjects[0].Namespace != sentinel.DefaultNamespace {
		t.Fatalf("namespace API secret access subjects = %#v", binding.Subjects)
	}
}

func TestEnsureTeamNamespaceWaitsForRegistrySecretRBAC(t *testing.T) {
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "disabled")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.example.org")
	t.Setenv("ADMIN_API_KEYS", "test-admin-key")
	client := kubernetesfake.NewSimpleClientset()
	attempts := 0
	client.PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "mcp-team-acme" {
			return false, nil, nil
		}
		attempts++
		if _, err := client.Tracker().Get(rbacv1.SchemeGroupVersion.WithResource("rolebindings"), "mcp-team-acme", platformNamespaceAPISecretAccessName); err != nil {
			t.Errorf("registry access attempted before RoleBinding: %v", err)
		}
		if attempts <= 2 {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, registryPullSecretName, errors.New("RBAC cache has not observed binding"))
		}
		return false, nil, nil
	})
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
	if err := server.Deployments().ensureTeamNamespace(context.Background(), teamRecord{ID: "team-acme-id", Slug: "acme", Namespace: "mcp-team-acme"}); err != nil {
		t.Fatalf("team provisioning failed during RBAC propagation: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("secret GET attempts = %d, want 3", attempts)
	}
	if _, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), "mcp-team-acme", registryPullSecretName); err != nil {
		t.Fatalf("pull secret not created: %v", err)
	}
	sa, err := client.CoreV1().ServiceAccounts("mcp-team-acme").Get(context.Background(), kubeworkload.DefaultServiceAccountName, metav1.GetOptions{})
	if err != nil || len(sa.ImagePullSecrets) != 1 || sa.ImagePullSecrets[0].Name != registryPullSecretName {
		t.Fatalf("pull secret not attached to workload account: %#v, %v", sa, err)
	}
}

func TestRegistrySecretRBACRetryStopsOnErrorOrCancellation(t *testing.T) {
	for _, forbidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("forbidden=%t", forbidden), func(t *testing.T) {
			t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.example.org")
			t.Setenv("ADMIN_API_KEYS", "test-admin-key")
			client := kubernetesfake.NewSimpleClientset()
			attempts := 0
			failure := errors.New("API unavailable")
			if forbidden {
				failure = apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, registryPullSecretName, errors.New("permanent denial"))
			}
			client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
				attempts++
				return true, nil, failure
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			server := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: client}}
			err := server.Deployments().ensureNamespaceRegistryPullSecretAfterBinding(ctx, client, "mcp-team-acme")
			if forbidden {
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "permanent denial") {
					t.Fatalf("error = %v, want deadline and original denial", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want immediate API failure", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1", attempts)
			}
		})
	}
}

func TestEnsureDefaultDenyNetworkPolicyIdempotent(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-default-deny", Namespace: "user-2"},
	})
	if err := ensureDefaultDenyNetworkPolicy(context.Background(), client, "user-2"); err != nil {
		t.Fatalf("ensureDefaultDenyNetworkPolicy() with existing policy returned %v", err)
	}
}

func TestEnsureTeamNamespaceConfiguresTraefikIngressWatch(t *testing.T) {
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "required")
	clearRegistryPullSecretEnv(t)
	client := kubernetesfake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "traefik",
							Args: []string{
								"--providers.kubernetesingress=true",
								"--providers.kubernetesingress.namespaces=registry,mcp-sentinel,mcp-servers",
							},
						}},
					},
				},
			},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "platform-default-deny", Namespace: "mcp-team-acme"},
		},
	)
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
	}
	if err := server.Deployments().ensureTeamNamespace(context.Background(), teamRecord{
		ID:        "team-acme-id",
		Slug:      "acme",
		Name:      "Acme",
		Namespace: "mcp-team-acme",
	}); err != nil {
		t.Fatalf("ensureTeamNamespace() error = %v", err)
	}
	if _, err := client.RbacV1().Roles("mcp-team-acme").Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{}); err != nil {
		t.Fatalf("traefik watch role missing: %v", err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-team-acme").Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik watch rolebinding missing: %v", err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != traefikWatchRoleName {
		t.Fatalf("traefik watch binding role ref = %#v", binding.RoleRef)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != rbacv1.ServiceAccountKind || binding.Subjects[0].Namespace != "traefik" || binding.Subjects[0].Name != "traefik" {
		t.Fatalf("traefik watch binding subjects = %#v", binding.Subjects)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-team-acme").Get(context.Background(), "platform-default-deny", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("network policy missing: %v", err)
	}
	if !networkPolicyAllowsNamespace(policy, "traefik") {
		t.Fatalf("network policy does not allow ingress from traefik: %#v", policy.Spec.Ingress)
	}
	deployment, err := client.AppsV1().Deployments("traefik").Get(context.Background(), "traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik deployment missing: %v", err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
	if !strings.Contains(args, "--providers.kubernetesingress.namespaces=registry,mcp-sentinel,mcp-servers,mcp-team-acme") {
		t.Fatalf("traefik namespace args = %q", args)
	}
}

func TestEnsureCatalogNamespaceAutoTraefikWatchSkipsExternalIngress(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	t.Setenv("PLATFORM_TEAM_TRAEFIK_WATCH", "auto")
	clearRegistryPullSecretEnv(t)
	client := kubernetesfake.NewSimpleClientset()
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
	}
	if err := server.Deployments().EnsureCatalogNamespace(context.Background(), defaultPublicCatalogNamespace); err != nil {
		t.Fatalf("EnsureCatalogNamespace() error = %v", err)
	}
	ns, err := client.CoreV1().Namespaces().Get(context.Background(), defaultPublicCatalogNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("catalog namespace missing: %v", err)
	}
	if ns.Labels[platformManagedLabel] != "true" || ns.Labels[platformScopeLabel] != "public" {
		t.Fatalf("catalog namespace labels = %#v", ns.Labels)
	}
	if _, err := client.RbacV1().Roles(defaultPublicCatalogNamespace).Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("traefik watch role error = %v, want not found", err)
	}
	if _, err := client.RbacV1().RoleBindings(defaultPublicCatalogNamespace).Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("traefik watch rolebinding error = %v, want not found", err)
	}
}

func TestEnsureTraefikWatchRBACUsesNamespaceRole(t *testing.T) {
	cfg := teamTraefikWatchConfig{
		namespace:      "traefik",
		serviceAccount: "traefik",
	}
	existingRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: traefikWatchRoleName, Namespace: "mcp-servers-public"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	existingBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: traefikWatchRoleName, Namespace: "mcp-servers-public"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     traefikWatchRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: cfg.serviceAccount, Namespace: cfg.namespace},
		},
	}
	client := kubernetesfake.NewSimpleClientset(existingRole, existingBinding)

	if err := ensureTraefikWatchRBAC(context.Background(), client, "mcp-servers-public", cfg); err != nil {
		t.Fatalf("ensureTraefikWatchRBAC() error = %v", err)
	}
	binding, err := client.RbacV1().RoleBindings("mcp-servers-public").Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik watch rolebinding missing: %v", err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != traefikWatchRoleName {
		t.Fatalf("traefik watch binding role ref = %#v, want Role/%s", binding.RoleRef, traefikWatchRoleName)
	}
	role, err := client.RbacV1().Roles("mcp-servers-public").Get(context.Background(), traefikWatchRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik watch role missing: %v", err)
	}
	if len(role.Rules) != 2 {
		t.Fatalf("traefik watch role rules = %#v, want 2 rules", role.Rules)
	}
	hasSecrets := false
	for _, resource := range role.Rules[0].Resources {
		if resource == "secrets" {
			hasSecrets = true
			break
		}
	}
	if !hasSecrets {
		t.Fatalf("traefik watch role rules = %#v, want secrets watch in namespace role", role.Rules)
	}
}

func TestEnsureTraefikDeploymentWatchesNamespaceRetriesConflict(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "traefik",
						Args: []string{
							"--providers.kubernetesingress=true",
							"--providers.kubernetesingress.namespaces=registry,mcp-sentinel,mcp-servers",
						},
					}},
				},
			},
		},
	})
	updateAttempts := 0
	client.Fake.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updateAttempts++
		if updateAttempts == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "deployments"},
				"traefik",
				errors.New("stale deployment resource version"),
			)
		}
		return false, nil, nil
	})

	err := ensureTraefikDeploymentWatchesNamespace(context.Background(), client, "mcp-team-beta", teamTraefikWatchConfig{
		mode:       "required",
		namespace:  "traefik",
		deployment: "traefik",
	})
	if err != nil {
		t.Fatalf("ensureTraefikDeploymentWatchesNamespace() error = %v", err)
	}
	if updateAttempts < 2 {
		t.Fatalf("expected update retry after conflict, got %d attempt(s)", updateAttempts)
	}
	deployment, err := client.AppsV1().Deployments("traefik").Get(context.Background(), "traefik", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("traefik deployment missing: %v", err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, "\n")
	if !strings.Contains(args, "--providers.kubernetesingress.namespaces=registry,mcp-sentinel,mcp-servers,mcp-team-beta") {
		t.Fatalf("traefik namespace args = %q", args)
	}
}

func TestDesiredDeploymentUsesRestrictedPodDefaults(t *testing.T) {
	deployment := desiredDeployment("demo", "user-77", "registry.example.com/demo:latest", 8088, 1, map[string]string{
		"app.kubernetes.io/name": "demo",
	})
	podSpec := deployment.Spec.Template.Spec
	if podSpec.AutomountServiceAccountToken == nil || *podSpec.AutomountServiceAccountToken {
		t.Fatal("expected deployed user workloads to disable service account token automount")
	}
	if podSpec.SecurityContext == nil || podSpec.SecurityContext.SeccompProfile == nil {
		t.Fatal("expected pod security context with seccomp profile")
	}
	if podSpec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccomp profile = %q, want %q", podSpec.SecurityContext.SeccompProfile.Type, corev1.SeccompProfileTypeRuntimeDefault)
	}
	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser != restrictedRunAsUser {
		t.Fatalf("runAsUser = %v, want %d", podSpec.SecurityContext.RunAsUser, restrictedRunAsUser)
	}
	container := podSpec.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("expected user workload container to disallow privilege escalation")
	}
	if container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Fatalf("expected user workload container to drop all capabilities, got %#v", container.SecurityContext.Capabilities)
	}
	if container.Resources.Requests.Cpu().IsZero() || container.Resources.Requests.Memory().IsZero() {
		t.Fatalf("expected user workload resource requests, got %#v", container.Resources)
	}
	if container.Resources.Limits.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
		t.Fatalf("expected user workload resource limits, got %#v", container.Resources)
	}
	if container.ReadinessProbe == nil || container.ReadinessProbe.TCPSocket == nil {
		t.Fatalf("expected TCP readiness probe, got %#v", container.ReadinessProbe)
	}
	if container.LivenessProbe == nil || container.LivenessProbe.TCPSocket == nil {
		t.Fatalf("expected TCP liveness probe, got %#v", container.LivenessProbe)
	}
}

func TestHandleDeploymentItemRejectsServiceUserWithoutIdentity(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
	)
	server := &RuntimeServer{
		k8sClients: &k8sclient.Clients{Clientset: client},
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/deployments/team-a/demo", nil)
	request = request.WithContext(withPrincipal(request.Context(), principal{
		Role:      roleUser,
		IsService: true,
		Namespace: "team-a",
	}))
	recorder := httptest.NewRecorder()
	server.Deployments().HandleDeploymentItem(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func networkPolicyAllowsSameNamespace(policy *networkingv1.NetworkPolicy) bool {
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.PodSelector != nil && peer.NamespaceSelector == nil {
				return true
			}
		}
	}
	return false
}

func networkPolicyAllowsSameNamespaceEgress(policy *networkingv1.NetworkPolicy) bool {
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.PodSelector != nil && peer.NamespaceSelector == nil && len(rule.Ports) == 0 {
				return true
			}
		}
	}
	return false
}

func networkPolicyAllowsEgressToNamespacePort(policy *networkingv1.NetworkPolicy, namespace string, port int) bool {
	for _, rule := range policy.Spec.Egress {
		portAllowed := false
		for _, candidate := range rule.Ports {
			if candidate.Port != nil && candidate.Port.Type == intstr.Int && candidate.Port.IntVal == int32(port) {
				portAllowed = true
			}
		}
		if !portAllowed {
			continue
		}
		for _, peer := range rule.To {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == namespace {
				return true
			}
		}
	}
	return false
}

func networkPolicyAllowsNamespace(policy *networkingv1.NetworkPolicy, namespace string) bool {
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == namespace {
				return true
			}
		}
	}
	return false
}

func clearRegistryPullSecretEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MCP_PLATFORM_DOMAIN",
		"MCP_REGISTRY_INGRESS_HOST",
		"MCP_REGISTRY_HOST",
		"MCP_REGISTRY_ENDPOINT",
	} {
		t.Setenv(key, "")
	}
}
