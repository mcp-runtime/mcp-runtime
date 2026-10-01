package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/kubeworkload"
)

func checkRegistryService(kubectl core.KubectlRunner) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "svc", "-n", "registry", "registry", "-o", "jsonpath={.spec.ports[0].nodePort}"})
	if err != nil {
		return DoctorCheck{Name: "registry Service", OK: false, Detail: fmt.Sprintf("kubectl error: %v", err), Remedy: "run `./bin/mcp-runtime setup` to install the registry, or check cluster connectivity"}
	}
	out, err := cmd.Output()
	port := strings.TrimSpace(string(out))
	if err != nil || port == "" {
		return DoctorCheck{
			Name:   "registry Service",
			OK:     false,
			Detail: "Service registry/registry not found or has no NodePort",
			Remedy: "run `./bin/mcp-runtime setup` to install the registry",
		}
	}
	return DoctorCheck{
		Name:   "registry Service",
		OK:     true,
		Detail: fmt.Sprintf("NodePort %s", port),
	}
}

// checkRegistryReachableFromCluster verifies that an in-cluster pod can talk to
// the registry over the cluster-internal service DNS. This exercises the same
// path the in-cluster push helper uses, so a failure here means `admin registry push
// --mode in-cluster` will also fail. Kubelet's pull path (node-side containerd
// with registries.yaml mirrors) is distribution-specific and surfaced via the
// remediation hint, not as a pass/fail check — we can't reach into kubelet
// non-destructively.
func checkRegistryReachableFromCluster(kubectl core.KubectlRunner) DoctorCheck {
	podName := fmt.Sprintf("mcp-runtime-doctor-curl-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	registryURL := doctorRegistryServiceURL(kubectl)
	curlArgs := []string{
		"-skSI", "--connect-timeout", "5", "--max-time", "15",
		registryURL,
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", "registry", "--ignore-not-found"})
	}()
	args := []string{
		"run", podName,
		"-n", "registry",
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", curlArgs...),
	}
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return DoctorCheck{
			Name:   "registry reachability (in-cluster)",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check cluster connectivity and kubeconfig",
		}
	}
	createOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		detail := fmt.Sprintf("helper pod failed: %v", runErr)
		if strings.TrimSpace(string(createOut)) != "" {
			detail += ": " + strings.TrimSpace(string(createOut))
		}
		return DoctorCheck{
			Name:   "registry reachability (in-cluster)",
			OK:     false,
			Detail: detail,
			Remedy: "run `./bin/mcp-runtime setup` if the registry is missing; check `kubectl -n registry get pods`",
		}
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, "registry", 90*time.Second); err != nil {
		logs, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", "registry", "--tail=50"})
		termination, _ := readKubectlOutput(kubectl, []string{
			"get", "pod", podName, "-n", "registry", "-o",
			"jsonpath={.status.containerStatuses[0].state.terminated.reason}|{.status.containerStatuses[0].state.terminated.exitCode}|{.status.containerStatuses[0].state.terminated.message}",
		})
		detail := fmt.Sprintf("helper pod did not succeed: %v", err)
		if termination = strings.Trim(termination, " |\n\t"); termination != "" {
			detail += "; container termination: " + termination
		}
		if strings.TrimSpace(logs) != "" {
			detail += ": " + strings.TrimSpace(logs)
		} else if logsErr != nil {
			detail += "; logs unavailable: " + logsErr.Error()
		}
		return DoctorCheck{
			Name:   "registry reachability (in-cluster)",
			OK:     false,
			Detail: detail,
			Remedy: "run `./bin/mcp-runtime setup` if the registry is missing; check `kubectl -n registry get pods`",
		}
	}
	body, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", "registry"})
	if logsErr != nil {
		return DoctorCheck{
			Name:   "registry reachability (in-cluster)",
			OK:     false,
			Detail: fmt.Sprintf("failed reading helper pod logs: %v", logsErr),
			Remedy: "inspect pod events: `kubectl -n registry describe pod " + podName + "`",
		}
	}
	if !hasHTTP200Status(body) {
		return DoctorCheck{
			Name:   "registry reachability (in-cluster)",
			OK:     false,
			Detail: fmt.Sprintf("unexpected response: %q", strings.TrimSpace(body)),
			Remedy: "inspect the registry deployment: `kubectl -n registry get pods -o wide`",
		}
	}
	return DoctorCheck{
		Name:   "registry reachability (in-cluster)",
		OK:     true,
		Detail: fmt.Sprintf("HTTP 200 from %s", registryURL),
	}
}

func doctorRegistryServiceURL(kubectl core.KubectlRunner) string {
	scheme := "http"
	if doctorRegistryInternalTLSConfigured(kubectl) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d/v2/", scheme, doctorServiceDNS("registry", "registry"), doctorRegistryServicePort(kubectl))
}

func doctorRegistryServicePort(kubectl core.KubectlRunner) int {
	out, err := readKubectlOutput(kubectl, []string{"get", "svc", "registry", "-n", "registry", "-o", "jsonpath={.spec.ports[0].port}"})
	if err == nil {
		if port, parseErr := strconv.Atoi(strings.TrimSpace(out)); parseErr == nil && port > 0 {
			return port
		}
	}
	return 5000
}

func doctorRegistryServiceScheme(registryURL string) string {
	scheme, _, found := strings.Cut(registryURL, "://")
	if !found || strings.TrimSpace(scheme) == "" {
		return "registry service"
	}
	return strings.ToUpper(scheme)
}

func doctorRegistryInternalTLSConfigured(kubectl core.KubectlRunner) bool {
	out, err := readKubectlOutput(kubectl, []string{
		"get", "secret", "registry-internal-tls",
		"-n", "registry",
		"-o", "jsonpath={.metadata.name}",
	})
	return err == nil && strings.TrimSpace(out) == "registry-internal-tls"
}

func checkMCPServersImagePullSecrets(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "serviceaccount", kubeworkload.DefaultServiceAccountName, "-n", namespace, "-o", "jsonpath={range .imagePullSecrets[*]}{.name}{\"\\n\"}{end}"})
	if err != nil {
		return DoctorCheck{
			Name:   "mcp-servers imagePullSecrets",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: fmt.Sprintf("check serviceaccount %s in %s", kubeworkload.DefaultServiceAccountName, namespace),
		}
	}
	out, execErr := cmd.CombinedOutput()
	if execErr != nil {
		if isKubectlNotFound(execErr) || isKubectlNotFoundOutput(string(out)) {
			return DoctorCheck{
				Name:   "mcp-servers imagePullSecrets",
				OK:     true,
				Detail: fmt.Sprintf("serviceaccount %s not provisioned yet (created on first managed server deploy)", kubeworkload.DefaultServiceAccountName),
			}
		}
		return DoctorCheck{
			Name:   "mcp-servers imagePullSecrets",
			OK:     false,
			Detail: fmt.Sprintf("failed reading serviceaccount %s imagePullSecrets", kubeworkload.DefaultServiceAccountName),
			Remedy: fmt.Sprintf("inspect `kubectl -n %s get sa %s -o yaml`", namespace, kubeworkload.DefaultServiceAccountName),
		}
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return DoctorCheck{
			Name:   "mcp-servers imagePullSecrets",
			OK:     true,
			Detail: fmt.Sprintf("no imagePullSecrets configured on serviceaccount %s", kubeworkload.DefaultServiceAccountName),
		}
	}
	names := strings.Split(raw, "\n")
	for _, name := range names {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		if _, getErr := readKubectlOutput(kubectl, []string{"get", "secret", n, "-n", namespace, "-o", "jsonpath={.metadata.name}"}); getErr != nil {
			return DoctorCheck{
				Name:   "mcp-servers imagePullSecrets",
				OK:     false,
				Detail: fmt.Sprintf("referenced imagePullSecret %s is missing", n),
				Remedy: fmt.Sprintf("create secret %s in namespace %s or update serviceaccount", n, namespace),
			}
		}
	}
	return DoctorCheck{
		Name:   "mcp-servers imagePullSecrets",
		OK:     true,
		Detail: fmt.Sprintf("%d imagePullSecrets present", len(names)),
	}
}

func checkMCPServersImagePullSmoke(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	image, imageSource := resolveDoctorSmokeImage(kubectl, namespace)
	pullSecrets := resolveDoctorImagePullSecrets(kubectl, namespace)
	podName := fmt.Sprintf("doctor-pull-%d", time.Now().UnixNano())
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", namespace, "--ignore-not-found"})
	}()
	createCmd, cmdErr := kubectl.CommandArgs([]string{
		"run", podName,
		"-n", namespace,
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverridesWithPullSecrets(podName, image, "", pullSecrets),
	})
	if cmdErr != nil {
		return DoctorCheck{
			Name:   "mcp-servers image pull smoke",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", cmdErr),
			Remedy: "check kubectl setup",
		}
	}
	createOut, err := createCmd.CombinedOutput()
	if err != nil {
		return DoctorCheck{
			Name:   "mcp-servers image pull smoke",
			OK:     false,
			Detail: fmt.Sprintf("failed creating smoke pod: %v: %s", err, strings.TrimSpace(string(createOut))),
			Remedy: "check pull credentials, registry reachability, and image existence",
		}
	}
	if err := waitForDoctorPodImagePulled(kubectl, podName, namespace, 90*time.Second); err != nil {
		describe, describeErr := readKubectlOutput(kubectl, []string{"describe", "pod", podName, "-n", namespace})
		detail := fmt.Sprintf("pod image was not pulled: %v", err)
		if describeErr == nil && strings.TrimSpace(describe) != "" {
			detail += "; " + firstMatchingSnippet(describe, "Events:", "Failed", "ErrImagePull", "ImagePullBackOff", "Back-off")
		}
		return DoctorCheck{
			Name:   "mcp-servers image pull smoke",
			OK:     false,
			Detail: detail,
			Remedy: "inspect pod events: `kubectl -n mcp-servers describe pod " + podName + "`",
		}
	}
	return DoctorCheck{
		Name:   "mcp-servers image pull smoke",
		OK:     true,
		Detail: fmt.Sprintf("pull/ready succeeded using image %s (%s)", image, imageSource),
	}
}

func resolveDoctorImagePullSecrets(kubectl core.KubectlRunner, namespace string) []string {
	if out, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-n", namespace, "-o", "jsonpath={range .items[*]}{.status.readyReplicas}|{.spec.template.spec.imagePullSecrets[*].name}{\"\\n\"}{end}"}); err == nil {
		for _, line := range filterNonEmptyLines(out) {
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[0]) != "0" && strings.TrimSpace(parts[1]) != "" {
				return strings.Fields(parts[1])
			}
		}
	}
	for _, serviceAccount := range []string{"mcp-workload", "default"} {
		out, err := readKubectlOutput(kubectl, []string{"get", "serviceaccount", serviceAccount, "-n", namespace, "-o", "jsonpath={.imagePullSecrets[*].name}"})
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.Fields(out)
		}
	}
	return nil
}

func waitForDoctorPodImagePulled(kubectl core.KubectlRunner, name, namespace string, timeout time.Duration) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastStatus string
	for {
		imageID, imageErr := readKubectlOutput(kubectl, []string{"get", "pod", name, "-n", namespace, "-o", "jsonpath={.status.containerStatuses[0].imageID}"})
		if imageErr == nil && strings.TrimSpace(imageID) != "" {
			return nil
		}

		reason, _ := readKubectlOutput(kubectl, []string{"get", "pod", name, "-n", namespace, "-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}"})
		reason = strings.TrimSpace(reason)
		if isImagePullWaitingReason(reason) {
			return core.NewWithSentinel(core.ErrDoctorImagePullStatusFailed, reason)
		}

		phase, _ := readKubectlOutput(kubectl, []string{"get", "pod", name, "-n", namespace, "-o", "jsonpath={.status.phase}"})
		lastStatus = strings.TrimSpace(phase)
		if reason != "" {
			lastStatus = reason
		}
		if lastStatus == "" && imageErr != nil {
			lastStatus = imageErr.Error()
		}

		select {
		case <-timeoutTimer.C:
			if lastStatus == "" {
				lastStatus = "timed out"
			}
			return core.NewWithSentinel(core.ErrDoctorImagePullStatusFailed, lastStatus)
		case <-ticker.C:
		}
	}
}

func waitForDoctorPodSucceeded(kubectl core.KubectlRunner, name, namespace string, timeout time.Duration) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastStatus string
	for {
		phase, phaseErr := readKubectlOutput(kubectl, []string{"get", "pod", name, "-n", namespace, "-o", "jsonpath={.status.phase}"})
		phase = strings.TrimSpace(phase)
		switch phase {
		case "Succeeded":
			return nil
		case "Failed":
			return core.NewWithSentinel(core.ErrDoctorPodPhaseFailed, "pod phase Failed")
		}

		reason, _ := readKubectlOutput(kubectl, []string{"get", "pod", name, "-n", namespace, "-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}"})
		reason = strings.TrimSpace(reason)
		if isImagePullWaitingReason(reason) {
			return core.NewWithSentinel(core.ErrDoctorImagePullStatusFailed, reason)
		}
		lastStatus = phase
		if reason != "" {
			lastStatus = reason
		}
		if lastStatus == "" && phaseErr != nil {
			lastStatus = phaseErr.Error()
		}

		select {
		case <-timeoutTimer.C:
			if lastStatus == "" {
				lastStatus = "timed out"
			}
			return core.NewWithSentinel(core.ErrDoctorImagePullStatusFailed, lastStatus)
		case <-ticker.C:
		}
	}
}

func isImagePullWaitingReason(reason string) bool {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff", "InvalidImageName":
		return true
	default:
		return false
	}
}

func restrictedRunOverrides(containerName, image, command string, args ...string) string {
	return restrictedRunOverridesWithPullSecrets(containerName, image, command, nil, args...)
}

func restrictedRunOverridesWithPullSecrets(containerName, image, command string, pullSecrets []string, args ...string) string {
	container := map[string]any{
		"name":       strings.TrimSpace(containerName),
		"image":      strings.TrimSpace(image),
		"workingDir": "/tmp",
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"runAsNonRoot":             true,
			"runAsUser":                doctorRestrictedRunAsUser,
			"capabilities": map[string]any{
				"drop": []string{"ALL"},
			},
		},
	}
	if strings.TrimSpace(command) != "" {
		container["command"] = []string{strings.TrimSpace(command)}
	}
	if len(args) > 0 {
		container["args"] = args
	}
	overrides := map[string]any{
		"spec": map[string]any{
			"automountServiceAccountToken": false,
			"securityContext": map[string]any{
				"runAsNonRoot": true,
				"runAsUser":    doctorRestrictedRunAsUser,
				"seccompProfile": map[string]any{
					"type": "RuntimeDefault",
				},
			},
			"containers": []map[string]any{container},
		},
	}
	if len(pullSecrets) > 0 {
		refs := make([]map[string]string, 0, len(pullSecrets))
		for _, name := range pullSecrets {
			if strings.TrimSpace(name) != "" {
				refs = append(refs, map[string]string{"name": strings.TrimSpace(name)})
			}
		}
		if len(refs) > 0 {
			overrides["spec"].(map[string]any)["imagePullSecrets"] = refs
		}
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func checkRegistryHTTPPullMismatch(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "pods", "-A", "-o", buildImagePullJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "registry HTTP pull mismatch",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check API connectivity and RBAC for listing pods",
		}
	}

	candidates := parseImagePullCandidates(out)
	if len(candidates) == 0 {
		return DoctorCheck{
			Name:   "registry HTTP pull mismatch",
			OK:     true,
			Detail: "no ErrImagePull/ImagePullBackOff pods detected",
		}
	}

	// First pass: check waiting messages already returned by `kubectl get`.
	// Cheap and avoids any describe calls when the kubelet event is fresh.
	for _, candidate := range candidates {
		if hasRegistryHTTPPullMismatchMessage(candidate.Messages) {
			return mismatchResult(candidate)
		}
	}

	// Second pass: fall back to `kubectl describe`, capped so a cluster
	// with many ImagePullBackOff pods doesn't shell out indefinitely.
	describeFailures := 0
	var firstDescribeFailure string
	inspected := 0
	for _, candidate := range candidates {
		if inspected >= imagePullDescribeLimit {
			break
		}
		inspected++
		describe, err := readKubectlOutput(kubectl, []string{"describe", "pod", candidate.Name, "-n", candidate.Namespace})
		if err != nil {
			describeFailures++
			if firstDescribeFailure == "" {
				firstDescribeFailure = fmt.Sprintf("%s/%s: %v", candidate.Namespace, candidate.Name, err)
			}
			continue
		}
		if strings.Contains(describe, registryHTTPPullMismatch) {
			return mismatchResult(candidate)
		}
	}
	if describeFailures > 0 {
		detail := fmt.Sprintf("pod inspection failed for %d/%d ErrImagePull/ImagePullBackOff candidate(s); first error: %s", describeFailures, inspected, firstDescribeFailure)
		if inspected < len(candidates) {
			detail += fmt.Sprintf(" (inspected first %d of %d)", inspected, len(candidates))
		}
		return DoctorCheck{
			Name:   "registry HTTP pull mismatch",
			OK:     false,
			Detail: detail,
			Remedy: "inspect pull-failing pods manually with `kubectl describe pod <name> -n <namespace>` and check kubectl RBAC/connectivity",
		}
	}

	detail := fmt.Sprintf("%d ErrImagePull/ImagePullBackOff pod(s) found, none with HTTP-vs-HTTPS registry mismatch events", len(candidates))
	if inspected < len(candidates) {
		detail = fmt.Sprintf("%d ErrImagePull/ImagePullBackOff pod(s) found; inspected first %d, none with HTTP-vs-HTTPS registry mismatch events", len(candidates), inspected)
	}
	return DoctorCheck{
		Name:   "registry HTTP pull mismatch",
		OK:     true,
		Detail: detail,
	}
}

func checkRegistryServiceIPImageRefs(kubectl core.KubectlRunner) DoctorCheck {
	serviceIP, err := readKubectlOutput(kubectl, []string{"get", "svc", "registry", "-n", "registry", "-o", "jsonpath={.spec.clusterIP}"})
	serviceIP = strings.TrimSpace(serviceIP)
	if err != nil || serviceIP == "" || strings.EqualFold(serviceIP, "none") {
		return DoctorCheck{
			Name:   "MCPServer registry image refs",
			OK:     true,
			Detail: "registry Service IP unavailable; skipped MCPServer image reference check",
		}
	}

	out, err := readKubectlOutput(kubectl, []string{"get", "mcpservers", "-A", "-o", buildMCPServerImageJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "MCPServer registry image refs",
			OK:     false,
			Detail: fmt.Sprintf("failed listing MCPServers: %v", err),
			Remedy: "check MCPServer CRD availability and RBAC for listing MCPServers across namespaces",
		}
	}
	refs := parseMCPServerImageRefs(out)
	for _, ref := range refs {
		host := registryHostFromImageRef(ref.Image)
		if registryHostMatchesServiceIP(host, serviceIP) {
			return DoctorCheck{
				Name:   "MCPServer registry image refs",
				OK:     false,
				Detail: fmt.Sprintf("MCPServer %s/%s uses registry Service IP image %s; kubelet verifies registry TLS against the image host and bundled registry certs do not cover ClusterIP addresses", ref.Namespace, ref.Name, ref.Image),
				Remedy: "reapply or patch the MCPServer to use the pullable registry host, for example registry.<domain>/<scope>/<image>, then restart the workload pod",
			}
		}
	}
	return DoctorCheck{
		Name:   "MCPServer registry image refs",
		OK:     true,
		Detail: fmt.Sprintf("%d MCPServer image reference(s) checked; none use registry Service IP %s", len(refs), serviceIP),
	}
}

func checkMCPServerImagePullSecrets(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "mcpservers", "-A", "-o", buildMCPServerPullSecretJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "MCPServer imagePullSecrets",
			OK:     false,
			Detail: fmt.Sprintf("failed listing MCPServers: %v", err),
			Remedy: "check MCPServer CRD availability and RBAC for listing MCPServers across namespaces",
		}
	}
	refs := parseMCPServerPullSecretRefs(out)
	if len(refs) == 0 {
		return DoctorCheck{
			Name:   "MCPServer imagePullSecrets",
			OK:     true,
			Detail: "no MCPServer spec.imagePullSecrets configured",
		}
	}
	for _, ref := range refs {
		if _, err := readKubectlOutput(kubectl, []string{"get", "secret", ref.Secret, "-n", ref.Namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
			return DoctorCheck{
				Name:   "MCPServer imagePullSecrets",
				OK:     false,
				Detail: fmt.Sprintf("MCPServer %s/%s references missing imagePullSecret %s", ref.Namespace, ref.Server, ref.Secret),
				Remedy: "create the docker-registry Secret in the MCPServer namespace or patch spec.imagePullSecrets to an existing registry credential",
			}
		}
	}
	return DoctorCheck{
		Name:   "MCPServer imagePullSecrets",
		OK:     true,
		Detail: fmt.Sprintf("%d MCPServer imagePullSecret reference(s) exist", len(refs)),
	}
}

func checkRegistryImagePullDiagnostics(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "pods", "-A", "-o", buildImagePullJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "registry image pull diagnostics",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check API connectivity and RBAC for listing pods",
		}
	}
	candidates := parseImagePullCandidates(out)
	if len(candidates) == 0 {
		return DoctorCheck{
			Name:   "registry image pull diagnostics",
			OK:     true,
			Detail: "no ErrImagePull/ImagePullBackOff pods detected",
		}
	}
	for _, candidate := range candidates {
		if diagnosis, ok := classifyImagePullDiagnostic(candidate.Messages); ok {
			return imagePullDiagnosticResult(candidate, diagnosis)
		}
	}
	describeFailures := 0
	var firstDescribeFailure string
	inspected := 0
	for _, candidate := range candidates {
		if inspected >= imagePullDescribeLimit {
			break
		}
		inspected++
		describe, err := readKubectlOutput(kubectl, []string{"describe", "pod", candidate.Name, "-n", candidate.Namespace})
		if err != nil {
			describeFailures++
			if firstDescribeFailure == "" {
				firstDescribeFailure = fmt.Sprintf("%s/%s: %v", candidate.Namespace, candidate.Name, err)
			}
			continue
		}
		if diagnosis, ok := classifyImagePullDiagnostic([]string{describe}); ok {
			return imagePullDiagnosticResult(candidate, diagnosis)
		}
	}
	if describeFailures > 0 {
		detail := fmt.Sprintf("pod inspection failed for %d/%d ErrImagePull/ImagePullBackOff candidate(s); first error: %s", describeFailures, inspected, firstDescribeFailure)
		if inspected < len(candidates) {
			detail += fmt.Sprintf(" (inspected first %d of %d)", inspected, len(candidates))
		}
		return DoctorCheck{
			Name:   "registry image pull diagnostics",
			OK:     false,
			Detail: detail,
			Remedy: "inspect pull-failing pods manually with `kubectl describe pod <name> -n <namespace>` and check kubectl RBAC/connectivity",
		}
	}
	detail := fmt.Sprintf("%d ErrImagePull/ImagePullBackOff pod(s) found, none matched known registry TLS/auth/DNS/corrupt-manifest patterns", len(candidates))
	if inspected < len(candidates) {
		detail = fmt.Sprintf("%d ErrImagePull/ImagePullBackOff pod(s) found; inspected first %d, none matched known registry TLS/auth/DNS/corrupt-manifest patterns", len(candidates), inspected)
	}
	return DoctorCheck{
		Name:   "registry image pull diagnostics",
		OK:     true,
		Detail: detail,
	}
}

// buildImagePullJSONPath returns the kubectl jsonpath used by the HTTP
// mismatch check. Inner list items are joined with imagePullListSep (0x1f)
// so commas in kubelet messages don't fragment the parse.
func buildImagePullJSONPath() string {
	return fmt.Sprintf(
		`jsonpath={range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{range .spec.initContainers[*]}{.image}{"%[1]s"}{end}{range .spec.containers[*]}{.image}{"%[1]s"}{end}{"|"}{range .status.initContainerStatuses[*]}{.state.waiting.reason}{"%[1]s"}{end}{range .status.containerStatuses[*]}{.state.waiting.reason}{"%[1]s"}{end}{"|"}{range .status.initContainerStatuses[*]}{.state.waiting.message}{"%[1]s"}{end}{range .status.containerStatuses[*]}{.state.waiting.message}{"%[1]s"}{end}{"\n"}{end}`,
		imagePullListSep,
	)
}

func buildMCPServerImageJSONPath() string {
	return `jsonpath={range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.spec.image}{"\n"}{end}`
}

func buildMCPServerPullSecretJSONPath() string {
	return fmt.Sprintf(`jsonpath={range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{range .spec.imagePullSecrets[*]}{.name}{"%s"}{end}{"\n"}{end}`, imagePullListSep)
}

func mismatchResult(c imagePullPodCandidate) DoctorCheck {
	image := firstNonEmpty(c.Images, "unknown")
	reason := pickImagePullReason(c.Reasons)
	var detail string
	if reason != "" {
		detail = fmt.Sprintf("pod %s/%s image %s (%s) failed pull: %s", c.Namespace, c.Name, image, reason, registryHTTPPullMismatch)
	} else {
		detail = fmt.Sprintf("pod %s/%s image %s failed pull: %s", c.Namespace, c.Name, image, registryHTTPPullMismatch)
	}
	return DoctorCheck{
		Name:   "registry HTTP pull mismatch",
		OK:     false,
		Detail: detail,
		Remedy: "Registry HTTP pull mismatch: kubelet tried HTTPS against the HTTP registry. Configure the node container runtime's insecure registry mirror for the exact image host, or use TLS.",
	}
}

type imagePullDiagnosis struct {
	Category string
	Message  string
	Remedy   string
}

func imagePullDiagnosticResult(c imagePullPodCandidate, d imagePullDiagnosis) DoctorCheck {
	image := firstNonEmpty(c.Images, "unknown")
	reason := pickImagePullReason(c.Reasons)
	var detail string
	if reason != "" {
		detail = fmt.Sprintf("pod %s/%s image %s (%s) has registry %s issue: %s", c.Namespace, c.Name, image, reason, d.Category, d.Message)
	} else {
		detail = fmt.Sprintf("pod %s/%s image %s has registry %s issue: %s", c.Namespace, c.Name, image, d.Category, d.Message)
	}
	return DoctorCheck{
		Name:   "registry image pull diagnostics",
		OK:     false,
		Detail: detail,
		Remedy: d.Remedy,
	}
}

func classifyImagePullDiagnostic(messages []string) (imagePullDiagnosis, bool) {
	for _, message := range messages {
		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "x509:") ||
			strings.Contains(lower, "certificate signed by unknown authority") ||
			strings.Contains(lower, "doesn't contain any ip sans") ||
			strings.Contains(lower, "cannot validate certificate") ||
			strings.Contains(lower, "tls: failed to verify certificate"):
			return imagePullDiagnosis{
				Category: "TLS",
				Message:  firstMatchingSnippet(message, "x509:", "certificate signed by unknown authority", "cannot validate certificate", "IP SANs", "tls: failed to verify certificate"),
				Remedy:   "use the public registry hostname covered by the registry certificate, add the registry CA to every node runtime, or configure the exact registry mirror host in the node runtime",
			}, true
		case strings.Contains(lower, "no basic auth credentials") ||
			strings.Contains(lower, "pull access denied") ||
			strings.Contains(lower, "authorization failed") ||
			strings.Contains(lower, "401 unauthorized") ||
			strings.Contains(lower, "403 forbidden") ||
			strings.Contains(lower, "insufficient_scope"):
			return imagePullDiagnosis{
				Category: "auth",
				Message:  firstMatchingSnippet(message, "no basic auth credentials", "pull access denied", "authorization failed", "401 Unauthorized", "403 Forbidden", "insufficient_scope"),
				Remedy:   "create a registry credential and reference it from spec.imagePullSecrets or the namespace service account; verify the credential is allowed to pull the image repository",
			}, true
		case strings.Contains(lower, "no such host") ||
			strings.Contains(lower, "server misbehaving") ||
			strings.Contains(lower, "lookup ") && strings.Contains(lower, "/v2/"):
			return imagePullDiagnosis{
				Category: "DNS",
				Message:  firstMatchingSnippet(message, "lookup", "no such host", "server misbehaving"),
				Remedy:   "fix node DNS for the registry host or configure the node container runtime mirror for the exact image host",
			}, true
		case strings.Contains(lower, "manifest unknown") ||
			strings.Contains(lower, "not found") ||
			strings.Contains(lower, "invalid tar header") ||
			strings.Contains(lower, "unexpected eof"):
			return imagePullDiagnosis{
				Category: "image",
				Message:  firstMatchingSnippet(message, "manifest unknown", "not found", "invalid tar header", "unexpected EOF"),
				Remedy:   "verify the image tag exists in the registry and that the pushed manifest/layer media types are valid for kubelet pulls",
			}, true
		}
	}
	return imagePullDiagnosis{}, false
}

func firstMatchingSnippet(message string, needles ...string) string {
	for _, needle := range needles {
		idx := strings.Index(strings.ToLower(message), strings.ToLower(needle))
		if idx < 0 {
			continue
		}
		end := idx + 180
		if end > len(message) {
			end = len(message)
		}
		return strings.TrimSpace(message[idx:end])
	}
	message = strings.TrimSpace(message)
	if len(message) > 180 {
		return message[:180]
	}
	return message
}

func pickImagePullReason(reasons []string) string {
	for _, r := range reasons {
		switch trimmed := strings.TrimSpace(r); trimmed {
		case "Init:ErrImagePull", "Init:ImagePullBackOff", "ErrImagePull", "ImagePullBackOff":
			return trimmed
		}
	}
	return ""
}

func parseImagePullCandidates(value string) []imagePullPodCandidate {
	var candidates []imagePullPodCandidate
	for _, line := range filterNonEmptyLines(value) {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 4 {
			continue
		}
		reasons := splitSepTrim(parts[3], imagePullListSep)
		messages := []string{}
		if len(parts) == 5 {
			messages = splitSepTrim(parts[4], imagePullListSep)
		}
		if !hasImagePullReason(reasons) && !hasRegistryHTTPPullMismatchMessage(messages) {
			continue
		}
		candidates = append(candidates, imagePullPodCandidate{
			Namespace: strings.TrimSpace(parts[0]),
			Name:      strings.TrimSpace(parts[1]),
			Images:    splitSepTrim(parts[2], imagePullListSep),
			Reasons:   reasons,
			Messages:  messages,
		})
	}
	return candidates
}

func parseMCPServerImageRefs(value string) []mcpServerImageRef {
	var refs []mcpServerImageRef
	for _, line := range filterNonEmptyLines(value) {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		ref := mcpServerImageRef{
			Namespace: strings.TrimSpace(parts[0]),
			Name:      strings.TrimSpace(parts[1]),
			Image:     strings.TrimSpace(parts[2]),
		}
		if ref.Namespace == "" || ref.Name == "" || ref.Image == "" {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

func parseMCPServerPullSecretRefs(value string) []mcpServerPullSecretRef {
	var refs []mcpServerPullSecretRef
	for _, line := range filterNonEmptyLines(value) {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		namespace := strings.TrimSpace(parts[0])
		server := strings.TrimSpace(parts[1])
		if namespace == "" || server == "" {
			continue
		}
		for _, secret := range splitSepTrim(parts[2], imagePullListSep) {
			if secret == "" {
				continue
			}
			refs = append(refs, mcpServerPullSecretRef{
				Namespace: namespace,
				Server:    server,
				Secret:    secret,
			})
		}
	}
	return refs
}

func registryHostFromImageRef(image string) string {
	first, _, found := strings.Cut(strings.TrimSpace(image), "/")
	if !found {
		return ""
	}
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first
	}
	return ""
}

func registryHostMatchesServiceIP(host, serviceIP string) bool {
	host = strings.TrimSpace(host)
	serviceIP = strings.TrimSpace(serviceIP)
	if host == "" || serviceIP == "" {
		return false
	}
	return host == serviceIP || strings.HasPrefix(host, serviceIP+":")
}

func hasImagePullReason(reasons []string) bool {
	for _, reason := range reasons {
		switch strings.TrimSpace(reason) {
		case "ErrImagePull", "ImagePullBackOff", "Init:ErrImagePull", "Init:ImagePullBackOff":
			return true
		}
	}
	return false
}

func hasRegistryHTTPPullMismatchMessage(messages []string) bool {
	for _, message := range messages {
		if strings.Contains(message, registryHTTPPullMismatch) {
			return true
		}
	}
	return false
}

func checkMCPServerReconcileSmoke(kubectl core.KubectlRunner, namespace string) DoctorCheck {
	target := resolveDoctorSmokeTarget(kubectl, namespace)
	pullSecrets := resolveDoctorImagePullSecrets(kubectl, namespace)
	name := fmt.Sprintf("doctor-smoke-%d", time.Now().UnixNano()%1_000_000)
	pullSecretYAML := ""
	if len(pullSecrets) > 0 {
		items := make([]string, 0, len(pullSecrets))
		for _, secret := range pullSecrets {
			items = append(items, fmt.Sprintf("    - %s", secret))
		}
		pullSecretYAML = "  imagePullSecrets:\n" + strings.Join(items, "\n") + "\n"
	}
	ingressClass := strings.TrimSpace(os.Getenv("MCP_DEFAULT_INGRESS_CLASS"))
	if ingressClass == "" {
		ingressClass = "traefik"
	}
	ingressEntryPoints := strings.TrimSpace(os.Getenv("MCP_DEFAULT_INGRESS_ENTRYPOINTS"))
	if ingressEntryPoints == "" {
		ingressEntryPoints = "web"
	}
	servicePort := target.Port
	if configured := strings.TrimSpace(os.Getenv("MCP_DEFAULT_SERVICE_PORT")); configured != "" {
		if parsed, err := strconv.ParseUint(configured, 10, 16); err == nil && parsed > 0 {
			servicePort = int32(parsed)
		}
	}
	manifest := fmt.Sprintf(`apiVersion: mcpruntime.org/v1alpha1
kind: MCPServer
metadata:
  name: %s
  namespace: %s
spec:
  image: %s
%s
  port: %d
  servicePort: %d
  publicPathPrefix: %s
  ingressClass: %s
  ingressAnnotations:
    traefik.ingress.kubernetes.io/router.entrypoints: %s
`, name, namespace, strings.TrimSpace(target.Image), pullSecretYAML, target.Port, servicePort, name, ingressClass, ingressEntryPoints)
	cleanup := func() {
		_ = kubectl.Run([]string{"delete", "mcpserver", name, "-n", namespace, "--ignore-not-found"})
		_ = kubectl.Run([]string{"delete", "deploy", name, "-n", namespace, "--ignore-not-found"})
		_ = kubectl.Run([]string{"delete", "svc", name, "-n", namespace, "--ignore-not-found"})
		_ = kubectl.Run([]string{"delete", "ingress", name, "-n", namespace, "--ignore-not-found"})
		_ = kubectl.Run([]string{"delete", "ingressroute.traefik.io", name, "-n", namespace, "--ignore-not-found"})
	}
	defer cleanup()

	applyCmd, cmdErr := kubectl.CommandArgs([]string{"apply", "-f", "-"})
	if cmdErr != nil {
		return DoctorCheck{
			Name:   "MCPServer reconcile smoke",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", cmdErr),
			Remedy: "check kubeconfig access",
		}
	}
	applyCmd.SetStdin(strings.NewReader(manifest))
	if out, runErr := applyCmd.CombinedOutput(); runErr != nil {
		return DoctorCheck{
			Name:   "MCPServer reconcile smoke",
			OK:     false,
			Detail: fmt.Sprintf("failed to apply smoke MCPServer: %s", strings.TrimSpace(string(out))),
			Remedy: "check MCPServer webhook/CRD/operator availability",
		}
	}

	if err := waitForDoctorResource(kubectl, "deployment", name, namespace, 150*time.Second); err != nil {
		return DoctorCheck{
			Name:   "MCPServer reconcile smoke",
			OK:     false,
			Detail: fmt.Sprintf("deployment was not created: %v", err),
			Remedy: "inspect operator reconcile errors and MCPServer status",
		}
	}
	if target.WaitForReady {
		if err := waitForDoctorDeploymentReady(kubectl, name, namespace, 150*time.Second); err != nil {
			return DoctorCheck{
				Name:   "MCPServer reconcile smoke",
				OK:     false,
				Detail: fmt.Sprintf("deployment did not become ready: %v", err),
				Remedy: "inspect operator reconcile and smoke deployment events",
			}
		}
	}
	if !target.WaitForReady {
		if err := waitForDoctorPodsScheduled(kubectl, name, namespace, 30*time.Second); err != nil {
			return DoctorCheck{
				Name:   "MCPServer reconcile smoke",
				OK:     false,
				Detail: fmt.Sprintf("deployment pod was not scheduled: %v", err),
				Remedy: "inspect operator reconcile and smoke deployment events",
			}
		}
	}
	if err := waitForDoctorResource(kubectl, "svc", name, namespace, 150*time.Second); err != nil {
		return DoctorCheck{
			Name:   "MCPServer reconcile smoke",
			OK:     false,
			Detail: fmt.Sprintf("service not created for smoke MCPServer: %v", err),
			Remedy: "inspect operator service reconciliation",
		}
	}
	// Gateway-on servers with adapter certificates use a Traefik IngressRoute
	// instead of a plain Ingress; accept either so smoke tracks the real route.
	routeKind := "ingress"
	if err := waitForDoctorResource(kubectl, "ingress", name, namespace, 150*time.Second); err != nil {
		if routeErr := waitForDoctorResource(kubectl, "ingressroute.traefik.io", name, namespace, 30*time.Second); routeErr != nil {
			return DoctorCheck{
				Name:   "MCPServer reconcile smoke",
				OK:     false,
				Detail: fmt.Sprintf("ingress/IngressRoute not created for smoke MCPServer: ingress=%v ingressroute=%v", err, routeErr),
				Remedy: "inspect operator ingress reconciliation",
			}
		}
		routeKind = "IngressRoute"
	}
	if target.WaitForReady {
		return DoctorCheck{
			Name:   "MCPServer reconcile smoke",
			OK:     true,
			Detail: fmt.Sprintf("temporary MCPServer %s reconciled ready deployment/service/%s using %s", name, routeKind, target.Source),
		}
	}
	return DoctorCheck{
		Name:   "MCPServer reconcile smoke",
		OK:     true,
		Detail: fmt.Sprintf("temporary MCPServer %s reconciled deployment/service/%s using %s; skipped readiness because the fallback image does not expose the MCP port", name, routeKind, target.Source),
	}
}
