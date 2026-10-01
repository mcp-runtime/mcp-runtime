package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/platforminventory"
)

func checkClusterImageArchitecture(kubectl core.KubectlRunner) DoctorCheck {
	configured := strings.TrimSpace(os.Getenv("MCP_IMAGE_PLATFORM"))
	if configured == "" {
		return DoctorCheck{Name: "cluster image architecture", OK: true, Detail: "MCP_IMAGE_PLATFORM is unset; image architecture compatibility is not enforced by the doctor"}
	}
	parts := strings.Split(configured, "/")
	want := strings.TrimSpace(parts[len(parts)-1])
	if want == "" {
		return DoctorCheck{Name: "cluster image architecture", OK: false, Detail: fmt.Sprintf("MCP_IMAGE_PLATFORM=%q has no architecture", configured), Remedy: "set MCP_IMAGE_PLATFORM to a valid value such as linux/amd64 or linux/arm64"}
	}
	out, err := readKubectlOutput(kubectl, []string{"get", "nodes", "-o", `jsonpath={range .items[*]}{.status.nodeInfo.architecture}{"\n"}{end}`})
	if err != nil {
		return DoctorCheck{Name: "cluster image architecture", OK: false, Detail: fmt.Sprintf("failed reading node architectures: %v", err), Remedy: "check kubectl access and node status"}
	}
	architectures := uniqueNonEmptyLines(out)
	for _, architecture := range architectures {
		if architecture != want {
			return DoctorCheck{Name: "cluster image architecture", OK: false, Detail: fmt.Sprintf("configured image architecture %s does not match node architecture %s", want, architecture), Remedy: "set MCP_IMAGE_PLATFORM to a node-supported architecture or publish multi-architecture images"}
		}
	}
	return DoctorCheck{Name: "cluster image architecture", OK: true, Detail: fmt.Sprintf("MCP_IMAGE_PLATFORM=%s matches node architecture(s) %s", configured, strings.Join(architectures, ", "))}
}

func checkDNSNetworkPolicyPortability(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "DNS NetworkPolicy portability", OK: true, Detail: "namespace mcp-platform is not installed; skipping runtime API DNS policy check"}
	}
	selectors := []string{"k8s-app=kube-dns", "k8s-app=coredns", "app.kubernetes.io/name=coredns"}
	matched := ""
	for _, selector := range selectors {
		out, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", "kube-system", "-l", selector, "-o", "jsonpath={.items[0].metadata.name}"})
		if err == nil && strings.TrimSpace(out) != "" {
			matched = selector
			break
		}
	}
	if matched == "" {
		return DoctorCheck{Name: "DNS NetworkPolicy portability", OK: false, Detail: "no supported CoreDNS/kube-dns pod label was found", Remedy: "configure MCP_DNS_LABEL_KEY and MCP_DNS_LABEL_VALUE or ensure the cluster DNS Service has discoverable backing pods"}
	}
	policy, err := readNetworkPolicyJSON(kubectl, componentNamespace("platform-api"), "mcp-runtime-api-platform-egress")
	if err != nil {
		return DoctorCheck{Name: "DNS NetworkPolicy portability", OK: false, Detail: fmt.Sprintf("failed reading runtime API NetworkPolicy: %v", err), Remedy: "apply the runtime API NetworkPolicy after discovering the cluster DNS selector"}
	}
	key, value, _ := strings.Cut(matched, "=")
	if !strings.Contains(policy, fmt.Sprintf(`"%s":"%s"`, key, value)) && !strings.Contains(policy, fmt.Sprintf(`"%s": "%s"`, key, value)) {
		return DoctorCheck{Name: "DNS NetworkPolicy portability", OK: false, Detail: fmt.Sprintf("runtime API NetworkPolicy does not allow detected DNS selector %s", matched), Remedy: "configure the DNS selector and re-render the NetworkPolicies"}
	}
	return DoctorCheck{Name: "DNS NetworkPolicy portability", OK: true, Detail: fmt.Sprintf("runtime API NetworkPolicy includes detected DNS selector %s", matched)}
}

func checkStorageClassReadiness(kubectl core.KubectlRunner) DoctorCheck {
	want := strings.TrimSpace(os.Getenv("MCP_STORAGE_CLASS"))
	if want == "" {
		raw, err := readKubectlOutput(kubectl, []string{"get", "storageclass", "-o", "json"})
		if err != nil {
			return DoctorCheck{Name: "storage class readiness", OK: false, Detail: fmt.Sprintf("could not discover a default StorageClass: %v", err), Remedy: "set MCP_STORAGE_CLASS to an installed StorageClass or install a default storage provisioner"}
		}
		var payload struct {
			Items []struct {
				Metadata struct {
					Name        string            `json:"name"`
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return DoctorCheck{Name: "storage class readiness", OK: false, Detail: fmt.Sprintf("could not parse StorageClass inventory: %v", err), Remedy: "set MCP_STORAGE_CLASS to an installed StorageClass"}
		}
		for _, item := range payload.Items {
			if item.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || item.Metadata.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				want = strings.TrimSpace(item.Metadata.Name)
				break
			}
		}
		if want == "" {
			return DoctorCheck{Name: "storage class readiness", OK: false, Detail: "no default StorageClass is available and MCP_STORAGE_CLASS is unset", Remedy: "set MCP_STORAGE_CLASS to an installed StorageClass or install a default storage provisioner"}
		}
	}
	out, err := readKubectlOutput(kubectl, []string{"get", "storageclass", want, "-o", "jsonpath={.metadata.name}"})
	if err != nil || strings.TrimSpace(out) == "" {
		return DoctorCheck{Name: "storage class readiness", OK: false, Detail: fmt.Sprintf("configured StorageClass %q is not available", want), Remedy: "set MCP_STORAGE_CLASS to an installed StorageClass or install the configured storage provisioner"}
	}
	nodes, err := readKubectlOutput(kubectl, []string{"get", "nodes", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`})
	if err != nil {
		return DoctorCheck{Name: "storage class readiness", OK: false, Detail: fmt.Sprintf("StorageClass %q exists but node inventory could not be read: %v", want, err), Remedy: "check kubectl access and node status"}
	}
	if strings.Contains(strings.ToLower(want), "local") && len(uniqueNonEmptyLines(nodes)) > 1 {
		return DoctorCheck{Name: "storage class readiness", OK: false, Detail: fmt.Sprintf("local StorageClass %q is configured on a multi-node cluster", want), Remedy: "use a replicated/dynamic StorageClass or explicitly choose hostpath mode for single-node development"}
	}
	return DoctorCheck{Name: "storage class readiness", OK: true, Detail: fmt.Sprintf("StorageClass %q is available", want)}
}

func checkSentinelSecretConsumerFreshness(kubectl core.KubectlRunner) DoctorCheck {
	namespace, secretName, ok := platforminventory.CredentialPlacement("API_KEYS")
	if !ok {
		return DoctorCheck{Name: "sentinel secret consumer freshness", OK: false, Detail: "API_KEYS has no credential owner", Remedy: "restore the platform credential catalog"}
	}
	secretRaw, err := readKubectlOutput(kubectl, []string{"get", "secret", secretName, "-n", namespace, "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "sentinel secret consumer freshness", OK: true, Detail: secretName + " is not installed; skipping freshness check"}
	}
	var secret struct {
		Metadata struct {
			ManagedFields []struct {
				Time *time.Time `json:"time"`
			} `json:"managedFields"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(secretRaw), &secret); err != nil {
		return DoctorCheck{Name: "sentinel secret consumer freshness", OK: false, Detail: fmt.Sprintf("failed parsing %s metadata: %v", secretName, err), Remedy: "inspect the Secret metadata and redeploy Sentinel"}
	}
	var secretUpdated time.Time
	for _, field := range secret.Metadata.ManagedFields {
		if field.Time != nil && field.Time.After(secretUpdated) {
			secretUpdated = *field.Time
		}
	}
	if secretUpdated.IsZero() {
		return DoctorCheck{Name: "sentinel secret consumer freshness", OK: true, Detail: "Secret update timestamp is unavailable; runtime auth probes remain authoritative"}
	}
	podsRaw, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", componentNamespace("platform-api"), "-l", "app=mcp-runtime-api", "-o", `jsonpath={range .items[*]}{.metadata.name}|{.status.startTime}{"\n"}{end}`})
	if err != nil {
		return DoctorCheck{Name: "sentinel secret consumer freshness", OK: false, Detail: fmt.Sprintf("failed reading runtime-api pod start times: %v", err), Remedy: "inspect runtime-api pods and restart them after Secret changes"}
	}
	for _, line := range strings.Split(podsRaw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		started, parseErr := time.Parse(time.RFC3339, parts[1])
		if parseErr == nil && started.Before(secretUpdated) {
			return DoctorCheck{Name: "sentinel secret consumer freshness", OK: false, Detail: fmt.Sprintf("runtime-api pod %s started before the latest Secret update", parts[0]), Remedy: "roll out deployment/mcp-runtime-api and deployment/mcp-ui after changing " + secretName}
		}
	}
	return DoctorCheck{Name: "sentinel secret consumer freshness", OK: true, Detail: "runtime-api pods are not older than the latest Secret update"}
}

func checkSentinelOIDCConfiguration(kubectl core.KubectlRunner) DoctorCheck {
	configName := platforminventory.SharedConfigName
	raw, err := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "sentinel OIDC configuration", OK: true, Detail: configName + " not found; skipping OIDC configuration check"}
	}
	var config struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return DoctorCheck{Name: "sentinel OIDC configuration", OK: false, Detail: fmt.Sprintf("failed parsing %s: %v", configName, err), Remedy: "reapply the platform ConfigMap"}
	}
	mode := strings.TrimSpace(config.Data["PLATFORM_MODE"])
	if strings.EqualFold(strings.TrimSpace(config.Data["MCP_RUNTIME_TEST_MODE"]), "1") || strings.EqualFold(strings.TrimSpace(config.Data["MCP_RUNTIME_TEST_MODE"]), "true") {
		return DoctorCheck{Name: "sentinel OIDC configuration", OK: true, Detail: "MCP Runtime test mode is enabled; production OIDC configuration is not required"}
	}
	google, issuer, audience := strings.TrimSpace(config.Data["GOOGLE_CLIENT_ID"]), strings.TrimSpace(config.Data["OIDC_ISSUER"]), strings.TrimSpace(config.Data["OIDC_AUDIENCE"])
	if mode == "public" || mode == "tenant" {
		if google == "" && (issuer == "" || audience == "") {
			return DoctorCheck{Name: "sentinel OIDC configuration", OK: false, Detail: fmt.Sprintf("platform mode %q has incomplete Google/OIDC configuration", mode), Remedy: "configure GOOGLE_CLIENT_ID or both OIDC_ISSUER and OIDC_AUDIENCE; set OIDC_JWKS_URL only when issuer discovery is unavailable"}
		}
	}
	return DoctorCheck{Name: "sentinel OIDC configuration", OK: true, Detail: fmt.Sprintf("platform mode %q has a complete configured login contract", mode)}
}

func uniqueNonEmptyLines(raw string) []string {
	set := map[string]struct{}{}
	for _, line := range strings.Split(raw, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			set[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
