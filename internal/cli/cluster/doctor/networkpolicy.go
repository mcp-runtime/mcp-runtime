package doctor

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"mcp-runtime/internal/cli/core"
)

type doctorEgressNetworkPolicy struct {
	Spec struct {
		Egress []struct {
			To []struct {
				IPBlock *struct {
					CIDR string `json:"cidr"`
				} `json:"ipBlock"`
			} `json:"to"`
			Ports []struct {
				Port int `json:"port"`
			} `json:"ports"`
		} `json:"egress"`
	} `json:"spec"`
}

// checkRuntimeAPIKubernetesAPIEgress validates the actual endpoint port rather
// than assuming every Kubernetes distribution exposes the API on 6443. This
// catches the especially subtle failure where /health is green but all runtime
// inventory calls fail when the API server is reached through a NetworkPolicy.
func checkRuntimeAPIKubernetesAPIEgress(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: true, Detail: "namespace mcp-platform not found; skipping runtime API egress check"}
	}
	portsRaw, err := readKubectlOutput(kubectl, []string{"get", "endpoints", "kubernetes", "-o", `jsonpath={range .subsets[*].ports[*]}{.port}{"\n"}{end}`})
	if err != nil {
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: fmt.Sprintf("failed reading Kubernetes API endpoint ports: %v", err), Remedy: "verify the kubernetes Service/Endpoints object and kubectl access"}
	}
	ports := make([]int, 0, 2)
	for _, line := range strings.Split(portsRaw, "\n") {
		port, parseErr := strconv.Atoi(strings.TrimSpace(line))
		if parseErr == nil && port > 0 {
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: "Kubernetes Service has no usable endpoint port", Remedy: "restore Kubernetes API endpoints before using the runtime catalog"}
	}
	policyRaw, err := readNetworkPolicyJSON(kubectl, componentNamespace("platform-api"), "mcp-runtime-api-platform-egress")
	if err != nil {
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: fmt.Sprintf("failed reading runtime API NetworkPolicy: %v", err), Remedy: "apply k8s/22-split-api-networkpolicy.yaml or configure the runtime API egress policy"}
	}
	var policy doctorEgressNetworkPolicy
	if err := json.Unmarshal([]byte(policyRaw), &policy); err != nil {
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: fmt.Sprintf("runtime API NetworkPolicy is invalid JSON: %v", err), Remedy: "reapply the runtime API NetworkPolicy"}
	}
	for _, endpointPort := range ports {
		for _, rule := range policy.Spec.Egress {
			public := false
			for _, peer := range rule.To {
				if peer.IPBlock != nil && peer.IPBlock.CIDR == "0.0.0.0/0" {
					public = true
				}
			}
			if !public {
				continue
			}
			for _, port := range rule.Ports {
				if port.Port == endpointPort {
					return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: true, Detail: fmt.Sprintf("runtime API egress allows Kubernetes API endpoint port %d", endpointPort)}
				}
			}
		}
		return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: fmt.Sprintf("runtime API NetworkPolicy does not allow Kubernetes API endpoint port %d", endpointPort), Remedy: fmt.Sprintf("allow TCP %d in mcp-runtime-api-platform-egress (configure MCP_KUBERNETES_API_PORT if this is a non-standard cluster)", endpointPort)}
	}
	return DoctorCheck{Name: "runtime API Kubernetes API egress", OK: false, Detail: "runtime API NetworkPolicy has no public egress rule for Kubernetes API endpoints", Remedy: "allow the Kubernetes API endpoint through mcp-runtime-api-platform-egress"}
}

func checkPlatformAPILiveInventoryNetworkPolicy(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "mcpservers", "-A", "-o", buildMCPServerNamespaceJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "platform API live inventory ingress",
			OK:     false,
			Detail: fmt.Sprintf("failed listing MCPServer namespaces: %v", err),
			Remedy: "check MCPServer CRD availability and RBAC for listing MCPServers across namespaces",
		}
	}
	namespaces := teamNamespacesWithMCPServers(out)
	if len(namespaces) == 0 {
		return DoctorCheck{
			Name:   "platform API live inventory ingress",
			OK:     true,
			Detail: "no team MCPServer namespaces found",
		}
	}

	checked := 0
	for _, namespace := range namespaces {
		policyJSON, err := readNetworkPolicyJSON(kubectl, namespace, "platform-default-deny")
		if isKubectlNotFound(err) {
			continue
		}
		if err != nil {
			return DoctorCheck{
				Name:   "platform API live inventory ingress",
				OK:     false,
				Detail: fmt.Sprintf("failed reading networkpolicy %s/platform-default-deny: %v", namespace, err),
				Remedy: "check Kubernetes API access and RBAC for reading NetworkPolicies in team namespaces",
			}
		}
		checked++
		if !networkPolicyAllowsPlatformAPI(policyJSON) {
			return DoctorCheck{
				Name:   "platform API live inventory ingress",
				OK:     false,
				Detail: fmt.Sprintf("networkpolicy %s/platform-default-deny blocks %s from probing MCPServer Services", namespace, componentNamespace("platform-api")),
				Remedy: "rerun team provisioning or patch platform-default-deny to allow ingress from namespace " + componentNamespace("platform-api"),
			}
		}
	}
	if checked == 0 {
		return DoctorCheck{
			Name:   "platform API live inventory ingress",
			OK:     true,
			Detail: fmt.Sprintf("%d team MCPServer namespace(s) found; no platform-default-deny NetworkPolicy present", len(namespaces)),
		}
	}
	return DoctorCheck{
		Name:   "platform API live inventory ingress",
		OK:     true,
		Detail: fmt.Sprintf("%d team NetworkPolicy object(s) allow platform API live inventory probes", checked),
	}
}

func readNetworkPolicyJSON(kubectl core.KubectlRunner, namespace, name string) (string, error) {
	cmd, err := kubectl.CommandArgs([]string{"get", "networkpolicy", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return "", err
	}
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		return string(out), nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return "", runErr
	}
	return "", fmt.Errorf("%w: %s", runErr, detail)
}

func isKubectlNotFound(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "notfound") || strings.Contains(lower, "not found")
}

func isKubectlNotFoundOutput(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(lower, "(notfound)") || strings.Contains(lower, "not found")
}

func buildMCPServerNamespaceJSONPath() string {
	return `jsonpath={range .items[*]}{.metadata.namespace}{"\n"}{end}`
}

func teamNamespacesWithMCPServers(out string) []string {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		namespace := strings.TrimSpace(line)
		if !strings.HasPrefix(namespace, "mcp-team-") {
			continue
		}
		seen[namespace] = struct{}{}
	}
	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

type doctorNetworkPolicy struct {
	Spec struct {
		Ingress []struct {
			From []struct {
				NamespaceSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"namespaceSelector"`
				PodSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"podSelector"`
			} `json:"from"`
		} `json:"ingress"`
	} `json:"spec"`
}

func networkPolicyAllowsPlatformAPI(raw string) bool {
	var policy doctorNetworkPolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return false
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != componentNamespace("platform-api") {
				continue
			}
			if peer.PodSelector == nil {
				return true
			}
			labels := peer.PodSelector.MatchLabels
			if len(labels) == 0 || platformAPIPodAppAllowed(labels["app"]) {
				return true
			}
		}
	}
	return false
}

func platformAPIPodAppAllowed(app string) bool {
	switch app {
	case doctorPlatformAPIService, doctorRuntimeAPIService, doctorAnalyticsAPIService:
		return true
	default:
		return false
	}
}
