package doctor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"mcp-runtime/internal/cli/core"
)

// checkSentinelTelemetryPipeline verifies the bundled telemetry path instead
// of treating a healthy application deployment as proof that traces can be
// exported. It remains distribution-neutral by discovering the Service and
// checking the deployed workload rather than assuming a node address.
func checkSentinelTelemetryPipeline(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: true, Detail: "namespace mcp-sentinel not found; skipping telemetry check"}
	}

	collectorPair, collectorReady, err := doctorDeploymentReplicaStatus(kubectl, doctorSentinelNamespace, "otel-collector")
	if err != nil {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: false, Detail: err.Error(), Remedy: "inspect the otel-collector deployment and its ConfigMap"}
	}
	if !collectorReady {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: false, Detail: fmt.Sprintf("otel-collector %s replicas ready", collectorPair), Remedy: "inspect `kubectl -n mcp-sentinel logs deploy/otel-collector` and the collector ConfigMap"}
	}

	if _, err := readKubectlOutput(kubectl, []string{"get", "service", "otel-collector", "-n", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: false, Detail: fmt.Sprintf("otel-collector Service is unavailable: %v", err), Remedy: "restore Service/otel-collector and its selector"}
	}
	addresses, err := readKubectlOutput(kubectl, []string{"get", "endpoints", "otel-collector", "-n", doctorSentinelNamespace, "-o", "jsonpath={.subsets[*].addresses[*].ip}"})
	if err != nil || strings.TrimSpace(addresses) == "" {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: false, Detail: "otel-collector Service has no ready endpoints", Remedy: "check the collector pod labels, readiness probe, and Service selector"}
	}

	if _, err := readKubectlOutput(kubectl, []string{"get", "configmap", "otel-collector-config", "-n", doctorSentinelNamespace, "-o", "jsonpath={.data.otel-collector-config.yaml}"}); err != nil {
		return DoctorCheck{Name: "sentinel telemetry pipeline", OK: false, Detail: fmt.Sprintf("otel-collector ConfigMap is unavailable: %v", err), Remedy: "restore ConfigMap/otel-collector-config and its trace pipeline"}
	}
	return DoctorCheck{Name: "sentinel telemetry pipeline", OK: true, Detail: fmt.Sprintf("otel-collector %s ready with a Service endpoint", collectorPair)}
}

func checkPersistentVolumeClaims(kubectl core.KubectlRunner) DoctorCheck {
	raw, err := readKubectlOutput(kubectl, []string{"get", "pvc", "-A", "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "persistent volume claims", OK: false, Detail: fmt.Sprintf("failed reading PVC status: %v", err), Remedy: "check Kubernetes storage permissions and PVC status"}
	}
	var payload struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return DoctorCheck{Name: "persistent volume claims", OK: false, Detail: fmt.Sprintf("failed parsing PVC status: %v", err), Remedy: "inspect PVC objects and storage provisioner events"}
	}
	var waiting []string
	for _, item := range payload.Items {
		phase := strings.TrimSpace(item.Status.Phase)
		if phase != "Bound" {
			waiting = append(waiting, fmt.Sprintf("%s/%s (%s)", item.Metadata.Namespace, item.Metadata.Name, phase))
		}
	}
	if len(waiting) > 0 {
		return DoctorCheck{Name: "persistent volume claims", OK: false, Detail: fmt.Sprintf("PVCs are not Bound: %s", strings.Join(waiting, ", ")), Remedy: "inspect PVC events, StorageClass provisioning, capacity, and node affinity"}
	}
	if len(payload.Items) == 0 {
		return DoctorCheck{Name: "persistent volume claims", OK: true, Detail: "no PVCs are present; skipping persistence validation"}
	}
	return DoctorCheck{Name: "persistent volume claims", OK: true, Detail: fmt.Sprintf("%d PVC(s) are Bound", len(payload.Items))}
}

const (
	grafanaDashboardUID  = "mcp-server"
	grafanaPrometheusUID = "prometheus"
)

var grafanaPrometheusUIDPattern = regexp.MustCompile(`(?m)^\s*uid:\s*["']?` + grafanaPrometheusUID + `["']?\s*$`)

// checkSentinelGrafanaProvisioning detects drift between the live Grafana
// provisioning objects and the repo manifests. Server-card deep links target
// dashboard UID "mcp-server" whose panels pin datasource UID "prometheus"; if
// either is missing Grafana reports "Dashboard not found" or "Data source not
// found" even though the card metrics (served from the Prometheus API) work.
// All reads are ConfigMap/Deployment reads; no Grafana credentials are needed.
func checkSentinelGrafanaProvisioning(kubectl core.KubectlRunner) DoctorCheck {
	const name = "sentinel Grafana provisioning"
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "namespace mcp-sentinel not found; skipping Grafana provisioning check"}
	}
	if _, err := readKubectlOutput(kubectl, []string{"get", "deployment", "grafana", "-n", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "grafana deployment not found; skipping Grafana provisioning check"}
	}
	const remedy = "re-run `mcp-runtime setup` (or the targeted platform update) so k8s/19-grafana-datasources.yaml, k8s/21-grafana-dashboards.yaml, and k8s/12-grafana.yaml are applied, then restart deployment/grafana"

	datasources, err := readKubectlOutput(kubectl, []string{"get", "configmap", "grafana-datasources", "-n", doctorSentinelNamespace, "-o", `jsonpath={.data.datasources\.yaml}`})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("ConfigMap grafana-datasources is unavailable: %v", err), Remedy: remedy}
	}
	if !grafanaPrometheusUIDPattern.MatchString(datasources) {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("grafana-datasources does not pin the Prometheus datasource uid %q; dashboard panels fail with \"Data source not found\"", grafanaPrometheusUID), Remedy: remedy}
	}

	dashboard, err := readKubectlOutput(kubectl, []string{"get", "configmap", "grafana-dashboards", "-n", doctorSentinelNamespace, "-o", `jsonpath={.data.mcp-server\.json}`})
	if err != nil || strings.TrimSpace(dashboard) == "" {
		return DoctorCheck{Name: name, OK: false, Detail: "ConfigMap grafana-dashboards is missing the mcp-server.json dashboard; server-card Grafana links fail with \"Dashboard not found\"", Remedy: remedy}
	}
	var parsed struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(dashboard), &parsed); err != nil || parsed.UID != grafanaDashboardUID {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("mcp-server.json does not declare dashboard uid %q", grafanaDashboardUID), Remedy: remedy}
	}

	raw, err := readKubectlOutput(kubectl, []string{"get", "deployment", "grafana", "-n", doctorSentinelNamespace, "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed reading deployment/grafana: %v", err), Remedy: remedy}
	}
	var deploy struct {
		Spec struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						ConfigMap *struct {
							Name  string `json:"name"`
							Items []struct {
								Key string `json:"key"`
							} `json:"items"`
						} `json:"configMap"`
					} `json:"volumes"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(raw), &deploy); err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("failed parsing deployment/grafana: %v", err), Remedy: remedy}
	}
	mounted := map[string]bool{}
	for _, vol := range deploy.Spec.Template.Spec.Volumes {
		if vol.ConfigMap == nil {
			continue
		}
		if vol.ConfigMap.Name == "grafana-datasources" {
			mounted["grafana-datasources"] = true
		}
		for _, item := range vol.ConfigMap.Items {
			if vol.ConfigMap.Name == "grafana-dashboards" {
				mounted[item.Key] = true
			}
		}
	}
	var missing []string
	for _, key := range []string{"grafana-datasources", "dashboard-provider.yaml", "mcp-server.json"} {
		if !mounted[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("deployment/grafana does not mount provisioning input(s): %s", strings.Join(missing, ", ")), Remedy: remedy}
	}
	return DoctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("dashboard uid %q and Prometheus datasource uid %q are provisioned and mounted", grafanaDashboardUID, grafanaPrometheusUID)}
}
