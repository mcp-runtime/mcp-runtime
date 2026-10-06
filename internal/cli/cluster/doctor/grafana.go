package doctor

import (
	"fmt"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/ops"
)

const grafanaAdminCredentialCheckName = "platform Grafana admin credential drift"

// checkPlatformGrafanaAdminCredentials detects drift between the Grafana admin
// account persisted in Grafana's database (on its PVC) and the configured
// mcp-grafana-credentials Secret (issue #500). GF_SECURITY_ADMIN_PASSWORD only
// seeds the account when the database is first created, so rotating the Secret
// later leaves Grafana rejecting the configured password while the platform
// ingress gate (platform-admin-auth) still admits the operator.
//
// The probe is shared with `mcp-runtime ops grafana check`: it runs inside the
// Grafana pod against 127.0.0.1 (bypassing the ingress gate), reads the
// configured credentials from the pod's own environment, and returns only HTTP
// status codes. No credential value crosses the CLI or appears in output. The
// check never resets an account; recovery is the deliberate
// `ops grafana reset-admin-password --yes` workflow.
func checkPlatformGrafanaAdminCredentials(kubectl core.KubectlRunner) DoctorCheck {
	const name = grafanaAdminCredentialCheckName
	namespace := componentNamespace("grafana")
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("namespace %s not found; skipping Grafana credential check", namespace)}
	}
	if _, err := readKubectlOutput(kubectl, []string{"get", "deployment", "grafana", "-n", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "grafana deployment not found; skipping Grafana credential check"}
	}
	pair, ready, err := doctorDeploymentReplicaStatus(kubectl, namespace, "grafana")
	if err != nil {
		return DoctorCheck{Name: name, OK: true, Detail: "grafana readiness could not be read; credential check skipped, see platform workload rollout health"}
	}
	if !ready {
		return DoctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("grafana is not ready (%s replicas); credential check skipped, see platform workload rollout health", pair)}
	}

	probe, err := kubectl.CommandArgs(ops.GrafanaCredentialProbeArgs())
	var out []byte
	if err == nil {
		out, err = probe.CombinedOutput()
	}
	if err != nil {
		// Do not echo probe output: keep this path free of anything but codes.
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("could not run the in-pod Grafana login probe: %v", err), Remedy: "check `kubectl exec` permission on deploy/grafana, then run `mcp-runtime ops grafana check`"}
	}

	res := ops.ParseGrafanaProbe(string(out))
	switch res.State {
	case ops.GrafanaAuthOK:
		return DoctorCheck{Name: name, OK: true, Detail: "Grafana's persisted admin account accepts the configured mcp-grafana-credentials"}
	case ops.GrafanaCredentialDrift:
		return DoctorCheck{
			Name:   name,
			OK:     false,
			Detail: "Grafana rejects the configured admin credentials (HTTP 401): the admin account persisted on grafana-pvc has drifted from mcp-grafana-credentials. The platform ingress gate is a separate layer and may still admit operators",
			Remedy: "confirm with `mcp-runtime ops grafana check`, then recover deliberately with `mcp-runtime ops grafana reset-admin-password --yes` (backs up grafana.db first, keeps dashboards/datasources, overwrites any password set inside Grafana)",
		}
	case ops.GrafanaUnhealthy:
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("Grafana /api/health returned %s inside the pod; credentials were not evaluated", probeCode(res.HealthCode)), Remedy: "inspect `mcp-runtime ops logs grafana` and the grafana-pvc database"}
	default:
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("unexpected Grafana login response (auth=%s); credentials were not classified", probeCode(res.AuthCode)), Remedy: "run `mcp-runtime ops grafana check` and inspect `mcp-runtime ops logs grafana`"}
	}
}

func probeCode(code string) string {
	if code == "" {
		return "no response"
	}
	return code
}
