package manifest_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// MCPServer backends get a per-server egress policy from the operator
// (internal/operator/traefik_egress.go). The static policy must not grant
// Traefik a fixed port list into MCP server namespaces (#618).
func TestTraefikStaticEgressCoversOnlyPlatformNamespaces(t *testing.T) {
	policies := loadTraefikNetworkPolicies(t)
	egress, ok := policies["traefik-allow-egress"]
	if !ok {
		t.Fatal("traefik-allow-egress policy not found")
	}
	allowed := map[string]bool{"kube-system": true, "registry": true, "mcp-platform": true, "mcp-observability": true}
	seen := map[string]bool{}
	for _, rule := range egress.Spec.Egress {
		for _, peer := range rule.To {
			if peer.NamespaceSelector == nil {
				t.Fatalf("traefik egress peer without a namespace selector: %+v", peer)
			}
			name, ok := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
			if !ok || len(peer.NamespaceSelector.MatchLabels) != 1 {
				t.Fatalf("traefik egress must name platform namespaces explicitly, got %v", peer.NamespaceSelector.MatchLabels)
			}
			if !allowed[name] {
				t.Fatalf("traefik static egress must not reach MCP server namespace %q; the operator manages per-server egress", name)
			}
			seen[name] = true
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Fatalf("traefik static egress lost platform namespace %q", name)
		}
	}
	if !hasEgressPort(egress, 5000) {
		t.Fatal("traefik egress must keep the registry port")
	}
}

// The static policies ship with the repo-managed Traefik, which setup installs
// only into the traefik namespace. An external Traefik (k3s kube-system) gets
// no static egress restriction, so the operator leaves its egress unrestricted
// instead of adding a per-server policy that would isolate it.
func TestTraefikStaticPoliciesTargetRepoManagedNamespace(t *testing.T) {
	for name, policy := range loadTraefikNetworkPolicies(t) {
		if policy.Metadata.Namespace != "traefik" {
			t.Fatalf("policy %s targets namespace %q, want the repo-managed traefik namespace", name, policy.Metadata.Namespace)
		}
	}
}

func loadTraefikNetworkPolicies(t *testing.T) map[string]networkPolicyDoc {
	t.Helper()

	path := filepath.Join("..", "..", "config", "ingress", "base", "networkpolicy.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read traefik networkpolicy: %v", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	policies := make(map[string]networkPolicyDoc)
	for {
		var doc networkPolicyDoc
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode traefik networkpolicy: %v", err)
		}
		if doc.Kind != "NetworkPolicy" || doc.Metadata.Name == "" {
			continue
		}
		policies[doc.Metadata.Name] = doc
	}
	return policies
}

func hasEgressPort(rule networkPolicyDoc, port int) bool {
	for _, egress := range rule.Spec.Egress {
		for _, p := range egress.Ports {
			if p.Protocol == "TCP" && p.Port == port {
				return true
			}
		}
	}
	return false
}
