package doctor

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
)

func checkNamespaceExists(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.name}"})
	if err != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s", namespace),
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check cluster connectivity and kubeconfig",
		}
	}
	out, err := runKubectlBytes(cmd)
	got := strings.TrimSpace(string(out))
	if err != nil || got != namespace {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s", namespace),
			OK:     false,
			Detail: kubectlResultDetail(err, fmt.Sprintf("namespace %s not found", namespace)),
			Remedy: "run `./bin/mcp-runtime setup` to create the runtime namespaces",
		}
	}
	return DoctorCheck{
		Name:   fmt.Sprintf("namespace %s", namespace),
		OK:     true,
		Detail: "present",
	}
}

func checkNamespaceDefaultServiceAccount(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "serviceaccount", "default", "-n", namespace, "-o", "jsonpath={.metadata.name}"})
	if err != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s default serviceaccount", namespace),
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check namespace permissions and kubeconfig",
		}
	}
	out, err := runKubectlBytes(cmd)
	name := strings.TrimSpace(string(out))
	if err != nil || name != "default" {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s default serviceaccount", namespace),
			OK:     false,
			Detail: kubectlResultDetail(err, "serviceaccount default missing"),
			Remedy: fmt.Sprintf("recreate the namespace or run `kubectl create serviceaccount default -n %s`", namespace),
		}
	}
	return DoctorCheck{
		Name:   fmt.Sprintf("namespace %s default serviceaccount", namespace),
		OK:     true,
		Detail: "present",
	}
}

func checkNamespacePolicyGuardrails(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "resourcequota,limitrange", "-n", namespace, "--no-headers", "-o", "name"})
	if err != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s quota/limitrange", namespace),
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "verify RBAC allows listing quota and limitrange resources",
		}
	}
	out, execErr := cmd.CombinedOutput()
	if execErr != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s quota/limitrange", namespace),
			OK:     false,
			Detail: strings.TrimSpace(string(out)),
			Remedy: "inspect namespace policies: `kubectl get resourcequota,limitrange -n mcp-servers`",
		}
	}
	listing := strings.TrimSpace(string(out))
	if listing == "" {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s quota/limitrange", namespace),
			OK:     true,
			Detail: "no ResourceQuota/LimitRange defined",
		}
	}
	count := len(strings.Split(listing, "\n"))
	return DoctorCheck{
		Name:   fmt.Sprintf("namespace %s quota/limitrange", namespace),
		OK:     true,
		Detail: fmt.Sprintf("%d policy objects detected", count),
	}
}

func checkNamespacePodAdmission(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	podName := fmt.Sprintf("doctor-admission-%d", time.Now().UnixNano())
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.9
    securityContext:
      allowPrivilegeEscalation: false
      runAsNonRoot: true
      capabilities:
        drop:
        - ALL
`, podName, namespace)
	cmd, err := kubectl.CommandArgs([]string{"apply", "--dry-run=server", "-f", "-"})
	if err != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s pod admission", namespace),
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check API server admission webhooks and RBAC",
		}
	}
	cmd.SetStdin(strings.NewReader(manifest))
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   fmt.Sprintf("namespace %s pod admission", namespace),
			OK:     false,
			Detail: strings.TrimSpace(string(out)),
			Remedy: "inspect ResourceQuota/LimitRange/admission policies blocking pod creation",
		}
	}
	return DoctorCheck{
		Name:   fmt.Sprintf("namespace %s pod admission", namespace),
		OK:     true,
		Detail: "server-side dry-run pod creation succeeded",
	}
}

func checkMCPServerCRD(kubectl core.KubectlRunner) DoctorCheck {
	crd := "mcpservers.mcpruntime.org"
	cmd, err := kubectl.CommandArgs([]string{"get", "crd", crd, "-o", "jsonpath={.metadata.name}"})
	if err != nil {
		return DoctorCheck{
			Name:   "MCPServer CRD",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "run `./bin/mcp-runtime setup` to install CRDs",
		}
	}
	out, err := runKubectlBytes(cmd)
	got := strings.TrimSpace(string(out))
	if err != nil || got != crd {
		return DoctorCheck{
			Name:   "MCPServer CRD",
			OK:     false,
			Detail: kubectlResultDetail(err, fmt.Sprintf("CRD %s not found", crd)),
			Remedy: "apply CRDs (for example `make manifests` then `kubectl apply -f config/crd/bases`)",
		}
	}
	return DoctorCheck{
		Name:   "MCPServer CRD",
		OK:     true,
		Detail: crd,
	}
}

func checkOperatorReady(kubectl core.KubectlRunner) DoctorCheck {
	deployName := "mcp-runtime-operator-controller-manager"
	ns := "mcp-runtime"
	pair, ready, err := doctorDeploymentReplicaStatus(kubectl, ns, deployName)
	if err != nil {
		return DoctorCheck{
			Name:   "operator readiness",
			OK:     false,
			Detail: err.Error(),
			Remedy: "run `./bin/mcp-runtime setup` to install the operator",
		}
	}
	if !ready {
		return DoctorCheck{
			Name:   "operator readiness",
			OK:     false,
			Detail: fmt.Sprintf("%s replicas ready", pair),
			Remedy: "check operator pods: `kubectl -n mcp-runtime get pods -l control-plane=controller-manager`",
		}
	}
	return DoctorCheck{
		Name:   "operator readiness",
		OK:     true,
		Detail: fmt.Sprintf("%s replicas ready", pair),
	}
}

const operatorWebhookCertRenewalWindow = 30 * 24 * time.Hour

func checkOperatorWebhookCertExpiry(kubectl core.KubectlRunner) DoctorCheck {
	const (
		checkName  = "operator webhook TLS expiry"
		secretName = "mcp-runtime-operator-webhook-server-cert" // #nosec G101 -- Kubernetes Secret object name, not credential material.
		namespace  = "mcp-runtime"
	)
	cmd, err := kubectl.CommandArgs([]string{
		"get", "secret", secretName,
		"-n", namespace,
		"--ignore-not-found", "-o", "jsonpath={.data.tls\\.crt}",
	})
	if err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check cluster connectivity and kubeconfig",
		}
	}
	out, err := runKubectlBytes(cmd)
	certB64 := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "notfound") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return DoctorCheck{Name: checkName, OK: true, Detail: "webhook TLS secret not present (webhooks may be disabled)"}
		}
		return DoctorCheck{Name: checkName, OK: false, Detail: fmt.Sprintf("cannot read operator webhook certificate: %v", err), Remedy: "check RBAC for reading the operator webhook Secret"}
	}
	if certB64 == "" {
		return DoctorCheck{
			Name:   checkName,
			OK:     true,
			Detail: "webhook TLS secret not present (webhooks may be disabled)",
		}
	}
	certPEM, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: "failed to decode webhook serving certificate",
			Remedy: "re-run `./bin/mcp-runtime setup` to regenerate operator webhook TLS",
		}
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: "webhook serving certificate is not valid PEM",
			Remedy: "re-run `./bin/mcp-runtime setup` to regenerate operator webhook TLS",
		}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("failed to parse webhook serving certificate: %v", err),
			Remedy: "re-run `./bin/mcp-runtime setup` to regenerate operator webhook TLS",
		}
	}

	now := time.Now().UTC()
	if now.After(cert.NotAfter) {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("serving certificate expired at %s", cert.NotAfter.UTC().Format(time.RFC3339)),
			Remedy: "re-run `./bin/mcp-runtime setup` to renew operator webhook TLS",
		}
	}
	if now.Add(operatorWebhookCertRenewalWindow).After(cert.NotAfter) {
		return DoctorCheck{
			Name:   checkName,
			OK:     true,
			Detail: fmt.Sprintf("warning: serving certificate expires %s (within 30-day renewal window); re-run setup to renew", cert.NotAfter.UTC().Format(time.RFC3339)),
			Remedy: "re-run `./bin/mcp-runtime setup` to renew the serving certificate",
		}
	}
	return DoctorCheck{
		Name:   checkName,
		OK:     true,
		Detail: fmt.Sprintf("serving certificate valid until %s", cert.NotAfter.UTC().Format(time.RFC3339)),
	}
}

func checkOperatorRecentReconcileErrors(kubectl core.KubectlRunner) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"logs", "-n", "mcp-runtime", "deploy/mcp-runtime-operator-controller-manager", "--since=10m"})
	if err != nil {
		return DoctorCheck{
			Name:   "operator reconcile errors (last 10m)",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "verify operator deployment exists and logs are accessible",
		}
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   "operator reconcile errors (last 10m)",
			OK:     false,
			Detail: strings.TrimSpace(string(out)),
			Remedy: "inspect operator logs directly and fix reconcile failures",
		}
	}
	patterns := []string{"reconciler error", "failed to reconcile", "error syncing"}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())
		if strings.Contains(line, "doctor-smoke-") {
			continue
		}
		for _, p := range patterns {
			if !strings.Contains(line, p) {
				continue
			}
			return DoctorCheck{
				Name:   "operator reconcile errors (last 10m)",
				OK:     false,
				Detail: fmt.Sprintf("detected %q in recent operator logs", p),
				Remedy: "inspect `kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager --since=10m`",
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return DoctorCheck{
			Name:   "operator reconcile errors (last 10m)",
			OK:     false,
			Detail: fmt.Sprintf("failed scanning operator logs: %v", err),
			Remedy: "inspect `kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager --since=10m`",
		}
	}
	return DoctorCheck{
		Name:   "operator reconcile errors (last 10m)",
		OK:     true,
		Detail: "no reconcile error patterns detected",
	}
}

func checkOperatorClusterRoleRules(kubectl core.KubectlRunner) DoctorCheck {
	const name = "operator ClusterRole rules"
	const remedy = "run `mcp-runtime cluster operator-rbac apply --kubeconfig <path> --context <name> --yes` to restore the shipped ClusterRole; the controller-runtime informer cache will not sync without these"

	cmd, err := kubectl.CommandArgs([]string{"get", "clusterrole", "mcp-runtime-operator-role", "-o", "json"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("kubectl error: %v", err), Remedy: remedy}
	}
	out, err := runKubectlBytes(cmd)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("ClusterRole mcp-runtime-operator-role not readable: %v", err), Remedy: remedy}
	}

	var role struct {
		Rules []struct {
			APIGroups []string `json:"apiGroups"`
			Resources []string `json:"resources"`
			Verbs     []string `json:"verbs"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(out, &role); err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("could not parse ClusterRole JSON: %v", err), Remedy: remedy}
	}

	required := []string{"get", "list", "watch"}

	var missing []string
	for _, want := range operatorClusterRoleResources {
		verbs := map[string]bool{}
		for _, rule := range role.Rules {
			if !operatorClusterRoleRuleIncludes(rule.APIGroups, want.APIGroup) ||
				!operatorClusterRoleRuleIncludes(rule.Resources, want.Resource) {
				continue
			}
			for _, verb := range rule.Verbs {
				verbs[verb] = true
			}
		}
		if !operatorClusterRoleHasRequiredVerbs(verbs, required) {
			missing = append(missing, operatorClusterRoleResourceLabel(want))
		}
	}

	if len(missing) > 0 {
		return DoctorCheck{
			Name:   name,
			OK:     false,
			Detail: fmt.Sprintf("ClusterRole mcp-runtime-operator-role missing get/list/watch on: %s", strings.Join(missing, ", ")),
			Remedy: remedy,
		}
	}
	return DoctorCheck{
		Name:   name,
		OK:     true,
		Detail: fmt.Sprintf("get/list/watch present for %d expected resources", len(operatorClusterRoleResources)),
	}
}

func operatorClusterRoleRuleIncludes(values []string, want string) bool {
	for _, got := range values {
		if got == want || got == "*" {
			return true
		}
	}
	return false
}

func operatorClusterRoleHasRequiredVerbs(verbs map[string]bool, required []string) bool {
	if verbs["*"] {
		return true
	}
	for _, verb := range required {
		if !verbs[verb] {
			return false
		}
	}
	return true
}

func operatorClusterRoleResourceLabel(resource operatorClusterRoleResource) string {
	if resource.APIGroup == "" {
		return resource.Resource
	}
	return resource.Resource + "." + resource.APIGroup
}
