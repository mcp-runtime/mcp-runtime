package doctor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/mcpdefaults"
)

// checkGatewayAnalyticsCredentials verifies every analytics-enabled gateway
// sidecar can authenticate to ingest: a projected ServiceAccount token for the
// ingest audience running as the mcp-workload ServiceAccount, or an explicit
// ANALYTICS_API_KEY override that resolves to a populated Secret.
func checkGatewayAnalyticsCredentials(kubectl core.KubectlRunner) DoctorCheck {
	const checkName = "gateway analytics credentials"
	deployments, check := listGatewayAnalyticsDeployments(kubectl, checkName)
	if check != nil {
		return *check
	}

	failures := make([]string, 0)
	for _, deployment := range deployments {
		id := deployment.Metadata.Namespace + "/" + deployment.Metadata.Name
		container, _ := deployment.gatewayContainer()
		if _, hasKey := envByName(container.Env, "ANALYTICS_API_KEY"); hasKey {
			if msg := analyticsAPIKeyProblem(kubectl, deployment, container); msg != "" {
				failures = append(failures, id+" "+msg)
			}
			continue
		}
		if msg := analyticsTokenProblem(deployment, container); msg != "" {
			failures = append(failures, id+" "+msg)
		}
	}

	if len(failures) > 0 {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: strings.Join(limitStrings(failures, 4), "; "),
			Remedy: "update the operator and let it reconcile the MCPServer (or redeploy with `mcp-runtime server deploy`) so the gateway gets a projected token for audience " + mcpdefaults.AnalyticsTokenAudience + " under the " + kubeworkload.DefaultServiceAccountName + " ServiceAccount",
		}
	}
	if len(deployments) == 0 {
		return DoctorCheck{
			Name:   checkName,
			OK:     true,
			Detail: "no gateway sidecars with analytics ingest URLs found",
		}
	}
	return DoctorCheck{
		Name:   checkName,
		OK:     true,
		Detail: fmt.Sprintf("%d gateway sidecar(s) can authenticate to ingest", len(deployments)),
	}
}

// listGatewayAnalyticsDeployments returns Deployments whose mcp-gateway
// container emits analytics. A non-nil check is a terminal result.
func listGatewayAnalyticsDeployments(kubectl core.KubectlRunner, checkName string) ([]doctorDeployment, *DoctorCheck) {
	out, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-A", "-o", "json"})
	if err != nil {
		return nil, &DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("failed listing deployments: %v", err),
			Remedy: "check Kubernetes API access and RBAC for listing Deployments",
		}
	}
	var list doctorDeploymentList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, &DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("failed parsing deployments JSON: %v", err),
			Remedy: "rerun cluster doctor; if this persists, inspect `kubectl get deploy -A -o json`",
		}
	}
	var out2 []doctorDeployment
	for _, deployment := range list.Items {
		if container, ok := deployment.gatewayContainer(); ok && envValue(container.Env, "ANALYTICS_INGEST_URL") != "" {
			out2 = append(out2, deployment)
		}
	}
	return out2, nil
}

func analyticsAPIKeyProblem(kubectl core.KubectlRunner, deployment doctorDeployment, container doctorContainer) string {
	apiKeyEnv, _ := envByName(container.Env, "ANALYTICS_API_KEY")
	if strings.TrimSpace(apiKeyEnv.Value) != "" {
		return ""
	}
	if apiKeyEnv.ValueFrom == nil || apiKeyEnv.ValueFrom.SecretKeyRef == nil {
		return "ANALYTICS_API_KEY has no value or secretKeyRef"
	}
	ref := apiKeyEnv.ValueFrom.SecretKeyRef
	if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" {
		return "ANALYTICS_API_KEY secretKeyRef is incomplete"
	}
	if err := checkSecretKeyPopulated(kubectl, deployment.Metadata.Namespace, ref.Name, ref.Key); err != nil {
		return fmt.Sprintf("references unusable Secret %s/%s: %v", ref.Name, ref.Key, err)
	}
	return ""
}

// analyticsTokenProblem explains why a gateway's projected-token wiring would
// make ingest reject it, or returns "" when the wiring is sound.
func analyticsTokenProblem(deployment doctorDeployment, container doctorContainer) string {
	tokenFile := envValue(container.Env, "ANALYTICS_TOKEN_FILE")
	if tokenFile == "" {
		return "has neither ANALYTICS_TOKEN_FILE nor ANALYTICS_API_KEY, so ingest will answer 401"
	}
	var volumeName string
	for _, mount := range container.VolumeMounts {
		if mount.MountPath != "" && strings.HasPrefix(tokenFile, strings.TrimRight(mount.MountPath, "/")+"/") {
			volumeName = mount.Name
		}
	}
	if volumeName == "" {
		return fmt.Sprintf("ANALYTICS_TOKEN_FILE %s is not under a volume mount", tokenFile)
	}
	audience := ""
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name != volumeName || volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				audience = source.ServiceAccountToken.Audience
			}
		}
	}
	if audience == "" {
		return fmt.Sprintf("volume %s is not a projected service account token", volumeName)
	}
	if audience != mcpdefaults.AnalyticsTokenAudience {
		return fmt.Sprintf("token audience is %q, ingest requires %q", audience, mcpdefaults.AnalyticsTokenAudience)
	}
	if sa := deployment.Spec.Template.Spec.ServiceAccountName; sa != kubeworkload.DefaultServiceAccountName {
		return fmt.Sprintf("runs as ServiceAccount %q, ingest only accepts %q", sa, kubeworkload.DefaultServiceAccountName)
	}
	return ""
}

// checkGatewayAnalyticsDelivery looks for the symptom that hid the 401 outage:
// sidecars logging rejected or failed analytics emissions. It reads a short
// log tail per analytics-enabled gateway.
func checkGatewayAnalyticsDelivery(kubectl core.KubectlRunner) DoctorCheck {
	const checkName = "gateway analytics delivery"
	const maxGateways = 25
	deployments, check := listGatewayAnalyticsDeployments(kubectl, checkName)
	if check != nil {
		return *check
	}
	if len(deployments) == 0 {
		return DoctorCheck{Name: checkName, OK: true, Detail: "no gateway sidecars with analytics ingest URLs found"}
	}
	scanned := deployments
	if len(scanned) > maxGateways {
		scanned = scanned[:maxGateways]
	}
	failures := make([]string, 0)
	for _, deployment := range scanned {
		logs, err := readKubectlOutput(kubectl, []string{"logs", "deploy/" + deployment.Metadata.Name, "-n", deployment.Metadata.Namespace, "-c", "mcp-gateway", "--tail=200"})
		if err != nil {
			continue // pod may be starting; readiness checks cover that
		}
		unauthorized, other := 0, 0
		for _, line := range strings.Split(logs, "\n") {
			switch {
			case strings.Contains(line, "gateway analytics emission failed with status 401"), strings.Contains(line, "gateway analytics emission failed with status 403"):
				unauthorized++
			case strings.Contains(line, "gateway analytics emission failed"), strings.Contains(line, "failed to emit gateway analytics event"):
				other++
			}
		}
		id := deployment.Metadata.Namespace + "/" + deployment.Metadata.Name
		if unauthorized > 0 {
			failures = append(failures, fmt.Sprintf("%s ingest rejected %d recent event(s) as unauthorized", id, unauthorized))
		} else if other > 0 {
			failures = append(failures, fmt.Sprintf("%s failed to deliver %d recent event(s)", id, other))
		}
	}
	if len(failures) > 0 {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: strings.Join(limitStrings(failures, 4), "; "),
			Remedy: "usage analytics are missing events for these servers; run the gateway analytics credentials and ingest service account checks, then inspect the sidecar and mcp-ingest logs",
		}
	}
	return DoctorCheck{
		Name:   checkName,
		OK:     true,
		Detail: fmt.Sprintf("no recent emission failures in %d gateway sidecar log(s)", len(scanned)),
	}
}

// checkIngestServiceAccountAuth verifies the pieces ingest needs to validate
// gateway tokens: the audience setting, its own ServiceAccount with a mounted
// token, and permission to create TokenReviews.
func checkIngestServiceAccountAuth(kubectl core.KubectlRunner) DoctorCheck {
	const checkName = "ingest service account auth"
	namespace := componentNamespace("ingest")
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: checkName, OK: true, Detail: fmt.Sprintf("namespace %s not installed; skipping ingest auth check", namespace)}
	}
	raw, err := readKubectlOutput(kubectl, []string{"get", "deployment", "mcp-ingest", "-n", namespace, "-o", "json"})
	if err != nil || strings.TrimSpace(raw) == "" {
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("deployment mcp-ingest not found in %s", namespace), Remedy: "rerun setup to install analytics ingest"}
	}
	var deployment doctorDeployment
	if err := json.Unmarshal([]byte(raw), &deployment); err != nil {
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("failed parsing mcp-ingest deployment: %v", err), Remedy: "rerun cluster doctor and inspect `kubectl -n " + namespace + " get deploy mcp-ingest -o yaml`"}
	}
	podSpec := deployment.Spec.Template.Spec
	remedy := "apply the release's k8s/06-ingest.yaml (rerun setup) so ingest has its ServiceAccount, TokenReview RBAC and INGEST_SA_TOKEN_AUDIENCE"

	var ingest doctorContainer
	for _, container := range podSpec.Containers {
		if container.Name == "ingest" {
			ingest = container
		}
	}
	if got := envValue(ingest.Env, "INGEST_SA_TOKEN_AUDIENCE"); got != mcpdefaults.AnalyticsTokenAudience {
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("mcp-ingest INGEST_SA_TOKEN_AUDIENCE is %q, gateways present %q: every gateway event will be rejected", got, mcpdefaults.AnalyticsTokenAudience), Remedy: remedy}
	}
	if podSpec.ServiceAccountName != ingestServiceAccountName {
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("mcp-ingest runs as ServiceAccount %q, expected %q", podSpec.ServiceAccountName, ingestServiceAccountName), Remedy: remedy}
	}
	if podSpec.AutomountServiceAccountToken != nil && !*podSpec.AutomountServiceAccountToken {
		return DoctorCheck{Name: checkName, OK: false, Detail: "mcp-ingest has automountServiceAccountToken=false, so it cannot call the TokenReview API", Remedy: remedy}
	}
	if _, err := readKubectlOutput(kubectl, []string{"get", "serviceaccount", ingestServiceAccountName, "-n", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("ServiceAccount %s/%s not found", namespace, ingestServiceAccountName), Remedy: remedy}
	}
	answer, err := readKubectlOutput(kubectl, []string{"auth", "can-i", "create", "tokenreviews.authentication.k8s.io", "--as=system:serviceaccount:" + namespace + ":" + ingestServiceAccountName})
	if strings.TrimSpace(answer) != "yes" {
		detail := fmt.Sprintf("ServiceAccount %s/%s cannot create tokenreviews.authentication.k8s.io; ingest cannot validate gateway tokens", namespace, ingestServiceAccountName)
		if err != nil && strings.TrimSpace(answer) == "" {
			detail = fmt.Sprintf("could not verify %s/%s may create tokenreviews: %v", namespace, ingestServiceAccountName, err)
		}
		return DoctorCheck{Name: checkName, OK: false, Detail: detail, Remedy: remedy + " (ClusterRoleBinding mcp-ingest-tokenreview)"}
	}
	return DoctorCheck{Name: checkName, OK: true, Detail: fmt.Sprintf("mcp-ingest validates gateway tokens for audience %q as ServiceAccount %s/%s", mcpdefaults.AnalyticsTokenAudience, namespace, ingestServiceAccountName)}
}

const ingestServiceAccountName = "mcp-ingest"

func checkPlatformWorkloadHealth(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "platform workload rollout health", OK: true, Detail: "namespace mcp-platform not found; skipping workload checks"}
	}
	deployments, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-n", componentNamespace("platform-api"), "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "platform workload rollout health", OK: false, Detail: fmt.Sprintf("failed listing platform deployments: %v", err), Remedy: "check Kubernetes API access and inspect `kubectl -n mcp-platform get deploy`"}
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Replicas int32 `json:"replicas"`
			} `json:"spec"`
			Status struct {
				Replicas, ReadyReplicas, UpdatedReplicas, AvailableReplicas, UnavailableReplicas int32
				Conditions                                                                       []struct{ Type, Status, Reason, Message string } `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(deployments), &list); err != nil {
		return DoctorCheck{Name: "platform workload rollout health", OK: false, Detail: fmt.Sprintf("failed parsing platform deployments: %v", err), Remedy: "rerun cluster doctor and inspect deployment JSON"}
	}
	failures := make([]string, 0)
	for _, item := range list.Items {
		desired := item.Spec.Replicas
		if desired > 0 && (item.Status.ReadyReplicas < desired || item.Status.UpdatedReplicas < desired || item.Status.UnavailableReplicas > 0) {
			failures = append(failures, fmt.Sprintf("deployment/%s ready=%d/%d updated=%d unavailable=%d", item.Metadata.Name, item.Status.ReadyReplicas, desired, item.Status.UpdatedReplicas, item.Status.UnavailableReplicas))
		}
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Progressing" && condition.Status == "False" {
				failures = append(failures, fmt.Sprintf("deployment/%s %s: %s", item.Metadata.Name, condition.Reason, condition.Message))
			}
		}
	}
	pods, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", componentNamespace("platform-api"), "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "platform workload rollout health", OK: false, Detail: fmt.Sprintf("failed listing platform pods: %v", err), Remedy: "inspect `kubectl -n mcp-platform get pods`"}
	}
	var podList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					Name         string `json:"name"`
					RestartCount int32  `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(pods), &podList); err != nil {
		return DoctorCheck{Name: "platform workload rollout health", OK: false, Detail: fmt.Sprintf("failed parsing platform pods: %v", err), Remedy: "rerun cluster doctor and inspect pod JSON"}
	}
	for _, pod := range podList.Items {
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Waiting != nil && container.State.Waiting.Reason != "" {
				reason := container.State.Waiting.Reason
				if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError" || reason == "RunContainerError" {
					failures = append(failures, fmt.Sprintf("pod/%s container/%s %s restarts=%d", pod.Metadata.Name, container.Name, reason, container.RestartCount))
				}
			}
			if container.RestartCount >= 3 {
				failures = append(failures, fmt.Sprintf("pod/%s container/%s restarts=%d", pod.Metadata.Name, container.Name, container.RestartCount))
			}
		}
	}
	if len(failures) > 0 {
		return DoctorCheck{Name: "platform workload rollout health", OK: false, Detail: strings.Join(limitStrings(failures, 6), "; "), Remedy: "inspect pod events and previous logs; rerun setup after correcting the failing Secret, image, or dependency"}
	}
	return DoctorCheck{Name: "platform workload rollout health", OK: true, Detail: fmt.Sprintf("%d platform deployment(s) and pod containers have healthy rollout state", len(list.Items))}
}

func checkPlatformStalePods(kubectl core.KubectlRunner) DoctorCheck {
	const name = "platform stale pods"
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "namespace mcp-platform not found; skipping stale pod check"}
	}
	pods, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", componentNamespace("platform-api"), "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed listing platform pods: %v", err), Remedy: "inspect `kubectl -n mcp-platform get pods`"}
	}
	var podList struct {
		Items []struct {
			Metadata struct {
				Name            string `json:"name"`
				OwnerReferences []struct {
					Kind string `json:"kind"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Status struct {
				Phase  string `json:"phase"`
				Reason string `json:"reason"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(pods), &podList); err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed parsing platform pods: %v", err), Remedy: "rerun cluster doctor and inspect pod JSON"}
	}
	stale := make([]string, 0)
	for _, pod := range podList.Items {
		switch pod.Status.Phase {
		case "Failed":
			reason := pod.Status.Reason
			if reason == "" {
				reason = "Failed"
			}
			stale = append(stale, fmt.Sprintf("pod/%s %s", pod.Metadata.Name, reason))
		case "Succeeded":
			ownedByJob := false
			for _, owner := range pod.Metadata.OwnerReferences {
				if owner.Kind == "Job" {
					ownedByJob = true
				}
			}
			if !ownedByJob {
				stale = append(stale, fmt.Sprintf("pod/%s Completed", pod.Metadata.Name))
			}
		}
	}
	if len(stale) == 0 {
		return DoctorCheck{Name: name, OK: true, Detail: "no Failed, Evicted, or orphaned Completed pods in mcp-platform"}
	}
	return DoctorCheck{
		Name:   name,
		OK:     false,
		Detail: fmt.Sprintf("%d stale terminated pod(s) left by eviction or restart churn: %s", len(stale), strings.Join(limitStrings(stale, 6), "; ")),
		Remedy: "rerun `mcp-runtime setup` (it prunes terminated pods), or run `kubectl -n mcp-platform delete pod --field-selector=status.phase=Failed`",
	}
}

func checkPlatformPostgresCredentialDrift(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", componentNamespace("platform-api"), "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: true, Detail: "namespace mcp-platform not found; skipping database credential check"}
	}
	postgresSecret, encoded, err := readOwnedSecret(kubectl, "POSTGRES_PASSWORD")
	if err != nil {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: false, Detail: "POSTGRES_PASSWORD is missing from " + postgresSecret, Remedy: "rerun setup so the managed database Secret is rendered and synchronized"}
	}
	expected, err := decodeBase64(encoded)
	if err != nil || strings.TrimSpace(expected) == "" {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: false, Detail: "POSTGRES_PASSWORD is empty or invalid in " + postgresSecret, Remedy: "set a non-empty POSTGRES_PASSWORD and rerun setup"}
	}
	pod, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", componentNamespace("postgres"), "-l", "app=mcp-postgres", "-o", "jsonpath={.items[0].metadata.name}"})
	if err != nil || strings.TrimSpace(pod) == "" {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: false, Detail: "no running Postgres pod was found", Remedy: "inspect `kubectl -n mcp-platform get pods -l app=mcp-postgres`"}
	}
	probe, err := kubectl.CommandArgs([]string{"exec", "-i", "-n", componentNamespace("postgres"), strings.TrimSpace(pod), "--", "sh", "-c", `IFS= read -r MCP_EXPECTED_PASSWORD || exit 1; PGPASSWORD="$MCP_EXPECTED_PASSWORD" psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc 'select 1'`})
	if err == nil {
		probe.SetStdin(strings.NewReader(expected + "\n"))
	}
	var actualBytes []byte
	if err == nil {
		actualBytes, err = probe.CombinedOutput()
	}
	actual := string(actualBytes)
	if err != nil {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: false, Detail: "the managed POSTGRES_PASSWORD failed a TCP authentication probe against the live database", Remedy: "rerun setup to apply ALTER USER and restart all Secret consumers; do not test only the local Postgres socket because peer auth can bypass the password"}
	}
	if strings.TrimSpace(actual) != "1" {
		return DoctorCheck{Name: "platform Postgres credential drift", OK: false, Detail: fmt.Sprintf("the managed POSTGRES_PASSWORD TCP probe returned %q instead of 1", strings.TrimSpace(actual)), Remedy: "rerun setup to apply ALTER USER and restart all Secret consumers"}
	}
	return DoctorCheck{Name: "platform Postgres credential drift", OK: true, Detail: "managed Postgres password matches the live database container credential"}
}

type doctorDeploymentList struct {
	Items []doctorDeployment `json:"items"`
}

type doctorDeployment struct {
	Metadata struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				ServiceAccountName           string            `json:"serviceAccountName"`
				AutomountServiceAccountToken *bool             `json:"automountServiceAccountToken"`
				Containers                   []doctorContainer `json:"containers"`
				Volumes                      []doctorVolume    `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func (d doctorDeployment) gatewayContainer() (doctorContainer, bool) {
	for _, container := range d.Spec.Template.Spec.Containers {
		if container.Name == "mcp-gateway" {
			return container, true
		}
	}
	return doctorContainer{}, false
}

type doctorVolume struct {
	Name      string `json:"name"`
	Projected *struct {
		Sources []struct {
			ServiceAccountToken *struct {
				Audience string `json:"audience"`
			} `json:"serviceAccountToken"`
		} `json:"sources"`
	} `json:"projected"`
}

type doctorVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type doctorContainer struct {
	Name         string              `json:"name"`
	Env          []doctorEnvVar      `json:"env"`
	VolumeMounts []doctorVolumeMount `json:"volumeMounts"`
}

type doctorEnvVar struct {
	Name      string              `json:"name"`
	Value     string              `json:"value"`
	ValueFrom *doctorEnvVarSource `json:"valueFrom"`
}

type doctorEnvVarSource struct {
	SecretKeyRef *doctorSecretKeyRef `json:"secretKeyRef"`
}

type doctorSecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func envByName(env []doctorEnvVar, name string) (doctorEnvVar, bool) {
	for _, item := range env {
		if item.Name == name {
			return item, true
		}
	}
	return doctorEnvVar{}, false
}

func envValue(env []doctorEnvVar, name string) string {
	item, ok := envByName(env, name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(item.Value)
}

func checkSecretKeyPopulated(kubectl core.KubectlRunner, namespace, name, key string) error {
	out, err := readKubectlOutput(kubectl, []string{"get", "secret", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return err
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &secret); err != nil {
		return err
	}
	encoded, ok := secret.Data[key]
	if !ok {
		return fmt.Errorf("key missing")
	}
	decodedBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return fmt.Errorf("decode secret key %q: %w", key, err)
	}
	if strings.TrimSpace(string(decodedBytes)) == "" {
		return fmt.Errorf("key empty")
	}
	return nil
}

func limitStrings(values []string, max int) []string {
	if len(values) <= max {
		return values
	}
	out := append([]string{}, values[:max]...)
	out = append(out, fmt.Sprintf("%d more", len(values)-max))
	return out
}
