package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
)

const traefikEgressCheckName = "MCPServer Traefik egress"

// checkMCPServerTraefikEgress surfaces the operator's TraefikEgressReady
// condition. False means the operator could not find Traefik pods in its
// configured ingress controller namespace, so it cannot open (or verify)
// Traefik egress to the server and ingress traffic may be dropped.
func checkMCPServerTraefikEgress(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "mcpservers", "-A", "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: traefikEgressCheckName, OK: true, Detail: "MCPServer CRD is not installed yet; no servers to check"}
	}
	failing, total, err := traefikEgressFailures([]byte(out))
	if err != nil {
		return DoctorCheck{Name: traefikEgressCheckName, OK: false, Detail: fmt.Sprintf("failed parsing MCPServers: %v", err), Remedy: "rerun Doctor and inspect MCPServer JSON"}
	}
	if len(failing) > 0 {
		return DoctorCheck{
			Name:   traefikEgressCheckName,
			OK:     false,
			Detail: strings.Join(limitStrings(failing, 5), "; "),
			Remedy: "rerun `mcp-runtime setup` so the operator receives the live Traefik namespace and pod labels (PLATFORM_TRAEFIK_NAMESPACE), or set MCP_INGRESS_CONTROLLER_NAMESPACE and MCP_INGRESS_CONTROLLER_POD_LABELS on the operator Deployment",
		}
	}
	return DoctorCheck{Name: traefikEgressCheckName, OK: true, Detail: fmt.Sprintf("%d MCPServer(s); none report TraefikEgressReady=False", total)}
}

func traefikEgressFailures(raw []byte) ([]string, int, error) {
	var servers struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &servers); err != nil {
		return nil, 0, err
	}
	var failing []string
	for _, server := range servers.Items {
		for _, condition := range server.Status.Conditions {
			if condition.Type == "TraefikEgressReady" && condition.Status == "False" {
				failing = append(failing, fmt.Sprintf("%s/%s %s: %s", server.Metadata.Namespace, server.Metadata.Name, condition.Reason, condition.Message))
			}
		}
	}
	return failing, len(servers.Items), nil
}
