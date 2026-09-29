package manifest_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

type networkPolicyDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		PodSelector networkPolicySelector      `yaml:"podSelector"`
		Ingress     []networkPolicyIngressRule `yaml:"ingress"`
		Egress      []networkPolicyEgressRule  `yaml:"egress"`
	} `yaml:"spec"`
}

type networkPolicyIngressRule struct {
	From  []networkPolicyPeer `yaml:"from"`
	Ports []networkPolicyPort `yaml:"ports"`
}

type networkPolicyEgressRule struct {
	To    []networkPolicyPeer `yaml:"to"`
	Ports []networkPolicyPort `yaml:"ports"`
}

type networkPolicyPeer struct {
	PodSelector       *networkPolicySelector `yaml:"podSelector"`
	NamespaceSelector *networkPolicySelector `yaml:"namespaceSelector"`
}

type networkPolicySelector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

type networkPolicyPort struct {
	Protocol string `yaml:"protocol"`
	Port     int    `yaml:"port"`
}

func TestRegistryACMEPolicyOnlyAllowsTraefikToSolverPort(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "registry", "base", "acme-networkpolicy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy networkPolicyDoc
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Spec.PodSelector.MatchLabels["acme.cert-manager.io/http01-solver"] != "true" || len(policy.Spec.PodSelector.MatchLabels) != 1 {
		t.Fatal("ACME ingress must select only HTTP-01 solver pods")
	}
	if policy.Metadata.Namespace != "registry" || len(policy.Spec.Egress) != 0 || len(policy.Spec.Ingress) != 1 {
		t.Fatal("ACME policy must only add one registry ingress rule")
	}
	rule := policy.Spec.Ingress[0]
	if len(rule.Ports) != 1 || !networkPolicyPortsInclude(rule.Ports, "TCP", 8089) || len(rule.From) != 2 {
		t.Fatal("ACME policy must allow only solver TCP 8089 from the two Traefik installations")
	}
	want := map[string]string{"traefik": "app", "kube-system": "app.kubernetes.io/name"}
	for _, peer := range rule.From {
		if peer.NamespaceSelector == nil || peer.PodSelector == nil {
			t.Fatal("ACME source must constrain both namespace and Traefik pod labels")
		}
		ns := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
		label, ok := want[ns]
		if !ok || peer.PodSelector.MatchLabels[label] != "traefik" {
			t.Fatalf("unexpected ACME ingress source: %+v", peer)
		}
		delete(want, ns)
	}
	if len(want) != 0 {
		t.Fatal("missing a supported Traefik ingress source")
	}
}

func TestRegistryNetworkPolicyAllowsHelperPushOnlyToRegistry(t *testing.T) {
	policies := loadRegistryNetworkPolicies(t)

	ingress, ok := policies["registry-allow-ingress"]
	if !ok {
		t.Fatal("registry-allow-ingress policy not found")
	}
	if !hasSameNamespaceIngressToPort(ingress, 5000) {
		t.Fatal("registry ingress policy must allow same-namespace helper pods to reach registry:5000")
	}
	if !hasNamespaceIngressToPort(ingress, "mcp-servers", 5000) {
		t.Fatal("registry ingress policy must allow catalog namespace probes to reach registry:5000")
	}
	if !hasManagedNamespaceIngressToPort(ingress, 5000) {
		t.Fatal("registry ingress policy must allow managed namespace probes to reach registry:5000")
	}

	egress, ok := policies["registry-allow-egress"]
	if !ok {
		t.Fatal("registry-allow-egress policy not found")
	}
	if !hasDNSEgress(egress) {
		t.Fatal("registry egress policy must allow helper pods to resolve cluster DNS")
	}
	if !hasRegistryEgressToPort(egress, 5000) {
		t.Fatal("registry egress policy must allow helper pods to reach only registry pods on port 5000")
	}
}

func loadRegistryNetworkPolicies(t *testing.T) map[string]networkPolicyDoc {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "registry", "base", "networkpolicy.yaml"))
	if err != nil {
		t.Fatalf("read registry network policy manifest: %v", err)
	}

	policies := map[string]networkPolicyDoc{}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc networkPolicyDoc
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode registry network policy manifest: %v", err)
		}
		if doc.Kind == "NetworkPolicy" && doc.Metadata.Namespace == "registry" {
			policies[doc.Metadata.Name] = doc
		}
	}
	return policies
}

func hasSameNamespaceIngressToPort(policy networkPolicyDoc, port int) bool {
	for _, rule := range policy.Spec.Ingress {
		if !networkPolicyPortsInclude(rule.Ports, "TCP", port) {
			continue
		}
		for _, peer := range rule.From {
			if peer.PodSelector != nil && peer.NamespaceSelector == nil {
				return true
			}
		}
	}
	return false
}

func hasNamespaceIngressToPort(policy networkPolicyDoc, namespace string, port int) bool {
	for _, rule := range policy.Spec.Ingress {
		if !networkPolicyPortsInclude(rule.Ports, "TCP", port) {
			continue
		}
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil &&
				peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == namespace {
				return true
			}
		}
	}
	return false
}

func hasManagedNamespaceIngressToPort(policy networkPolicyDoc, port int) bool {
	for _, rule := range policy.Spec.Ingress {
		if !networkPolicyPortsInclude(rule.Ports, "TCP", port) {
			continue
		}
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil &&
				peer.NamespaceSelector.MatchLabels["platform.mcpruntime.org/managed"] == "true" {
				return true
			}
		}
	}
	return false
}

func hasDNSEgress(policy networkPolicyDoc) bool {
	for _, rule := range policy.Spec.Egress {
		if !networkPolicyPortsInclude(rule.Ports, "UDP", 53) || !networkPolicyPortsInclude(rule.Ports, "TCP", 53) {
			continue
		}
		for _, peer := range rule.To {
			if peer.NamespaceSelector == nil || peer.PodSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "kube-system" &&
				peer.PodSelector.MatchLabels["k8s-app"] == "kube-dns" {
				return true
			}
		}
	}
	return false
}

func hasRegistryEgressToPort(policy networkPolicyDoc, port int) bool {
	for _, rule := range policy.Spec.Egress {
		if !networkPolicyPortsInclude(rule.Ports, "TCP", port) {
			continue
		}
		for _, peer := range rule.To {
			if peer.PodSelector != nil &&
				peer.PodSelector.MatchLabels["app"] == "registry" &&
				peer.NamespaceSelector == nil {
				return true
			}
		}
	}
	return false
}

func networkPolicyPortsInclude(ports []networkPolicyPort, protocol string, port int) bool {
	for _, candidate := range ports {
		if candidate.Protocol == protocol && candidate.Port == port {
			return true
		}
	}
	return false
}
