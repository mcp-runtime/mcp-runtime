package doctor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/metadata"
)

const doctorManagedTeamRegistryPullSecret = "mcp-runtime-registry-pull" // #nosec G101 -- Kubernetes Secret object name, not credential material.

var internalRegistryRefPattern = regexp.MustCompile(`(?:registry\.registry\.svc\.cluster\.local:5000|(?:10(?:\.\d{1,3}){3}|192\.168(?:\.\d{1,3}){2}|172\.(?:1[6-9]|2[0-9]|3[0-1])(?:\.\d{1,3}){2}):\d+)(?:/|")`)
var ipv4AddressPattern = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)

func checkManagedTeamWorkloadServiceAccounts(kubectl core.KubectlRunner) DoctorCheck {
	namespaces, err := managedTeamNamespaces(kubectl)
	if err != nil {
		return DoctorCheck{
			Name:   "team workload serviceaccounts",
			OK:     false,
			Detail: fmt.Sprintf("failed listing team namespaces: %v", err),
			Remedy: "check Kubernetes API access and RBAC for listing namespaces",
		}
	}
	if len(namespaces) == 0 {
		return DoctorCheck{
			Name:   "team workload serviceaccounts",
			OK:     true,
			Detail: "no managed team namespaces found",
		}
	}

	for _, namespace := range namespaces {
		out, err := readKubectlOutput(kubectl, []string{
			"get", "serviceaccount", kubeworkload.DefaultServiceAccountName,
			"-n", namespace,
			"-o", "json",
		})
		if err != nil {
			return DoctorCheck{
				Name:   "team workload serviceaccounts",
				OK:     false,
				Detail: fmt.Sprintf("serviceaccount %s missing in %s: %v", kubeworkload.DefaultServiceAccountName, namespace, err),
				Remedy: "recreate the team namespace through `mcp-runtime team create` or repair the serviceaccount",
			}
		}
		var sa struct {
			ImagePullSecrets []struct {
				Name string `json:"name"`
			} `json:"imagePullSecrets"`
		}
		if err := json.Unmarshal([]byte(out), &sa); err != nil {
			return DoctorCheck{
				Name:   "team workload serviceaccounts",
				OK:     false,
				Detail: fmt.Sprintf("failed parsing serviceaccount %s/%s: %v", namespace, kubeworkload.DefaultServiceAccountName, err),
				Remedy: "rerun cluster doctor; if this persists, inspect `kubectl get sa -n " + namespace + " " + kubeworkload.DefaultServiceAccountName + " -o json`",
			}
		}
		found := false
		for _, ref := range sa.ImagePullSecrets {
			if strings.TrimSpace(ref.Name) == doctorManagedTeamRegistryPullSecret {
				found = true
				break
			}
		}
		if !found {
			return DoctorCheck{
				Name:   "team workload serviceaccounts",
				OK:     false,
				Detail: fmt.Sprintf("serviceaccount %s in %s is missing imagePullSecret %s", kubeworkload.DefaultServiceAccountName, namespace, doctorManagedTeamRegistryPullSecret),
				Remedy: "rerun team provisioning or patch the serviceaccount to include the managed registry pull Secret",
			}
		}
	}

	return DoctorCheck{
		Name:   "team workload serviceaccounts",
		OK:     true,
		Detail: fmt.Sprintf("%d managed team namespace(s) have %s wired to %s", len(namespaces), doctorManagedTeamRegistryPullSecret, kubeworkload.DefaultServiceAccountName),
	}
}

func checkManagedTeamRegistryPullSecrets(kubectl core.KubectlRunner) DoctorCheck {
	namespaces, err := managedTeamNamespaces(kubectl)
	if err != nil {
		return DoctorCheck{
			Name:   "team registry pull secrets",
			OK:     false,
			Detail: fmt.Sprintf("failed listing team namespaces: %v", err),
			Remedy: "check Kubernetes API access and RBAC for listing namespaces",
		}
	}
	if len(namespaces) == 0 {
		return DoctorCheck{
			Name:   "team registry pull secrets",
			OK:     true,
			Detail: "no managed team namespaces found",
		}
	}

	for _, namespace := range namespaces {
		out, err := readKubectlOutput(kubectl, []string{
			"get", "secret", doctorManagedTeamRegistryPullSecret,
			"-n", namespace,
			"-o", "json",
		})
		if err != nil {
			return DoctorCheck{
				Name:   "team registry pull secrets",
				OK:     false,
				Detail: fmt.Sprintf("secret %s missing in %s: %v", doctorManagedTeamRegistryPullSecret, namespace, err),
				Remedy: "rerun team provisioning so the managed namespace pull secret is created",
			}
		}
		var secret struct {
			Type string            `json:"type"`
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &secret); err != nil {
			return DoctorCheck{
				Name:   "team registry pull secrets",
				OK:     false,
				Detail: fmt.Sprintf("failed parsing Secret %s/%s: %v", namespace, doctorManagedTeamRegistryPullSecret, err),
				Remedy: "inspect the Secret with `kubectl get secret -n " + namespace + " " + doctorManagedTeamRegistryPullSecret + " -o json`",
			}
		}
		if strings.TrimSpace(secret.Type) != "kubernetes.io/dockerconfigjson" {
			return DoctorCheck{
				Name:   "team registry pull secrets",
				OK:     false,
				Detail: fmt.Sprintf("secret %s/%s has type %q, want kubernetes.io/dockerconfigjson", namespace, doctorManagedTeamRegistryPullSecret, strings.TrimSpace(secret.Type)),
				Remedy: "recreate the registry pull Secret as a dockerconfigjson Secret",
			}
		}
		encoded := strings.TrimSpace(secret.Data[".dockerconfigjson"])
		if encoded == "" {
			return DoctorCheck{
				Name:   "team registry pull secrets",
				OK:     false,
				Detail: fmt.Sprintf("secret %s/%s is missing .dockerconfigjson data", namespace, doctorManagedTeamRegistryPullSecret),
				Remedy: "recreate the registry pull Secret with valid dockerconfigjson credentials",
			}
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || strings.TrimSpace(string(decoded)) == "" {
			return DoctorCheck{
				Name:   "team registry pull secrets",
				OK:     false,
				Detail: fmt.Sprintf("secret %s/%s has invalid .dockerconfigjson data", namespace, doctorManagedTeamRegistryPullSecret),
				Remedy: "recreate the registry pull Secret with valid dockerconfigjson credentials",
			}
		}
	}

	return DoctorCheck{
		Name:   "team registry pull secrets",
		OK:     true,
		Detail: fmt.Sprintf("%d managed team namespace(s) have valid %s dockerconfig Secrets", len(namespaces), doctorManagedTeamRegistryPullSecret),
	}
}

func checkOperatorRegistryEndpoint(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{
		"get", "deploy", "mcp-runtime-operator-controller-manager",
		"-n", "mcp-runtime",
		"-o", "json",
	})
	if err != nil {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     false,
			Detail: fmt.Sprintf("failed reading operator deployment: %v", err),
			Remedy: "check operator deployment health and Kubernetes API access",
		}
	}
	var deploy struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []doctorContainer `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &deploy); err != nil {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     false,
			Detail: fmt.Sprintf("failed parsing operator deployment: %v", err),
			Remedy: "rerun cluster doctor; if this persists, inspect `kubectl -n mcp-runtime get deploy mcp-runtime-operator-controller-manager -o json`",
		}
	}
	if len(deploy.Spec.Template.Spec.Containers) == 0 {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     false,
			Detail: "operator deployment has no containers",
			Remedy: "inspect the operator Deployment manifest",
		}
	}
	env := deploy.Spec.Template.Spec.Containers[0].Env
	endpoint := envValue(env, "MCP_REGISTRY_ENDPOINT")
	ingressHost := envValue(env, "MCP_REGISTRY_INGRESS_HOST")
	if endpoint == "" {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     true,
			Detail: fmt.Sprintf("MCP_REGISTRY_ENDPOINT not set; operator default is %s", metadata.ResolveRegistryPullHost()),
		}
	}
	if ingressHost != "" && strings.EqualFold(endpoint, ingressHost) {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     true,
			Detail: fmt.Sprintf("MCP_REGISTRY_ENDPOINT=%s matches MCP_REGISTRY_INGRESS_HOST; valid when nodes can resolve and trust the bundled HTTPS registry hostname", endpoint),
		}
	}
	if doctorLooksLikePublicRegistryHost(endpoint) {
		return DoctorCheck{
			Name:   "operator registry endpoint",
			OK:     true,
			Detail: fmt.Sprintf("MCP_REGISTRY_ENDPOINT=%s is a hostname endpoint; verify node DNS/TLS with image-pull diagnostics below", endpoint),
		}
	}
	return DoctorCheck{
		Name:   "operator registry endpoint",
		OK:     true,
		Detail: fmt.Sprintf("MCP_REGISTRY_ENDPOINT=%s", endpoint),
	}
}

func checkPlatformKafkaReadiness(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("kafka"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "platform Kafka readiness", OK: true, Detail: "namespace mcp-observability not found; skipping Kafka readiness"}
	}
	pair, ready, err := doctorStatefulSetReplicaStatus(kubectl, componentNamespace("kafka"), "kafka")
	if err != nil {
		return DoctorCheck{
			Name:   "platform Kafka readiness",
			OK:     false,
			Detail: err.Error(),
			Remedy: "inspect Kafka rollout, logs, and PVC state in mcp-observability",
		}
	}
	if !ready {
		return DoctorCheck{
			Name:   "platform Kafka readiness",
			OK:     false,
			Detail: fmt.Sprintf("%s replicas ready", pair),
			Remedy: "inspect `kubectl -n mcp-observability get pods`, `kubectl -n mcp-observability logs kafka-0 --previous`, and the Kafka PVC state",
		}
	}
	return DoctorCheck{
		Name:   "platform Kafka readiness",
		OK:     true,
		Detail: fmt.Sprintf("%s replicas ready", pair),
	}
}

func checkPlatformIngestReadiness(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("ingest"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "platform ingest readiness", OK: true, Detail: "namespace mcp-observability not found; skipping ingest readiness"}
	}
	pair, ready, err := doctorDeploymentReplicaStatus(kubectl, componentNamespace("ingest"), "mcp-ingest")
	if err != nil {
		return DoctorCheck{
			Name:   "platform ingest readiness",
			OK:     false,
			Detail: err.Error(),
			Remedy: "inspect the ingest deployment and pod logs in mcp-observability",
		}
	}
	if !ready {
		return DoctorCheck{
			Name:   "platform ingest readiness",
			OK:     false,
			Detail: fmt.Sprintf("%s replicas ready", pair),
			Remedy: "inspect `kubectl -n mcp-observability get pods`, `kubectl -n mcp-observability logs deploy/mcp-ingest`, and Kafka readiness",
		}
	}
	return DoctorCheck{
		Name:   "platform ingest readiness",
		OK:     true,
		Detail: fmt.Sprintf("%s replicas ready", pair),
	}
}

func checkRuntimeAPIImageDisplayRefs(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     true,
			Detail: "namespace mcp-platform not found; skipping runtime API image display check",
		}
	}
	uiSecret, apiKeyB64, err := readOwnedSecret(kubectl, "UI_API_KEY")
	if err != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: "UI_API_KEY not available in " + uiSecret,
			Remedy: "configure UI_API_KEY before probing runtime API responses",
		}
	}
	apiKey, err := decodeBase64(apiKeyB64)
	if err != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("UI_API_KEY in %s is not valid base64: %v", uiSecret, err),
			Remedy: "patch " + uiSecret + " with valid Kubernetes secret data for UI_API_KEY",
		}
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: "UI_API_KEY decoded to empty value",
			Remedy: "set non-empty UI_API_KEY in " + uiSecret,
		}
	}

	podName := fmt.Sprintf("doctor-runtime-servers-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", componentNamespace("platform-api"), "--ignore-not-found"})
	}()
	cmd, cmdErr := kubectl.CommandArgs([]string{
		"run", podName,
		"-n", componentNamespace("platform-api"),
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "sh", "-c", fmt.Sprintf(
			"status=$(curl -sS -o doctor-response.json -w '%%{http_code}' --connect-timeout 5 --max-time 20 -H %q %q); cat doctor-response.json; printf '\\nHTTP_STATUS=%%s\\n' \"$status\"",
			"x-api-key: "+apiKey,
			fmt.Sprintf("http://%s:%d/api/v1/runtime/servers", doctorServiceDNS(doctorRuntimeAPIService, componentNamespace("platform-api")), doctorRuntimeAPIPort),
		)),
	})
	if cmdErr != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", cmdErr),
			Remedy: "check kubectl connectivity and helper image access",
		}
	}
	createOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("failed creating runtime API probe pod: %v: %s", runErr, strings.TrimSpace(string(createOut))),
			Remedy: "verify platform API deployment/service and UI_API_KEY config",
		}
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, componentNamespace("platform-api"), 90*time.Second); err != nil {
		logs, _ := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api"), "--tail=80"})
		detail := fmt.Sprintf("runtime API probe pod did not complete: %v", err)
		if strings.TrimSpace(logs) != "" {
			detail += ": " + strings.TrimSpace(logs)
		}
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: detail,
			Remedy: "verify platform API deployment/service and runtime API route availability",
		}
	}
	out, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", componentNamespace("platform-api")})
	if logsErr != nil {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("failed reading runtime API probe logs: %v", logsErr),
			Remedy: "inspect the runtime API probe pod logs",
		}
	}
	if !strings.Contains(out, "HTTP_STATUS=200") {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("runtime API probe did not return HTTP 200: %s", strings.TrimSpace(out)),
			Remedy: "verify runtime API route availability and auth",
		}
	}
	if match := internalRegistryRefPattern.FindString(out); match != "" {
		return DoctorCheck{
			Name:   "runtime API image display refs",
			OK:     false,
			Detail: fmt.Sprintf("runtime API response leaked internal registry reference %s", strings.TrimSuffix(match, `"`)),
			Remedy: "sanitize user-facing runtime API image refs so internal pull hosts do not appear in UI/API responses",
		}
	}
	return DoctorCheck{
		Name:   "runtime API image display refs",
		OK:     true,
		Detail: "runtime API server listings do not expose internal registry hosts",
	}
}

func managedTeamNamespaces(kubectl core.KubectlRunner) ([]string, error) {
	out, err := readKubectlOutput(kubectl, []string{"get", "namespaces", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}"})
	if err != nil {
		return nil, err
	}
	var namespaces []string
	for _, line := range filterNonEmptyLines(out) {
		namespace := strings.TrimSpace(line)
		if strings.HasPrefix(namespace, "mcp-team-") {
			namespaces = append(namespaces, namespace)
		}
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

func doctorLooksLikePublicRegistryHost(endpoint string) bool {
	host := strings.TrimSpace(endpoint)
	if host == "" {
		return false
	}
	base := host
	if h, _, found := strings.Cut(host, ":"); found && !strings.Contains(h, ".svc.") && !ipv4AddressPattern.MatchString(h) {
		base = h
	}
	if strings.Contains(base, ".svc.") || strings.EqualFold(base, "localhost") {
		return false
	}
	if ipv4AddressPattern.MatchString(base) {
		return false
	}
	return !strings.Contains(host, ":5000")
}

func doctorStatefulSetReplicaStatus(kubectl core.KubectlRunner, namespace, name string) (string, bool, error) {
	out, err := readKubectlOutput(kubectl, []string{
		"get", "statefulset", name,
		"-n", namespace,
		"-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}",
	})
	if err != nil {
		return "", false, fmt.Errorf("failed reading statefulset %s/%s replica status: %w", namespace, name, err)
	}
	pair := strings.TrimSpace(out)
	parts := strings.SplitN(pair, "/", 2)
	if len(parts) != 2 {
		return "", false, fmt.Errorf("unexpected replica status %q for statefulset %s/%s", pair, namespace, name)
	}
	ready := strings.TrimSpace(parts[0])
	desired := strings.TrimSpace(parts[1])
	if ready == "" {
		ready = "0"
		pair = ready + "/" + desired
	}
	return pair, ready == desired && desired != "", nil
}
