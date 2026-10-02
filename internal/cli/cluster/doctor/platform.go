package doctor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/platforminventory"
)

func readOwnedSecret(kubectl core.KubectlRunner, key string) (name, value string, err error) {
	namespace, name, ok := platforminventory.CredentialPlacement(key)
	if !ok {
		return "", "", fmt.Errorf("credential %q has no owner", key)
	}
	value, err = readKubectlOutput(kubectl, []string{"get", "secret", name, "-n", namespace, "-o", "jsonpath={.data." + key + "}"})
	return name, value, err
}

func checkSentinelSecrets(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     true,
			Detail: "namespace mcp-platform not found; skipping sentinel secret checks",
		}
	}
	apiSecret, apiKeysB64, err := readOwnedSecret(kubectl, "API_KEYS")
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "secret " + apiSecret + " missing or API_KEYS key absent",
			Remedy: "rerun setup so each credential set is rendered for its owner",
		}
	}
	_, adminAPIKeysB64, err := readOwnedSecret(kubectl, "ADMIN_API_KEYS")
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "owner secret missing ADMIN_API_KEYS key",
			Remedy: "rerun setup so platform-api credentials include ADMIN_API_KEYS and UI_API_KEY",
		}
	}
	_, ingestAPIKeysB64, err := readOwnedSecret(kubectl, "INGEST_API_KEYS")
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "owner secret missing INGEST_API_KEYS key",
			Remedy: "rerun setup so the ingest credential set includes INGEST_API_KEYS",
		}
	}
	_, uiKeyB64, err := readOwnedSecret(kubectl, "UI_API_KEY")
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "owner secret missing UI_API_KEY key",
			Remedy: "rerun setup so the UI credential set includes UI_API_KEY",
		}
	}
	apiKeys, decodeCheck := decodeSentinelSecretValue("API_KEYS", apiKeysB64)
	if decodeCheck != nil {
		return *decodeCheck
	}
	adminAPIKeys, decodeCheck := decodeSentinelSecretValue("ADMIN_API_KEYS", adminAPIKeysB64)
	if decodeCheck != nil {
		return *decodeCheck
	}
	ingestAPIKeys, decodeCheck := decodeSentinelSecretValue("INGEST_API_KEYS", ingestAPIKeysB64)
	if decodeCheck != nil {
		return *decodeCheck
	}
	uiKey, decodeCheck := decodeSentinelSecretValue("UI_API_KEY", uiKeyB64)
	if decodeCheck != nil {
		return *decodeCheck
	}
	if apiKeys == "" || adminAPIKeys == "" || ingestAPIKeys == "" || uiKey == "" {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "API_KEYS, ADMIN_API_KEYS, INGEST_API_KEYS, or UI_API_KEY is empty",
			Remedy: "populate non-empty API_KEYS, ADMIN_API_KEYS, INGEST_API_KEYS, and UI_API_KEY on their owner Secrets",
		}
	}
	keys := splitCommaTrim(apiKeys)
	adminKeys := splitCommaTrim(adminAPIKeys)
	uiInAPIKeys := false
	for _, k := range keys {
		if k == uiKey {
			uiInAPIKeys = true
			break
		}
	}
	if !uiInAPIKeys {
		return DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: "UI_API_KEY not present in API_KEYS",
			Remedy: "align API_KEYS and UI_API_KEY on the platform-api and UI credential sets",
		}
	}
	for _, k := range adminKeys {
		if k == uiKey {
			return DoctorCheck{
				Name:   "sentinel secrets",
				OK:     true,
				Detail: "UI_API_KEY is present in API_KEYS and ADMIN_API_KEYS; INGEST_API_KEYS is populated separately",
			}
		}
	}
	return DoctorCheck{
		Name:   "sentinel secrets",
		OK:     false,
		Detail: "UI_API_KEY not present in ADMIN_API_KEYS",
		Remedy: "include UI_API_KEY in ADMIN_API_KEYS for browser admin access",
	}
}

func checkSentinelAPIAuthProbe(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     true,
			Detail: "namespace mcp-platform not found; skipping auth probe",
		}
	}
	uiSecret, apiKeyB64, err := readOwnedSecret(kubectl, "UI_API_KEY")
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: "UI_API_KEY not available in " + uiSecret,
			Remedy: "configure UI_API_KEY before probing API auth",
		}
	}
	apiKey, err := decodeBase64(apiKeyB64)
	if err != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: fmt.Sprintf("UI_API_KEY in %s is not valid base64: %v", uiSecret, err),
			Remedy: "patch " + uiSecret + " with valid Kubernetes secret data for UI_API_KEY",
		}
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: "UI_API_KEY decoded to empty value",
			Remedy: "set non-empty UI_API_KEY in " + uiSecret,
		}
	}
	podName := fmt.Sprintf("doctor-sentinel-probe-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	curlArgs := []string{
		"-sS", "-o", "doctor-response",
		"-w", "%{http_code}",
		"--connect-timeout", "5",
		"--max-time", "20",
		"-H", "x-api-key: " + apiKey,
		fmt.Sprintf("http://%s:%d/api/v1/runtime/components", doctorServiceDNS(doctorRuntimeAPIService, componentNamespace("platform-api")), doctorRuntimeAPIPort),
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", componentNamespace("platform-api"), "--ignore-not-found"})
	}()
	cmd, cmdErr := kubectl.CommandArgs([]string{
		"run", podName,
		"-n", componentNamespace("platform-api"),
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", curlArgs...),
	})
	if cmdErr != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", cmdErr),
			Remedy: "check kubectl connectivity and helper image access",
		}
	}
	createOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: fmt.Sprintf("failed creating auth probe pod: %v: %s", runErr, strings.TrimSpace(string(createOut))),
			Remedy: "verify sentinel API deployment/service and API key config",
		}
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, componentNamespace("platform-api"), 90*time.Second); err != nil {
		logs, _ := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api"), "--tail=50"})
		detail := fmt.Sprintf("auth probe pod did not complete: %v", err)
		if strings.TrimSpace(logs) != "" {
			detail += ": " + strings.TrimSpace(logs)
		}
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: detail,
			Remedy: "verify sentinel API deployment/service and API key config",
		}
	}
	out, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api")})
	if logsErr != nil {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     false,
			Detail: fmt.Sprintf("failed reading auth probe logs: %v", logsErr),
			Remedy: "inspect auth probe pod logs",
		}
	}
	status := strings.TrimSpace(out)
	if status == "200" {
		return DoctorCheck{
			Name:   "sentinel API auth probe",
			OK:     true,
			Detail: "authenticated probe returned HTTP 200",
		}
	}
	return DoctorCheck{
		Name:   "sentinel API auth probe",
		OK:     false,
		Detail: fmt.Sprintf("authenticated probe returned HTTP %s", status),
		Remedy: "verify API key and sentinel API route availability",
	}
}

// checkSentinelRuntimeCatalogProbe exercises the endpoints the dashboard uses
// for its server catalog. Readiness alone cannot detect a broken Kubernetes
// API route, an incomplete NetworkPolicy, or an API key accepted by the UI but
// rejected by runtime-api.
func checkSentinelRuntimeCatalogProbe(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: true, Detail: "namespace mcp-platform not found; skipping runtime catalog probe"}
	}
	adminSecret, encoded, err := readOwnedSecret(kubectl, "ADMIN_API_KEYS")
	if err != nil {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: "ADMIN_API_KEYS not available in " + adminSecret, Remedy: "configure an admin API key before probing runtime catalog routes"}
	}
	decoded, err := decodeBase64(encoded)
	if err != nil {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: fmt.Sprintf("ADMIN_API_KEYS is not valid base64: %v", err), Remedy: "patch " + adminSecret + " with valid Kubernetes secret data"}
	}
	adminKeys := splitCommaTrim(decoded)
	_, apiKeysEncoded, err := readOwnedSecret(kubectl, "API_KEYS")
	if err != nil {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: "API_KEYS not available in its owner Secret", Remedy: "configure the admin probe key in both API_KEYS and ADMIN_API_KEYS"}
	}
	apiKeysDecoded, err := decodeBase64(apiKeysEncoded)
	if err != nil {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: fmt.Sprintf("API_KEYS is not valid base64: %v", err), Remedy: "patch the owner Secret with valid Kubernetes secret data"}
	}
	apiKeys := splitCommaTrim(apiKeysDecoded)
	keys := intersectSecretKeys(apiKeys, adminKeys)
	if len(keys) == 0 {
		return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: "API_KEYS and ADMIN_API_KEYS have no common key", Remedy: "include at least one identical admin credential in both API_KEYS and ADMIN_API_KEYS"}
	}
	for _, path := range []string{"/api/v1/runtime/servers", "/api/v1/runtime/tools"} {
		status, probeErr := runSentinelAuthenticatedProbe(kubectl, keys[0], path)
		if probeErr != nil {
			return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: fmt.Sprintf("%s probe failed: %v", path, probeErr), Remedy: "inspect runtime-api NetworkPolicy, Kubernetes API reachability, service endpoints, and API_KEYS/ADMIN_API_KEYS alignment"}
		}
		if status != "200" {
			return DoctorCheck{Name: "sentinel runtime catalog probe", OK: false, Detail: fmt.Sprintf("%s returned HTTP %s", path, status), Remedy: "verify runtime-api authentication and catalog dependencies; /health can remain green while these routes fail"}
		}
	}
	return DoctorCheck{Name: "sentinel runtime catalog probe", OK: true, Detail: "authenticated runtime servers and tools catalog endpoints returned HTTP 200"}
}

func intersectSecretKeys(apiKeys, adminKeys []string) []string {
	adminSet := make(map[string]struct{}, len(adminKeys))
	for _, key := range adminKeys {
		adminSet[key] = struct{}{}
	}
	keys := make([]string, 0)
	for _, key := range apiKeys {
		if _, ok := adminSet[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func runSentinelAuthenticatedProbe(kubectl core.KubectlRunner, apiKey, path string) (string, error) {
	podName := fmt.Sprintf("doctor-sentinel-catalog-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	curlArgs := []string{
		"-sS", "-o", "/tmp/doctor-response", "-w", "%{http_code}",
		"--connect-timeout", "5", "--max-time", "20",
		"-H", "x-api-key: " + apiKey,
		fmt.Sprintf("http://%s:%d%s", doctorServiceDNS(doctorRuntimeAPIService, componentNamespace("platform-api")), doctorRuntimeAPIPort, path),
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", componentNamespace("platform-api"), "--ignore-not-found"})
	}()
	cmd, err := kubectl.CommandArgs([]string{
		"run", podName, "-n", componentNamespace("platform-api"), "--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", curlArgs...),
	})
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed creating probe pod: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, componentNamespace("platform-api"), 90*time.Second); err != nil {
		return "", err
	}
	logs, err := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api")})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(logs), nil
}

func checkSentinelPlatformAPIReadiness(kubectl core.KubectlRunner) DoctorCheck {
	return checkSentinelServiceHealthReadiness(
		kubectl,
		"sentinel platform API readiness",
		doctorPlatformAPIService,
		doctorPlatformAPIPort,
		componentNamespace("platform-api"),
	)
}

func checkSentinelAnalyticsAPIReadiness(kubectl core.KubectlRunner) DoctorCheck {
	return checkSentinelServiceHealthReadiness(
		kubectl,
		"sentinel analytics API readiness",
		doctorAnalyticsAPIService,
		doctorAnalyticsAPIPort,
		componentNamespace("analytics-api"),
	)
}

func checkSentinelServiceHealthReadiness(kubectl core.KubectlRunner, checkName, service string, port int, namespace string) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     true,
			Detail: fmt.Sprintf("namespace %s not found; skipping service readiness check", namespace),
		}
	}
	pair, ready, err := doctorDeploymentReplicaStatus(kubectl, namespace, service)
	if err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("failed reading deployment %s: %v", service, err),
			Remedy: fmt.Sprintf("inspect `kubectl -n %s get deploy/%s`", namespace, service),
		}
	}
	if !ready {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("%s replicas ready", pair),
			Remedy: fmt.Sprintf("inspect `kubectl -n %s get pods -l app=%s`", namespace, service),
		}
	}

	baseURL := fmt.Sprintf("http://%s:%d", doctorServiceDNS(service, namespace), port)
	for _, path := range []string{"/health", "/ready"} {
		status, probeErr := doctorCurlServiceEndpoint(kubectl, baseURL+path)
		if probeErr != nil {
			return DoctorCheck{
				Name:   checkName,
				OK:     false,
				Detail: probeErr.Error(),
				Remedy: fmt.Sprintf("verify %s deployment/service and %s endpoint", service, path),
			}
		}
		if status != "200" {
			return DoctorCheck{
				Name:   checkName,
				OK:     false,
				Detail: fmt.Sprintf("%s returned HTTP %s", path, status),
				Remedy: fmt.Sprintf("verify %s deployment/service and %s endpoint", service, path),
			}
		}
	}
	return DoctorCheck{
		Name:   checkName,
		OK:     true,
		Detail: fmt.Sprintf("%s replicas ready; /health and /ready returned HTTP 200", pair),
	}
}

func doctorCurlServiceEndpoint(kubectl core.KubectlRunner, url string) (string, error) {
	podName := fmt.Sprintf("doctor-sentinel-probe-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	curlArgs := []string{
		"-sS", "-o", "/dev/null",
		"-w", "%{http_code}",
		"--connect-timeout", "5",
		"--max-time", "20",
		url,
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", componentNamespace("platform-api"), "--ignore-not-found"})
	}()
	cmd, cmdErr := kubectl.CommandArgs([]string{
		"run", podName,
		"-n", componentNamespace("platform-api"),
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", curlArgs...),
	})
	if cmdErr != nil {
		return "", fmt.Errorf("kubectl error: %v", cmdErr)
	}
	createOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", fmt.Errorf("failed creating probe pod: %v: %s", runErr, strings.TrimSpace(string(createOut)))
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, componentNamespace("platform-api"), 90*time.Second); err != nil {
		logs, _ := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api"), "--tail=50"})
		detail := fmt.Sprintf("probe pod did not complete: %v", err)
		if strings.TrimSpace(logs) != "" {
			detail += ": " + strings.TrimSpace(logs)
		}
		return "", fmt.Errorf("%s", detail)
	}
	out, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api")})
	if logsErr != nil {
		return "", fmt.Errorf("failed reading probe logs: %v", logsErr)
	}
	return strings.TrimSpace(out), nil
}

func decodeSentinelSecretValue(key, encoded string) (string, *DoctorCheck) {
	decoded, err := decodeBase64(encoded)
	if err != nil {
		return "", &DoctorCheck{
			Name:   "sentinel secrets",
			OK:     false,
			Detail: fmt.Sprintf("%s in its owner Secret is not valid base64: %v", key, err),
			Remedy: "patch the owner Secret with valid Kubernetes secret data for API, admin, ingest, and UI keys",
		}
	}
	return strings.TrimSpace(decoded), nil
}

func checkNodeCapacity(kubectl core.KubectlRunner) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"top", "nodes", "--no-headers"})
	if err == nil {
		out, topErr := cmd.CombinedOutput()
		if topErr == nil {
			lines := filterNonEmptyLines(string(out))
			if len(lines) == 0 {
				return DoctorCheck{Name: "node capacity", OK: false, Detail: "no node metrics returned", Remedy: "check metrics-server installation"}
			}
			hot := make([]string, 0, len(lines))
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) < 5 {
					continue
				}
				cpuPct := strings.TrimSuffix(fields[2], "%")
				memPct := strings.TrimSuffix(fields[4], "%")
				cpu, _ := strconv.Atoi(cpuPct)
				mem, _ := strconv.Atoi(memPct)
				if cpu >= 95 || mem >= 95 {
					hot = append(hot, fmt.Sprintf("%s(cpu=%d%% mem=%d%%)", fields[0], cpu, mem))
				}
			}
			if len(hot) > 0 {
				return DoctorCheck{
					Name:   "node capacity",
					OK:     false,
					Detail: "high node utilization: " + strings.Join(hot, ", "),
					Remedy: "scale cluster capacity or reduce workload requests",
				}
			}
			return DoctorCheck{
				Name:   "node capacity",
				OK:     true,
				Detail: fmt.Sprintf("metrics available for %d node(s); utilization below 95%%", len(lines)),
			}
		}
	}

	alloc, allocErr := readKubectlOutput(kubectl, []string{"get", "nodes", "-o", "custom-columns=NAME:.metadata.name,ALLOC_CPU:.status.allocatable.cpu,ALLOC_MEM:.status.allocatable.memory", "--no-headers"})
	if allocErr != nil {
		return DoctorCheck{
			Name:   "node capacity",
			OK:     false,
			Detail: fmt.Sprintf("failed to read node allocatable resources: %v", allocErr),
			Remedy: "check cluster node readiness and kubectl permissions",
		}
	}
	lines := filterNonEmptyLines(alloc)
	if len(lines) == 0 {
		return DoctorCheck{
			Name:   "node capacity",
			OK:     false,
			Detail: "no nodes returned by API",
			Remedy: "check cluster connection",
		}
	}
	return DoctorCheck{
		Name:   "node capacity",
		OK:     true,
		Detail: fmt.Sprintf("allocatable resources visible on %d node(s) (metrics-server unavailable)", len(lines)),
	}
}

func checkPendingPodsByNamespace(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "pods", "-A", "--field-selector=status.phase=Pending", "-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name", "--no-headers"})
	if err != nil {
		return DoctorCheck{
			Name:   "pending pods",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check API connectivity and RBAC for listing pods",
		}
	}
	lines := filterNonEmptyLines(out)
	if len(lines) == 0 {
		return DoctorCheck{
			Name:   "pending pods",
			OK:     true,
			Detail: "no Pending pods across namespaces",
		}
	}
	counts := map[string]int{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		counts[fields[0]]++
	}
	summary := make([]string, 0, len(counts))
	for ns, count := range counts {
		summary = append(summary, fmt.Sprintf("%s=%d", ns, count))
	}
	return DoctorCheck{
		Name:   "pending pods",
		OK:     false,
		Detail: fmt.Sprintf("%d pending pods detected (%s)", len(lines), strings.Join(summary, ", ")),
		Remedy: "inspect pending pods/events: `kubectl get pods -A --field-selector=status.phase=Pending`",
	}
}
