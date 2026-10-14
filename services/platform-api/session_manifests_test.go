package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

func TestGatewayIdentityRBACReadsOnlyControllerChain(t *testing.T) {
	var found bool
	allowed := map[string]string{"pods": "", "replicasets": "apps", "deployments": "apps", "mcpservers": "mcpruntime.org", "tokenreviews": "authentication.k8s.io"}
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
