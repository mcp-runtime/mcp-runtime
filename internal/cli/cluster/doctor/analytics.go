package doctor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
)

func checkGatewayAnalyticsCredentials(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-A", "-o", "json"})
	if err != nil {
		return DoctorCheck{
			Name:   "gateway analytics credentials",
			OK:     false,
			Detail: fmt.Sprintf("failed listing deployments: %v", err),
			Remedy: "check Kubernetes API access and RBAC for listing Deployments",
		}
	}

	var deployments doctorDeploymentList
	if err := json.Unmarshal([]byte(out), &deployments); err != nil {
		return DoctorCheck{
			Name:   "gateway analytics credentials",
			OK:     false,
			Detail: fmt.Sprintf("failed parsing deployments JSON: %v", err),
			Remedy: "rerun cluster doctor; if this persists, inspect `kubectl get deploy -A -o json`",
		}
	}

	checked := 0
	failures := make([]string, 0)
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "mcp-gateway" {
				continue
			}
			if envValue(container.Env, "ANALYTICS_INGEST_URL") == "" {
				continue
			}
			checked++
			apiKeyEnv, ok := envByName(container.Env, "ANALYTICS_API_KEY")
			if !ok {
				failures = append(failures, fmt.Sprintf("%s/%s missing ANALYTICS_API_KEY", deployment.Metadata.Namespace, deployment.Metadata.Name))
				continue
			}
			if strings.TrimSpace(apiKeyEnv.Value) != "" {
				continue
			}
			if apiKeyEnv.ValueFrom == nil || apiKeyEnv.ValueFrom.SecretKeyRef == nil {
				failures = append(failures, fmt.Sprintf("%s/%s ANALYTICS_API_KEY has no value or secretKeyRef", deployment.Metadata.Namespace, deployment.Metadata.Name))
				continue
			}
			ref := apiKeyEnv.ValueFrom.SecretKeyRef
			if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" {
				failures = append(failures, fmt.Sprintf("%s/%s ANALYTICS_API_KEY secretKeyRef is incomplete", deployment.Metadata.Namespace, deployment.Metadata.Name))
				continue
			}
			if err := checkSecretKeyPopulated(kubectl, deployment.Metadata.Namespace, ref.Name, ref.Key); err != nil {
				failures = append(failures, fmt.Sprintf("%s/%s references unusable Secret %s/%s: %v", deployment.Metadata.Namespace, deployment.Metadata.Name, ref.Name, ref.Key, err))
			}
		}
	}

	if len(failures) > 0 {
		return DoctorCheck{
			Name:   "gateway analytics credentials",
			OK:     false,
			Detail: strings.Join(limitStrings(failures, 4), "; "),
			Remedy: "create a namespace-local ingest-key Secret and set spec.analytics.apiKeySecretRef on affected MCPServers, or redeploy with `mcp-runtime server deploy --metadata-dir .mcp`",
		}
	}
	if checked == 0 {
		return DoctorCheck{
			Name:   "gateway analytics credentials",
			OK:     true,
			Detail: "no gateway sidecars with analytics ingest URLs found",
		}
	}
	return DoctorCheck{
		Name:   "gateway analytics credentials",
		OK:     true,
		Detail: fmt.Sprintf("%d gateway sidecar(s) have usable analytics credentials", checked),
	}
}

func checkSentinelWorkloadHealth(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "sentinel workload rollout health", OK: true, Detail: "namespace mcp-sentinel not found; skipping workload checks"}
	}
	deployments, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-n", doctorSentinelNamespace, "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "sentinel workload rollout health", OK: false, Detail: fmt.Sprintf("failed listing sentinel deployments: %v", err), Remedy: "check Kubernetes API access and inspect `kubectl -n mcp-sentinel get deploy`"}
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
		return DoctorCheck{Name: "sentinel workload rollout health", OK: false, Detail: fmt.Sprintf("failed parsing sentinel deployments: %v", err), Remedy: "rerun cluster doctor and inspect deployment JSON"}
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
	pods, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", doctorSentinelNamespace, "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "sentinel workload rollout health", OK: false, Detail: fmt.Sprintf("failed listing sentinel pods: %v", err), Remedy: "inspect `kubectl -n mcp-sentinel get pods`"}
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
		return DoctorCheck{Name: "sentinel workload rollout health", OK: false, Detail: fmt.Sprintf("failed parsing sentinel pods: %v", err), Remedy: "rerun cluster doctor and inspect pod JSON"}
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
		return DoctorCheck{Name: "sentinel workload rollout health", OK: false, Detail: strings.Join(limitStrings(failures, 6), "; "), Remedy: "inspect pod events and previous logs; rerun setup after correcting the failing Secret, image, or dependency"}
	}
	return DoctorCheck{Name: "sentinel workload rollout health", OK: true, Detail: fmt.Sprintf("%d sentinel deployment(s) and pod containers have healthy rollout state", len(list.Items))}
}

func checkSentinelStalePods(kubectl core.KubectlRunner) DoctorCheck {
	const name = "sentinel stale pods"
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "namespace mcp-sentinel not found; skipping stale pod check"}
	}
	pods, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", doctorSentinelNamespace, "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed listing sentinel pods: %v", err), Remedy: "inspect `kubectl -n mcp-sentinel get pods`"}
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
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed parsing sentinel pods: %v", err), Remedy: "rerun cluster doctor and inspect pod JSON"}
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
		return DoctorCheck{Name: name, OK: true, Detail: "no Failed, Evicted, or orphaned Completed pods in mcp-sentinel"}
	}
	return DoctorCheck{
		Name:   name,
		OK:     false,
		Detail: fmt.Sprintf("%d stale terminated pod(s) left by eviction or restart churn: %s", len(stale), strings.Join(limitStrings(stale, 6), "; ")),
		Remedy: "rerun `mcp-runtime setup` (it prunes terminated pods), or run `kubectl -n mcp-sentinel delete pod --field-selector=status.phase=Failed`",
	}
}

func checkSentinelPostgresCredentialDrift(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: true, Detail: "namespace mcp-sentinel not found; skipping database credential check"}
	}
	encoded, err := readKubectlOutput(kubectl, []string{"get", "secret", "mcp-sentinel-secrets", "-n", doctorSentinelNamespace, "-o", "jsonpath={.data.POSTGRES_PASSWORD}"})
	if err != nil {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: false, Detail: "POSTGRES_PASSWORD is missing from mcp-sentinel-secrets", Remedy: "rerun setup so the managed database Secret is rendered and synchronized"}
	}
	expected, err := decodeBase64(encoded)
	if err != nil || strings.TrimSpace(expected) == "" {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: false, Detail: "POSTGRES_PASSWORD is empty or invalid in mcp-sentinel-secrets", Remedy: "set a non-empty POSTGRES_PASSWORD and rerun setup"}
	}
	pod, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", doctorSentinelNamespace, "-l", "app=mcp-sentinel-postgres", "-o", "jsonpath={.items[0].metadata.name}"})
	if err != nil || strings.TrimSpace(pod) == "" {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: false, Detail: "no running Postgres pod was found", Remedy: "inspect `kubectl -n mcp-sentinel get pods -l app=mcp-sentinel-postgres`"}
	}
	probe, err := kubectl.CommandArgs([]string{"exec", "-i", "-n", doctorSentinelNamespace, strings.TrimSpace(pod), "--", "sh", "-c", `IFS= read -r MCP_EXPECTED_PASSWORD || exit 1; PGPASSWORD="$MCP_EXPECTED_PASSWORD" psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc 'select 1'`})
	if err == nil {
		probe.SetStdin(strings.NewReader(expected + "\n"))
	}
	var actualBytes []byte
	if err == nil {
		actualBytes, err = probe.CombinedOutput()
	}
	actual := string(actualBytes)
	if err != nil {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: false, Detail: "the managed POSTGRES_PASSWORD failed a TCP authentication probe against the live database", Remedy: "rerun setup to apply ALTER USER and restart all Secret consumers; do not test only the local Postgres socket because peer auth can bypass the password"}
	}
	if strings.TrimSpace(actual) != "1" {
		return DoctorCheck{Name: "sentinel Postgres credential drift", OK: false, Detail: fmt.Sprintf("the managed POSTGRES_PASSWORD TCP probe returned %q instead of 1", strings.TrimSpace(actual)), Remedy: "rerun setup to apply ALTER USER and restart all Secret consumers"}
	}
	return DoctorCheck{Name: "sentinel Postgres credential drift", OK: true, Detail: "managed Postgres password matches the live database container credential"}
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
				Containers []doctorContainer `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type doctorContainer struct {
	Name string         `json:"name"`
	Env  []doctorEnvVar `json:"env"`
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
