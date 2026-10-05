package doctor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/platforminventory"
)

func waitForDoctorResource(kubectl core.KubectlRunner, resource, name, namespace string, timeout time.Duration) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		if _, err := readKubectlOutput(kubectl, []string{"get", resource, name, "-n", namespace, "-o", "jsonpath={.metadata.name}"}); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-timeoutTimer.C:
			if lastErr != nil {
				return lastErr
			}
			return core.NewWithSentinel(core.ErrDoctorResourceNotFoundBeforeTimeout, fmt.Sprintf("%s/%s not found before timeout", resource, name))
		case <-ticker.C:
		}
	}
}

func waitForDoctorDeploymentReady(kubectl core.KubectlRunner, name, namespace string, timeout time.Duration) error {
	cmd, err := kubectl.CommandArgs([]string{"rollout", "status", "deployment/" + name, "-n", namespace, "--timeout=" + timeout.String()})
	if err != nil {
		return err
	}
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return runErr
	}
	return core.WrapWithSentinel(core.ErrDoctorDeploymentRolloutFailed, runErr, fmt.Sprintf("%v: %s", runErr, detail))
}

func waitForDoctorPodsScheduled(kubectl core.KubectlRunner, name, namespace string, timeout time.Duration) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		out, err := readKubectlOutput(kubectl, []string{"get", "pods", "-n", namespace, "-l", "app=" + name, "-o", "jsonpath={.items[0].spec.nodeName}"})
		if err == nil && strings.TrimSpace(out) != "" {
			return nil
		}
		select {
		case <-timeoutTimer.C:
			return core.NewWithSentinel(core.ErrDoctorPodsNotScheduledBeforeTimeout, fmt.Sprintf("no scheduled pod found for deployment %s before timeout", name))
		case <-ticker.C:
		}
	}
}

func hasHTTP200Status(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "HTTP/") {
			continue
		}
		fields := strings.Fields(line)
		return len(fields) >= 2 && fields[1] == "200"
	}
	return false
}

func readKubectlOutput(kubectl core.KubectlRunner, args []string) (string, error) {
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return "", err
	}
	return runKubectlOutput(cmd)
}

// runKubectlOutput returns kubectl's diagnostic text with its process error,
// so callers do not reduce useful API/RBAC errors to the generic "exit status 1".
func runKubectlOutput(cmd core.Command) (string, error) {
	out, err := runKubectlBytes(cmd)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func runKubectlBytes(cmd core.Command) ([]byte, error) {
	out, execErr := cmd.Output()
	if execErr != nil {
		diagnostic := out
		var exitErr *exec.ExitError
		if errors.As(execErr, &exitErr) && len(exitErr.Stderr) > 0 {
			diagnostic = exitErr.Stderr
		}
		detail := kubeerr.CommandDetail(string(diagnostic), execErr)
		return nil, kubectlOutputError{detail: detail, cause: execErr}
	}
	return out, nil
}

type kubectlOutputError struct {
	detail string
	cause  error
}

func (e kubectlOutputError) Error() string { return e.detail }
func (e kubectlOutputError) Unwrap() error { return e.cause }

func kubectlResultDetail(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func decodeBase64(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrDoctorDecodeBase64Failed, err, fmt.Sprintf("decode base64 value: %v", err))
	}
	return string(decoded), nil
}

func splitCommaTrim(value string) []string {
	return splitSepTrim(value, ",")
}

func splitSepTrim(value, sep string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(values []string, fallback string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

// doctorClusterDomain returns the Kubernetes cluster DNS domain used by
// in-cluster service probes. Kubernetes defaults this to cluster.local, but
// distributions and operators can be installed with a different domain.
func doctorClusterDomain() string {
	domain := strings.TrimSpace(os.Getenv("MCP_CLUSTER_DOMAIN"))
	if domain == "" {
		return "cluster.local"
	}
	return strings.Trim(strings.TrimSuffix(domain, "."), ".")
}

func doctorServiceDNS(service, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, doctorClusterDomain())
}

func filterNonEmptyLines(value string) []string {
	raw := strings.Split(value, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func parseDoctorServicePorts(value string) []doctorServicePort {
	entries := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ','
	})
	ports := make([]doctorServicePort, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		switch len(parts) {
		case 2:
			port, err := strconv.Atoi(strings.TrimSpace(parts[0]))
			if err != nil {
				continue
			}
			ports = append(ports, doctorServicePort{
				Port:     port,
				NodePort: strings.TrimSpace(parts[1]),
			})
		case 3:
			port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				continue
			}
			ports = append(ports, doctorServicePort{
				Name:     strings.TrimSpace(parts[0]),
				Port:     port,
				NodePort: strings.TrimSpace(parts[2]),
			})
		}
	}
	return ports
}

func findTraefikWebPort(value string) (doctorServicePort, bool) {
	ports := parseDoctorServicePorts(value)
	for _, port := range ports {
		if port.Name == "web" && port.Port > 0 {
			return port, true
		}
	}
	for _, port := range ports {
		if port.Port == doctorTraefikWebPort || port.Port == doctorK3sTraefikWebPort {
			return port, true
		}
	}
	return doctorServicePort{}, false
}

func doctorNormalizePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "/" + trimmed
	}
	return trimmed
}

func resolveDoctorSmokeImage(kubectl core.KubectlRunner, preferredNamespace string) (string, string) {
	target := resolveDoctorSmokeTarget(kubectl, preferredNamespace)
	return target.Image, target.Source
}

func resolveDoctorSmokeTarget(kubectl core.KubectlRunner, preferredNamespace string) doctorSmokeTarget {
	if image := strings.TrimSpace(os.Getenv("MCP_DOCTOR_SMOKE_IMAGE")); image != "" {
		return doctorSmokeTarget{Image: image, Port: mcpdefaults.MCPServerPort, Source: "MCP_DOCTOR_SMOKE_IMAGE", WaitForReady: false}
	}
	configName := platforminventory.SharedConfigName
	if image, err := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "jsonpath={.data.MCP_DOCTOR_SMOKE_IMAGE}"}); err == nil && strings.TrimSpace(image) != "" {
		return doctorSmokeTarget{Image: strings.TrimSpace(image), Port: mcpdefaults.MCPServerPort, Source: configName + "/MCP_DOCTOR_SMOKE_IMAGE", WaitForReady: true}
	}
	mcpServerNames, haveMCPServerNames := readDoctorMCPServerNames(kubectl, preferredNamespace)
	out, err := readKubectlOutput(kubectl, []string{"get", "deploy", "-n", preferredNamespace, "-o", "jsonpath={range .items[*]}{.metadata.name}|{.status.readyReplicas}|{.spec.template.spec.containers[0].image}|{.spec.template.spec.containers[0].ports[0].containerPort}{\"\\n\"}{end}"})
	if err == nil {
		for _, line := range filterNonEmptyLines(out) {
			parts := strings.SplitN(line, "|", 4)
			if len(parts) < 3 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			ready := strings.TrimSpace(parts[1])
			image := strings.TrimSpace(parts[2])
			if name == "" || strings.HasPrefix(name, "doctor-smoke-") || ready == "" || ready == "0" || image == "" {
				continue
			}
			if haveMCPServerNames && !mcpServerNames[name] {
				continue
			}
			port := int32(mcpdefaults.MCPServerPort)
			if len(parts) == 4 {
				if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 32); parseErr == nil && parsed > 0 && parsed <= 65535 {
					port = int32(parsed)
				}
			}
			return doctorSmokeTarget{
				Image:        image,
				Port:         port,
				Source:       fmt.Sprintf("ready deployment %s/%s", preferredNamespace, name),
				WaitForReady: true,
			}
		}
	}
	return doctorSmokeTarget{
		Image:        "registry.k8s.io/pause:3.9",
		Port:         mcpdefaults.MCPServerPort,
		Source:       "fallback image registry.k8s.io/pause:3.9",
		WaitForReady: false,
	}
}

func readDoctorMCPServerNames(kubectl core.KubectlRunner, namespace string) (map[string]bool, bool) {
	out, err := readKubectlOutput(kubectl, []string{"get", "mcpservers", "-n", namespace, "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}"})
	if err != nil {
		return nil, false
	}
	names := map[string]bool{}
	for _, line := range filterNonEmptyLines(out) {
		name := strings.TrimSpace(line)
		if name != "" {
			names[name] = true
		}
	}
	return names, true
}

// PrintDoctorReport emits a human-readable report using the standard printer.
func PrintDoctorReport(r DoctorReport) {
	core.Section("Cluster Doctor")
	core.Info(fmt.Sprintf("Distribution: %s", r.Distribution))
	for _, c := range r.Checks {
		printDoctorCheckResult(c)
	}
	printDoctorReportFooter(r)
}

func printDoctorCheckProgress(event DoctorCheckProgressEvent) func(DoctorCheck) {
	core.Info(doctorCheckProgressMessage(event))
	return func(c DoctorCheck) {
		printDoctorCheckResult(c)
	}
}

func doctorCheckProgressMessage(event DoctorCheckProgressEvent) string {
	prefix := "Checking"
	if event.Total > 0 {
		prefix = fmt.Sprintf("Checking %d/%d", event.Index, event.Total)
	}
	if event.Detail == "" {
		return fmt.Sprintf("%s %s", prefix, event.Name)
	}
	return fmt.Sprintf("%s %s — %s", prefix, event.Name, event.Detail)
}

func printDoctorCheckResult(c DoctorCheck) {
	if c.OK {
		core.Success(doctorCheckMessage(c))
		return
	}
	core.Error(doctorCheckMessage(c))
	if c.Remedy != "" {
		core.Info("  Remedy: " + c.Remedy)
	}
}

func doctorCheckMessage(c DoctorCheck) string {
	return fmt.Sprintf("%s — %s", c.Name, c.Detail)
}

func printDoctorReportFooter(r DoctorReport) {
	if !r.AllOK() {
		core.Info("")
		core.Info("Full remediation steps per distribution are in docs/cluster-readiness.md.")
		if reportHasRegistryOrPullFailure(r) {
			core.Info(remediationHint(r.Distribution))
		}
	}
}

func reportHasRegistryOrPullFailure(r DoctorReport) bool {
	for _, check := range r.Checks {
		if check.OK {
			continue
		}
		switch check.Name {
		case "registry Service",
			"registry reachability (in-cluster)",
			"MCPServer registry image refs",
			"MCPServer imagePullSecrets",
			"mcp-servers imagePullSecrets",
			"mcp-servers image pull smoke",
			"registry HTTP pull mismatch",
			"registry image pull diagnostics":
			return true
		}
	}
	return false
}

func remediationHint(d Distribution) string {
	switch d {
	case DistroK3s:
		return "k3s: write /etc/rancher/k3s/registries.yaml mapping registry.local -> http://127.0.0.1:<NodePort>, add 127.0.0.1 registry.local to /etc/hosts, then `systemctl restart k3s`."
	case DistroKind:
		return "kind: recreate the cluster with containerdConfigPatches for the mirror and extraPortMappings for the NodePort, or use `kind load docker-image`."
	case DistroMinikube:
		return "minikube: start with `--insecure-registry=registry.local`, enable the ingress addon, and map registry.local in /etc/hosts to $(minikube ip)."
	case DistroDockerDesktop:
		return "Docker Desktop: add \"insecure-registries\": [\"registry.local\"] in Docker Engine settings and add 127.0.0.1 registry.local to /etc/hosts."
	default:
		return "generic k8s: edit /etc/containerd/config.toml on each node to add a mirror for registry.local -> http://<reachable>:<NodePort>, map /etc/hosts, and `systemctl restart containerd`."
	}
}
