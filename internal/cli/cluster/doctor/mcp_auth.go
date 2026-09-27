package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"

	"mcp-runtime/internal/cli/core"
)

const doctorMCPAuthDeployment = "mcp-auth-server"

// checkMCPAuthDeployment reports the optional authorization server separately
// from Sentinel. An install without the opt-in deployment is healthy and is
// explicitly skipped; an enabled deployment must have its rollout ready.
func checkMCPAuthDeployment(kubectl core.KubectlRunner) DoctorCheck {
	output, err := readKubectlOutput(kubectl, []string{"get", "deployment", doctorMCPAuthDeployment, "-n", doctorSentinelNamespace, "--ignore-not-found", "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: "mcp-auth deployment", OK: false, Detail: fmt.Sprintf("failed reading optional authorization server deployment: %v", err), Remedy: "check Kubernetes API access to deployments in mcp-sentinel"}
	}
	if strings.TrimSpace(output) == "" {
		return DoctorCheck{Name: "mcp-auth deployment", OK: true, Detail: "optional mcp-auth authorization server is not installed; skipping"}
	}
	var deployment appsv1.Deployment
	if err := json.Unmarshal([]byte(output), &deployment); err != nil {
		return DoctorCheck{Name: "mcp-auth deployment", OK: false, Detail: fmt.Sprintf("failed parsing authorization server deployment: %v", err), Remedy: "inspect kubectl get deployment mcp-auth-server -n mcp-sentinel -o json"}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	status := deployment.Status
	failed := false
	for _, condition := range status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == "False" {
			failed = true
		}
	}
	if desired < 1 || failed || status.ObservedGeneration < deployment.Generation || status.UpdatedReplicas < desired || status.Replicas > status.UpdatedReplicas || status.ReadyReplicas < desired || status.AvailableReplicas < desired || status.UnavailableReplicas > 0 {
		return DoctorCheck{
			Name:   "mcp-auth deployment",
			OK:     false,
			Detail: fmt.Sprintf("deployment %s rollout is incomplete (generation=%d observed=%d ready=%d/%d updated=%d total=%d unavailable=%d)", doctorMCPAuthDeployment, deployment.Generation, status.ObservedGeneration, status.ReadyReplicas, desired, status.UpdatedReplicas, status.Replicas, status.UnavailableReplicas),
			Remedy: "kubectl rollout status deployment/mcp-auth-server -n mcp-sentinel and inspect its pod events/logs",
		}
	}
	return DoctorCheck{Name: "mcp-auth deployment", OK: true, Detail: "optional authorization server deployment's current revision is ready"}
}

func checkMCPAuthSecrets(kubectl core.KubectlRunner) DoctorCheck {
	if _, err := readKubectlOutput(kubectl, []string{"get", "deployment", doctorMCPAuthDeployment, "-n", doctorSentinelNamespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{Name: "mcp-auth secrets", OK: true, Detail: "optional mcp-auth authorization server is not installed; skipping"}
	}
	checks := []struct {
		name string
		key  string
	}{}
	// Test mode deliberately uses an ephemeral signing key and HTTP ingress.
	// Production manifests expose the mounted key path and TLS ingress, so use
	// those rendered resources to discover custom Secret names instead of
	// assuming the setup defaults.
	signingKeySecret, _ := readKubectlOutput(kubectl, []string{"get", "deployment", doctorMCPAuthDeployment, "-n", doctorSentinelNamespace, "-o", "jsonpath={.spec.template.spec.volumes[?(@.name==\"signing-key\")].secret.secretName}"})
	if strings.TrimSpace(signingKeySecret) != "" {
		checks = append(checks, struct {
			name string
			key  string
		}{name: strings.TrimSpace(signingKeySecret), key: "private-key.pem"})
	}
	tlsSecret, _ := readKubectlOutput(kubectl, []string{"get", "ingress", doctorMCPAuthDeployment, "-n", doctorSentinelNamespace, "-o", "jsonpath={.spec.tls[0].secretName}"})
	if strings.TrimSpace(tlsSecret) == "" && strings.TrimSpace(signingKeySecret) != "" {
		// Older production manifests used the default name without exposing it
		// through a custom Ingress query; retain a useful check for those installs.
		tlsSecret = "mcp-auth-server-tls" // #nosec G101 -- Kubernetes Secret name, not a credential.
	}
	if strings.TrimSpace(tlsSecret) != "" {
		checks = append(checks,
			struct {
				name string
				key  string
			}{name: strings.TrimSpace(tlsSecret), key: "tls.crt"},
			struct {
				name string
				key  string
			}{name: strings.TrimSpace(tlsSecret), key: "tls.key"},
		)
	}
	if len(checks) == 0 {
		return DoctorCheck{Name: "mcp-auth secrets", OK: true, Detail: "test-mode mcp-auth uses ephemeral signing and ingress credentials; skipping Secret checks"}
	}
	for _, secret := range checks {
		path := "{.data." + strings.ReplaceAll(secret.key, ".", `\.`) + "}"
		value, err := readKubectlOutput(kubectl, []string{"get", "secret", secret.name, "-n", doctorSentinelNamespace, "-o", "jsonpath=" + path})
		if err != nil || strings.TrimSpace(value) == "" {
			return DoctorCheck{
				Name:   "mcp-auth secrets",
				OK:     false,
				Detail: fmt.Sprintf("Secret %s is missing non-empty key %s", secret.name, secret.key),
				Remedy: "create the mcp-auth signing-key Secret or wait for the managed mcp-auth TLS Certificate to become ready",
			}
		}
	}
	return DoctorCheck{Name: "mcp-auth secrets", OK: true, Detail: "configured signing key and TLS Secret contain the required keys"}
}
