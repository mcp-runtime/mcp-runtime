package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
	"mcp-runtime/pkg/sessionstate"
)

func manifestObjects(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	d := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	objects := []json.RawMessage{}
	for {
		var object json.RawMessage
		err := d.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(object) > 0 {
			objects = append(objects, object)
		}
	}
	return objects
}

func TestPlatformIngressAllowsSupportedTraefikWithoutSessionSync(t *testing.T) {
	var ingress networkingv1.NetworkPolicy
	for _, raw := range manifestObjects(t, "k8s/22-split-api-networkpolicy.yaml") {
		var policy networkingv1.NetworkPolicy
		if err := json.Unmarshal(raw, &policy); err != nil {
			t.Fatal(err)
		}
		if policy.Name == "mcp-platform-api-ingress" {
			ingress = policy
		}
	}
	if ingress.Name == "" {
		t.Fatal("platform API ingress policy missing")
	}
	var bundledLabels map[string]string
	for _, raw := range manifestObjects(t, "config/ingress/base/traefik.yaml") {
		var deployment appsv1.Deployment
		if err := json.Unmarshal(raw, &deployment); err != nil {
			t.Fatal(err)
		}
		if deployment.Kind == "Deployment" && deployment.Name == "traefik" {
			bundledLabels = deployment.Spec.Template.Labels
		}
	}
	if len(bundledLabels) == 0 {
		t.Fatal("bundled Traefik pod labels missing")
	}
	for _, tc := range []struct {
		name, namespace string
		podLabels       map[string]string
		public, sync    bool
	}{
		{"bundled Traefik", "traefik", bundledLabels, true, false},
		{"k3s Traefik", "kube-system", map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"}, true, false},
		{"Helm Traefik custom namespace", "ingress", map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-ingress"}, true, false},
		{"platform UI", "mcp-platform", map[string]string{"app": "mcp-ui"}, true, false},
		{"analytics API", "mcp-observability", map[string]string{"app": "mcp-analytics-api"}, true, false},
		{"analytics label in tenant namespace", "team-a", map[string]string{"app": "mcp-analytics-api"}, false, false},
		{"unrelated observability pod", "mcp-observability", map[string]string{"app": "unrelated"}, false, false},
		{"tenant gateway", "team-a", map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}, false, true},
		{"unrelated tenant", "team-a", map[string]string{"app": "tenant-app"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := networkPolicyAllowsTCP(t, ingress, tc.namespace, tc.podLabels, 8080); got != tc.public {
				t.Errorf("public API ingress = %v, want %v", got, tc.public)
			}
			if got := networkPolicyAllowsTCP(t, ingress, tc.namespace, tc.podLabels, sessionstate.GatewayPort); got != tc.sync {
				t.Errorf("session sync ingress = %v, want %v", got, tc.sync)
			}
		})
	}
}

// Evaluate the peer AND/OR semantics and numeric TCP ports used by this policy.
func networkPolicyAllowsTCP(t *testing.T, policy networkingv1.NetworkPolicy, namespace string, podLabels map[string]string, port int32) bool {
	t.Helper()
	for _, rule := range policy.Spec.Ingress {
		portAllowed := len(rule.Ports) == 0
		for _, candidate := range rule.Ports {
			if candidate.Protocol != nil && *candidate.Protocol != corev1.ProtocolTCP {
				continue
			}
			if candidate.Port == nil || candidate.Port.IntVal == port {
				portAllowed = true
			}
		}
		if !portAllowed {
			continue
		}
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				if namespace != policy.Namespace {
					continue
				}
			} else {
				selector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
				if err != nil {
					t.Fatal(err)
				}
				if !selector.Matches(labels.Set{"kubernetes.io/metadata.name": namespace}) {
					continue
				}
			}
			if peer.PodSelector != nil {
				selector, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
				if err != nil {
					t.Fatal(err)
				}
				if !selector.Matches(labels.Set(podLabels)) {
					continue
				}
			}
			return true
		}
	}
	return false
}

func TestSessionSyncPortIsNeverAnIngressBackend(t *testing.T) {
	var platformRoutes int
	for _, raw := range manifestObjects(t, "k8s/10-gateway.yaml") {
		var meta struct{ Kind string }
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Kind != "Ingress" {
			continue
		}
		var ingress networkingv1.Ingress
		if err := json.Unmarshal(raw, &ingress); err != nil {
			t.Fatal(err)
		}
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				svc := path.Backend.Service
				if svc == nil {
					continue
				}
				if svc.Name == "mcp-platform-api" && (svc.Port.Number == sessionstate.GatewayPort || svc.Port.Name == "gateway-sync") {
					t.Fatalf("sync port exposed on ingress %s", ingress.Name)
				}
				if svc.Name == "mcp-platform-api" {
					platformRoutes++
					if strings.HasPrefix("/internal/gateway/v1/sessions", path.Path) || strings.HasPrefix(path.Path, "/internal/") {
						t.Fatalf("public ingress can reach internal route: %s", path.Path)
					}
				}
			}
		}
	}
	if platformRoutes == 0 {
		t.Fatal("no platform ingress routes checked")
	}
}

func TestSessionSyncServiceAndNetworkPolicyPorts(t *testing.T) {
	var portFound, ingressFound bool
	for _, raw := range manifestObjects(t, "k8s/08-platform-api.yaml") {
		var meta struct{ Kind string }
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Kind != "Service" {
			continue
		}
		var svc corev1.Service
		if err := json.Unmarshal(raw, &svc); err != nil {
			t.Fatal(err)
		}
		if svc.Kind != "Service" {
			continue
		}
		for _, port := range svc.Spec.Ports {
			if port.Name == "gateway-sync" && port.Port == sessionstate.GatewayPort && port.TargetPort.IntVal == sessionstate.GatewayPort {
				portFound = true
			}
		}
	}
	if !portFound {
		t.Fatal("gateway sync service port missing")
	}
	for _, raw := range manifestObjects(t, "k8s/22-split-api-networkpolicy.yaml") {
		var np networkingv1.NetworkPolicy
		if err := json.Unmarshal(raw, &np); err != nil {
			t.Fatal(err)
		}
		if np.Name != "mcp-platform-api-ingress" {
			continue
		}
		ingressFound = true
		for _, rule := range np.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.NamespaceSelector != nil && peer.PodSelector != nil && peer.PodSelector.MatchLabels["app.kubernetes.io/managed-by"] == "mcp-runtime" {
					if len(rule.Ports) != 1 || rule.Ports[0].Port == nil || rule.Ports[0].Port.IntVal != sessionstate.GatewayPort {
						t.Fatal("tenant gateway ingress includes control-plane port")
					}
				}
			}
		}
	}
	if !ingressFound {
		t.Fatal("platform API ingress isolation missing")
	}
}

func TestGatewayIdentityRBACReadsOnlyControllerChainAndCurrentGrant(t *testing.T) {
	var found bool
	allowed := map[string]string{"pods": "", "replicasets": "apps", "deployments": "apps", "mcpservers": "mcpruntime.org", "mcpaccessgrants":"mcpruntime.org", "tokenreviews": "authentication.k8s.io"}
	for _, raw := range manifestObjects(t, "k8s/08-platform-api-rbac.yaml") {
		var role rbacv1.ClusterRole
		if err := json.Unmarshal(raw, &role); err != nil {
			t.Fatal(err)
		}
		if role.Kind != "ClusterRole" || role.Name != "mcp-platform-api-gateway-identity" {
			continue
		}
		found = true
		for _, rule := range role.Rules {
			for _, resource := range rule.Resources {
				group, ok := allowed[resource]
				if !ok || len(rule.APIGroups) != 1 || rule.APIGroups[0] != group {
					t.Fatalf("unexpected gateway identity access: %+v", rule)
				}
				verb := "get"
				if resource == "tokenreviews" {
					verb = "create"
				}
				if len(rule.Verbs) != 1 || rule.Verbs[0] != verb {
					t.Fatalf("gateway identity permissions too broad: %+v", rule)
				}
			}
		}
	}
	if !found {
		t.Fatal("gateway identity RBAC missing")
	}
}
