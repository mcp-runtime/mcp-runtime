package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// Evaluate every bundled ingress policy together: an extra broad allow policy
// must fail this check rather than being hidden by the presence of a deny.
func TestScopedTelemetryRejectsTenantAndIngressBypasses(t *testing.T) {
	dir := filepath.Join("..", "..", "k8s")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var policies []networkingv1.NetworkPolicy
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(data), "\n---\n") {
			var kind struct{ Kind string }
			if yaml.Unmarshal([]byte(doc), &kind) != nil || kind.Kind != "NetworkPolicy" {
				continue
			}
			var policy networkingv1.NetworkPolicy
			if err := yaml.Unmarshal([]byte(doc), &policy); err != nil {
				t.Fatal(err)
			}
			policies = append(policies, policy)
		}
	}
	for _, target := range []struct {
		app  string
		port int32
	}{{"grafana", 3000}, {"prometheus", 9090}, {"loki", 3100}, {"tempo", 3200}} {
		for _, source := range []struct{ namespace, app string }{
			{"mcp-team-foreign", "server"}, {"mcp-servers", "demo"}, {"mcp-servers-public", "demo"},
			{"mcp-team-foreign", "mcp-runtime-api"}, // app-label spoof alone is insufficient
			{"traefik", "traefik"}, {"kube-system", "traefik"}, {"mcp-platform", "mcp-ui"},
		} {
			if scopedIngressAllows(policies, target.app, target.port, source.namespace, source.app) {
				t.Errorf("%s/%s can bypass scoped proxy to %s:%d", source.namespace, source.app, target.app, target.port)
			}
		}
	}
	for _, route := range []struct {
		target, namespace, source string
		port                      int32
	}{
		{"grafana", "mcp-platform", "mcp-runtime-api", 3000},
		{"prometheus", "mcp-platform", "mcp-runtime-api", 9090},
		{"loki", "mcp-platform", "mcp-runtime-api", 3100},
		{"prometheus", "mcp-observability", "grafana", 9090},
		{"loki", "mcp-observability", "grafana", 3100},
		{"tempo", "mcp-observability", "grafana", 3200},
		{"loki", "mcp-log-collector", "promtail", 3100},
		{"tempo", "mcp-observability", "otel-collector", 4317},
	} {
		if !scopedIngressAllows(policies, route.target, route.port, route.namespace, route.source) {
			t.Errorf("required telemetry route blocked: %+v", route)
		}
	}
}

func scopedIngressAllows(policies []networkingv1.NetworkPolicy, target string, port int32, namespace, app string) bool {
	selected := false
	for _, policy := range policies {
		if policy.Namespace != "mcp-observability" || !scopedSelectorMatches(&policy.Spec.PodSelector, map[string]string{"app": target}) {
			continue
		}
		ingress := len(policy.Spec.PolicyTypes) == 0
		for _, typ := range policy.Spec.PolicyTypes {
			ingress = ingress || typ == networkingv1.PolicyTypeIngress
		}
		if !ingress {
			continue
		}
		selected = true
		for _, rule := range policy.Spec.Ingress {
			portAllowed := len(rule.Ports) == 0
			for _, candidate := range rule.Ports {
				portAllowed = portAllowed || candidate.Port == nil || candidate.Port.IntVal == port
			}
			if !portAllowed {
				continue
			}
			if len(rule.From) == 0 {
				return true
			}
			for _, peer := range rule.From {
				if peer.IPBlock != nil {
					// Any CIDR allowance here requires an explicit test fixture
					// review; conservatively treat it as a potential bypass.
					return true
				}
				namespaceAllowed := namespace == policy.Namespace
				if peer.NamespaceSelector != nil {
					namespaceAllowed = scopedSelectorMatches(peer.NamespaceSelector, map[string]string{"kubernetes.io/metadata.name": namespace})
				}
				if namespaceAllowed && scopedSelectorMatches(peer.PodSelector, map[string]string{"app": app}) {
					return true
				}
			}
		}
	}
	return !selected
}

func scopedSelectorMatches(selector *metav1.LabelSelector, values map[string]string) bool {
	if selector == nil {
		return true
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	return err == nil && parsed.Matches(labels.Set(values))
}
