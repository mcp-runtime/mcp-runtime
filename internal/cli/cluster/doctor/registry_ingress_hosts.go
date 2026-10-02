package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
)

const registryIngressHostsCheckName = "registry Ingress hosts"

type registryIngressHostsView struct {
	Spec struct {
		Rules []struct {
			Host string `json:"host"`
		} `json:"rules"`
		TLS []struct {
			Hosts []string `json:"hosts"`
		} `json:"tls"`
	} `json:"spec"`
}

// checkRegistryIngressHosts verifies the registry Ingress rule hosts match its
// TLS hosts and the platform registry host. A rule host of registry.local with
// a public TLS host makes Traefik answer 404 for every public registry request
// (anonymous /v2/ included), so node image pulls fail with NotFound.
func checkRegistryIngressHosts(kubectl core.KubectlRunner) DoctorCheck {
	raw, err := readKubectlOutput(kubectl, []string{"get", "ingress", k8sclient.RegistryIngressName, "-n", k8sclient.RegistryIngressNamespace, "-o", "json", "--ignore-not-found"})
	if err != nil {
		return DoctorCheck{Name: registryIngressHostsCheckName, OK: false, Detail: fmt.Sprintf("kubectl error: %v", err), Remedy: "check cluster connectivity and kubeconfig"}
	}
	if strings.TrimSpace(raw) == "" {
		return DoctorCheck{Name: registryIngressHostsCheckName, OK: true, Detail: "no bundled registry Ingress (external registry or not installed)"}
	}
	var ing registryIngressHostsView
	if err := json.Unmarshal([]byte(raw), &ing); err != nil {
		return DoctorCheck{Name: registryIngressHostsCheckName, OK: false, Detail: fmt.Sprintf("decode registry Ingress: %v", err), Remedy: "inspect `kubectl get ingress registry -n registry -o yaml`"}
	}
	var ruleHosts, tlsHosts []string
	for _, r := range ing.Spec.Rules {
		ruleHosts = append(ruleHosts, strings.TrimSpace(r.Host))
	}
	for _, t := range ing.Spec.TLS {
		for _, h := range t.Hosts {
			tlsHosts = append(tlsHosts, strings.TrimSpace(h))
		}
	}
	configName := platforminventory.SharedConfigName
	configHost, _ := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "jsonpath={.data.MCP_REGISTRY_INGRESS_HOST}"})
	configDomain, _ := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "jsonpath={.data.MCP_PLATFORM_DOMAIN}"})
	return evaluateRegistryIngressHosts(ruleHosts, tlsHosts, strings.TrimSpace(configHost), strings.TrimSpace(configDomain))
}

func evaluateRegistryIngressHosts(ruleHosts, tlsHosts []string, configHost, configDomain string) DoctorCheck {
	platformHost, _ := k8sclient.ChooseRegistryPublicHost(k8sclient.RegistryHostSources{
		ConfigIngressHost:    configHost,
		ConfigPlatformDomain: configDomain,
	})
	if k8sclient.IsPlaceholderRegistryHost(platformHost) {
		platformHost = ""
	}
	// The host every rule should carry: the public TLS host first (what the
	// certificate covers and nodes resolve), then the platform registry host.
	expected, _ := k8sclient.ChooseRegistryPublicHost(k8sclient.RegistryHostSources{
		IngressTLSHosts:   tlsHosts,
		ConfigIngressHost: platformHost,
	})
	detail := fmt.Sprintf("rules=%v tls=%v platform=%q", ruleHosts, tlsHosts, platformHost)
	var problems []string
	for _, h := range ruleHosts {
		if len(tlsHosts) > 0 && !containsFold(tlsHosts, h) {
			problems = append(problems, fmt.Sprintf("rule host %q is not in TLS hosts %v", h, tlsHosts))
		}
		if platformHost != "" && !strings.EqualFold(h, platformHost) {
			problems = append(problems, fmt.Sprintf("rule host %q differs from platform registry host %q", h, platformHost))
		}
	}
	if platformHost != "" && len(tlsHosts) > 0 && !containsFold(tlsHosts, platformHost) {
		problems = append(problems, fmt.Sprintf("TLS hosts %v do not cover platform registry host %q", tlsHosts, platformHost))
	}
	if len(problems) == 0 {
		return DoctorCheck{Name: registryIngressHostsCheckName, OK: true, Detail: detail}
	}
	remedy := "rerun setup with MCP_PLATFORM_DOMAIN (or MCP_REGISTRY_INGRESS_HOST) set to the public registry domain"
	if !k8sclient.IsPlaceholderRegistryHost(expected) {
		remedy = fmt.Sprintf(`kubectl patch ingress %s -n %s --type=json -p '[{"op":"replace","path":"/spec/rules/0/host","value":"%s"}]'  # Traefik returns 404 on https://%s/v2/ until the rule host matches`,
			k8sclient.RegistryIngressName, k8sclient.RegistryIngressNamespace, expected, expected)
	}
	return DoctorCheck{
		Name:   registryIngressHostsCheckName,
		OK:     false,
		Detail: detail + ": " + strings.Join(problems, "; "),
		Remedy: remedy,
	}
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
