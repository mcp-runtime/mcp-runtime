package doctor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/access"
	"mcp-runtime/pkg/metadata"
)

func checkTraefikIngressClass(kubectl core.KubectlRunner) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "ingressclass", "traefik", "-o", "jsonpath={.metadata.name}"})
	if err != nil {
		return DoctorCheck{
			Name:   "traefik ingressClass",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "install or expose Traefik ingress controller",
		}
	}
	out, err := cmd.Output()
	got := strings.TrimSpace(string(out))
	if err != nil || got != "traefik" {
		return DoctorCheck{
			Name:   "traefik ingressClass",
			OK:     false,
			Detail: "ingressClass traefik not found",
			Remedy: "ensure Traefik is installed and ingressClassName is `traefik`",
		}
	}
	return DoctorCheck{
		Name:   "traefik ingressClass",
		OK:     true,
		Detail: "present",
	}
}

func doctorTraefikEndpoints(distro Distribution) []doctorTraefikEndpoint {
	if distro == DistroK3s {
		return []doctorTraefikEndpoint{
			{
				Namespace: doctorK3sTraefikNamespace,
				Name:      doctorTraefikServiceName,
				WebPort:   doctorK3sTraefikWebPort,
				Source:    "k3s bundled Traefik",
			},
			{
				Namespace: doctorTraefikNamespace,
				Name:      doctorTraefikServiceName,
				WebPort:   doctorTraefikWebPort,
				Source:    "repo-managed Traefik",
			},
		}
	}
	return []doctorTraefikEndpoint{
		{
			Namespace: doctorTraefikNamespace,
			Name:      doctorTraefikServiceName,
			WebPort:   doctorTraefikWebPort,
			Source:    "repo-managed Traefik",
		},
	}
}

func (e doctorTraefikEndpoint) label() string {
	return fmt.Sprintf("%s %s/%s", e.Source, e.Namespace, e.Name)
}

func traefikRemedy(distro Distribution) string {
	if distro == DistroK3s {
		return "k3s usually installs Traefik as `kube-system/traefik`; verify it is enabled with `kubectl -n kube-system get deploy,svc traefik`, or install the repo ingress overlay."
	}
	return "install Traefik deployment/service in namespace `traefik`, or run setup with the repo ingress overlay"
}

func checkTraefikDeploymentReady(kubectl core.KubectlRunner, distro Distribution) DoctorCheck {
	failures := make([]string, 0, len(doctorTraefikEndpoints(distro)))
	for _, endpoint := range doctorTraefikEndpoints(distro) {
		check := checkTraefikDeploymentReadyAt(kubectl, endpoint)
		if check.OK {
			return check
		}
		failures = append(failures, fmt.Sprintf("%s: %s", endpoint.label(), check.Detail))
	}
	return DoctorCheck{
		Name:   "traefik deployment readiness",
		OK:     false,
		Detail: strings.Join(failures, "; "),
		Remedy: traefikRemedy(distro),
	}
}

func checkTraefikDeploymentReadyAt(kubectl core.KubectlRunner, endpoint doctorTraefikEndpoint) DoctorCheck {
	pair, ready, err := doctorDeploymentReplicaStatus(kubectl, endpoint.Namespace, endpoint.Name)
	if err != nil {
		return DoctorCheck{
			Name:   "traefik deployment readiness",
			OK:     false,
			Detail: err.Error(),
		}
	}
	if !ready {
		return DoctorCheck{
			Name:   "traefik deployment readiness",
			OK:     false,
			Detail: fmt.Sprintf("%s replicas ready at %s/%s", pair, endpoint.Namespace, endpoint.Name),
		}
	}
	return DoctorCheck{
		Name:   "traefik deployment readiness",
		OK:     true,
		Detail: fmt.Sprintf("%s replicas ready at %s/%s (%s)", pair, endpoint.Namespace, endpoint.Name, endpoint.Source),
	}
}

func checkTraefikWebEntrypoint(kubectl core.KubectlRunner, distro Distribution) DoctorCheck {
	endpoint, ports, ok := resolveDoctorTraefikWebEndpoint(kubectl, distro)
	if ok {
		return DoctorCheck{
			Name:   "traefik web entrypoint",
			OK:     true,
			Detail: fmt.Sprintf("service %s/%s exposes web entrypoint on port %d (%s)", endpoint.Namespace, endpoint.Name, endpoint.WebPort, endpoint.Source),
		}
	}
	return DoctorCheck{
		Name:   "traefik web entrypoint",
		OK:     false,
		Detail: ports,
		Remedy: traefikRemedy(distro),
	}
}

func resolveDoctorTraefikWebEndpoint(kubectl core.KubectlRunner, distro Distribution) (doctorTraefikEndpoint, string, bool) {
	failures := make([]string, 0, len(doctorTraefikEndpoints(distro)))
	for _, endpoint := range doctorTraefikEndpoints(distro) {
		ports, err := readTraefikServicePorts(kubectl, endpoint)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint.label(), err))
			continue
		}
		webPort, ok := findTraefikWebPort(ports)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s ports: %q", endpoint.label(), strings.TrimSpace(ports)))
			continue
		}
		endpoint.WebPort = webPort.Port
		return endpoint, ports, true
	}
	return doctorTraefikEndpoint{}, strings.Join(failures, "; "), false
}

func readTraefikServicePorts(kubectl core.KubectlRunner, endpoint doctorTraefikEndpoint) (string, error) {
	cmd, err := kubectl.CommandArgs([]string{"get", "svc", "-n", endpoint.Namespace, endpoint.Name, "-o", "jsonpath={range .spec.ports[*]}{.name}:{.port}:{.nodePort}{\"\\n\"}{end}"})
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrDoctorKubectlError, err, fmt.Sprintf("kubectl error: %v", err))
	}
	out, err := cmd.Output()
	if err != nil {
		return "", core.NewWithSentinel(core.ErrDoctorTraefikServiceNotFound, fmt.Sprintf("service %s/%s not found", endpoint.Namespace, endpoint.Name))
	}
	return strings.TrimSpace(string(out)), nil
}

func checkTraefikServiceExposure(kubectl core.KubectlRunner, distro Distribution) DoctorCheck {
	failures := make([]string, 0, len(doctorTraefikEndpoints(distro)))
	for _, endpoint := range doctorTraefikEndpoints(distro) {
		check := checkTraefikServiceExposureAt(kubectl, endpoint, distro)
		if check.OK {
			return check
		}
		failures = append(failures, fmt.Sprintf("%s: %s", endpoint.label(), check.Detail))
	}
	return DoctorCheck{
		Name:   "traefik service exposure",
		OK:     false,
		Detail: strings.Join(failures, "; "),
		Remedy: "ensure the active Traefik service has an external LoadBalancer address or NodePort for the web entrypoint",
	}
}

func ingressDistroAllowsPortForwardExposure(distro Distribution) bool {
	switch distro {
	case DistroKind, DistroMinikube, DistroDockerDesktop:
		return true
	default:
		return false
	}
}

func checkTraefikServiceExposureAt(kubectl core.KubectlRunner, endpoint doctorTraefikEndpoint, distro Distribution) DoctorCheck {
	cmd, err := kubectl.CommandArgs([]string{"get", "svc", "-n", endpoint.Namespace, endpoint.Name, "-o", "jsonpath={.spec.type}|{.status.loadBalancer.ingress[0].ip}|{.status.loadBalancer.ingress[0].hostname}|{range .spec.ports[*]}{.name}:{.port}:{.nodePort}{\",\"}{end}"})
	if err != nil {
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
		}
	}
	out, execErr := cmd.Output()
	if execErr != nil {
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     false,
			Detail: fmt.Sprintf("failed reading service exposure fields for %s/%s", endpoint.Namespace, endpoint.Name),
		}
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 4)
	if len(parts) < 4 {
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     false,
			Detail: fmt.Sprintf("unexpected service exposure payload %q", strings.TrimSpace(string(out))),
		}
	}
	svcType := strings.TrimSpace(parts[0])
	lbIP := strings.TrimSpace(parts[1])
	lbHost := strings.TrimSpace(parts[2])
	ports := strings.TrimSpace(parts[3])
	webPort, hasWebPort := findTraefikWebPort(ports)
	if !hasWebPort {
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     false,
			Detail: fmt.Sprintf("service type=%s has no web entrypoint port (ports=%q)", svcType, ports),
		}
	}
	if svcType == "LoadBalancer" && (lbIP != "" || lbHost != "") {
		addr := lbIP
		if addr == "" {
			addr = lbHost
		}
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     true,
			Detail: fmt.Sprintf("%s/%s LoadBalancer ready at %s (%s)", endpoint.Namespace, endpoint.Name, addr, endpoint.Source),
		}
	}
	if webPort.NodePort != "" && webPort.NodePort != "0" {
		return DoctorCheck{
			Name:   "traefik service exposure",
			OK:     true,
			Detail: fmt.Sprintf("%s/%s %s service exposes nodePort %s for web port %d (%s)", endpoint.Namespace, endpoint.Name, svcType, webPort.NodePort, webPort.Port, endpoint.Source),
		}
	}
	if ingressDistroAllowsPortForwardExposure(distro) && svcType == "LoadBalancer" && lbIP == "" && lbHost == "" {
		if readyCheck := checkTraefikDeploymentReadyAt(kubectl, endpoint); readyCheck.OK {
			return DoctorCheck{
				Name:   "traefik service exposure",
				OK:     true,
				Detail: fmt.Sprintf("%s/%s LoadBalancer has no external address on %s; reach Traefik via kubectl port-forward or cluster port mappings (%s)", endpoint.Namespace, endpoint.Name, distro, endpoint.Source),
			}
		}
	}
	return DoctorCheck{
		Name:   "traefik service exposure",
		OK:     false,
		Detail: fmt.Sprintf("service type=%s exposure not ready (lbIP=%q lbHost=%q ports=%q)", svcType, lbIP, lbHost, ports),
	}
}

func checkIngressLoadBalancerStatus(kubectl core.KubectlRunner) DoctorCheck {
	out, err := readKubectlOutput(kubectl, []string{"get", "ingress", "-A", "-o", buildIngressLoadBalancerJSONPath()})
	if err != nil {
		return DoctorCheck{
			Name:   "ingress LoadBalancer status",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check API connectivity and RBAC for listing Ingress resources",
		}
	}
	statuses := parseIngressLoadBalancerStatuses(out)
	if len(statuses) == 0 {
		return DoctorCheck{
			Name:   "ingress LoadBalancer status",
			OK:     true,
			Detail: "no host-based MCP Runtime ingresses found; skipping LoadBalancer status check",
		}
	}
	missing := make([]string, 0)
	ready := make([]string, 0)
	for _, status := range statuses {
		label := fmt.Sprintf("%s/%s host=%s", status.Namespace, status.Name, strings.Join(status.Hosts, ","))
		if status.LBIP == "" && status.LBHost == "" {
			missing = append(missing, label)
			continue
		}
		addr := status.LBIP
		if addr == "" {
			addr = status.LBHost
		}
		ready = append(ready, label+" -> "+addr)
	}
	if len(missing) > 0 {
		if doctorIngressReadinessPermissiveMode() {
			return DoctorCheck{
				Name:   "ingress LoadBalancer status",
				OK:     true,
				Detail: fmt.Sprintf("permissive ingress readiness mode allows %d/%d host-based ingress(es) with empty status.loadBalancer in dev/NodePort clusters: %s", len(missing), len(statuses), strings.Join(missing, "; ")),
			}
		}
		return DoctorCheck{
			Name:   "ingress LoadBalancer status",
			OK:     false,
			Detail: fmt.Sprintf("%d/%d host-based ingress(es) have empty status.loadBalancer: %s", len(missing), len(statuses), strings.Join(missing, "; ")),
			Remedy: "check the active ingress controller service exposure and set MCP_INGRESS_READINESS_MODE=permissive only for dev/NodePort clusters that intentionally do not publish Ingress LoadBalancer status",
		}
	}
	return DoctorCheck{
		Name:   "ingress LoadBalancer status",
		OK:     true,
		Detail: strings.Join(ready, "; "),
	}
}

func doctorIngressReadinessPermissiveMode() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(doctorEnvIngressReadinessMode)), doctorIngressReadinessPermissive)
}

func buildIngressLoadBalancerJSONPath() string {
	return `jsonpath={range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{range .spec.rules[*]}{.host}{","}{end}{"|"}{.status.loadBalancer.ingress[0].ip}{"|"}{.status.loadBalancer.ingress[0].hostname}{"\n"}{end}`
}

func parseIngressLoadBalancerStatuses(value string) []doctorIngressStatus {
	var statuses []doctorIngressStatus
	for _, line := range filterNonEmptyLines(value) {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) != 5 {
			continue
		}
		status := doctorIngressStatus{
			Namespace: strings.TrimSpace(parts[0]),
			Name:      strings.TrimSpace(parts[1]),
			Hosts:     splitCommaTrim(parts[2]),
			LBIP:      strings.TrimSpace(parts[3]),
			LBHost:    strings.TrimSpace(parts[4]),
		}
		if status.Namespace == "" || status.Name == "" || len(status.Hosts) == 0 {
			continue
		}
		if !isMCPRuntimeIngress(status) {
			continue
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func isMCPRuntimeIngress(status doctorIngressStatus) bool {
	if status.Namespace == "registry" || status.Namespace == componentNamespace("platform-api") || status.Namespace == doctorMCPServersNamespace {
		return true
	}
	if strings.HasPrefix(status.Namespace, "mcp-team-") || strings.HasPrefix(status.Namespace, "mcp-servers-") {
		return true
	}
	for _, host := range status.Hosts {
		switch {
		case strings.HasPrefix(host, "registry."), strings.HasPrefix(host, "platform."), strings.HasPrefix(host, "mcp."):
			return true
		}
	}
	return false
}

func doctorConfiguredIngressHosts() (platform, registry, mcp string, configured bool) {
	platform = strings.TrimSpace(metadata.ResolvePlatformIngressHost())
	registry = strings.TrimSpace(metadata.ResolveRegistryHost())
	mcp = strings.TrimSpace(metadata.ResolveMcpIngressHost())
	configured = doctorPublicIngressHostConfigExplicitlySet()
	if !configured && registry == metadata.DefaultRegistryHost {
		registry = ""
	}
	return platform, registry, mcp, configured
}

func doctorPublicIngressHostConfigExplicitlySet() bool {
	for _, key := range []string{
		"MCP_PLATFORM_DOMAIN",
		"MCP_PLATFORM_INGRESS_HOST",
		"MCP_REGISTRY_INGRESS_HOST",
		"MCP_MCP_INGRESS_HOST",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func doctorTLSPreflightRequested() bool {
	if strings.TrimSpace(os.Getenv(doctorEnvACMEEmail)) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv(doctorEnvTLSClusterIssuer)) != "" {
		return true
	}
	return false
}

func checkPublicIngressHostConfig() DoctorCheck {
	platform, registry, mcp, configured := doctorConfiguredIngressHosts()
	if !configured {
		return DoctorCheck{
			Name:   "public ingress host config",
			OK:     true,
			Detail: "no public ingress host env configured; skipping host-specific preflight",
		}
	}

	missing := make([]string, 0, 3)
	if platform == "" {
		missing = append(missing, "platform")
	}
	if registry == "" {
		missing = append(missing, "registry")
	}
	if mcp == "" {
		missing = append(missing, "mcp")
	}
	if len(missing) > 0 {
		return DoctorCheck{
			Name:   "public ingress host config",
			OK:     false,
			Detail: fmt.Sprintf("missing %s public host value(s)", strings.Join(missing, ", ")),
			Remedy: "set MCP_PLATFORM_DOMAIN or set MCP_PLATFORM_INGRESS_HOST, MCP_REGISTRY_INGRESS_HOST, and MCP_MCP_INGRESS_HOST together",
		}
	}

	for _, item := range []struct {
		field string
		host  string
	}{
		{field: "platform", host: platform},
		{field: "registry", host: registry},
		{field: "mcp", host: mcp},
	} {
		if err := access.ValidateResourceName(item.field, item.host); err != nil {
			return DoctorCheck{
				Name:   "public ingress host config",
				OK:     false,
				Detail: err.Error(),
				Remedy: "use lowercase DNS hostnames such as platform.example.com",
			}
		}
	}
	if platform == registry || platform == mcp || registry == mcp {
		return DoctorCheck{
			Name:   "public ingress host config",
			OK:     false,
			Detail: fmt.Sprintf("platform=%q registry=%q mcp=%q must be distinct hostnames", platform, registry, mcp),
			Remedy: "configure separate public hostnames for platform, registry, and MCP ingress",
		}
	}
	return DoctorCheck{
		Name:   "public ingress host config",
		OK:     true,
		Detail: fmt.Sprintf("platform=%s registry=%s mcp=%s", platform, registry, mcp),
	}
}

func checkPublicIngressDNS() DoctorCheck {
	platform, registry, mcp, configured := doctorConfiguredIngressHosts()
	if !configured {
		return DoctorCheck{
			Name:   "public ingress DNS",
			OK:     true,
			Detail: "no public ingress host env configured; skipping DNS resolution",
		}
	}

	hosts := []string{platform, registry, mcp}
	seen := map[string]struct{}{}
	results := make([]string, 0, len(hosts))
	failures := make([]string, 0)
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		ctx, cancel := context.WithTimeout(context.Background(), doctorDNSLookupTimeout)
		addrs, err := doctorLookupHost(ctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			failures = append(failures, fmt.Sprintf("%s: %v", host, err))
			continue
		}
		results = append(results, fmt.Sprintf("%s -> %s", host, strings.Join(addrs, ",")))
	}
	if len(failures) > 0 {
		return DoctorCheck{
			Name:   "public ingress DNS",
			OK:     false,
			Detail: strings.Join(failures, "; "),
			Remedy: "create public A, AAAA, or CNAME records for platform, registry, and mcp before running TLS setup",
		}
	}
	return DoctorCheck{
		Name:   "public ingress DNS",
		OK:     true,
		Detail: strings.Join(results, "; "),
	}
}

func checkCertManagerReadiness(kubectl core.KubectlRunner) DoctorCheck {
	if !doctorTLSPreflightRequested() {
		return DoctorCheck{
			Name:   "cert-manager readiness",
			OK:     true,
			Detail: "TLS preflight not requested; skipping cert-manager readiness",
		}
	}

	components := []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"}
	ready := make([]string, 0, len(components))
	failures := make([]string, 0)
	for _, name := range components {
		pair, readyNow, err := doctorDeploymentReplicaStatus(kubectl, "cert-manager", name)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if !readyNow {
			failures = append(failures, fmt.Sprintf("%s: %s ready", name, pair))
			continue
		}
		ready = append(ready, fmt.Sprintf("%s=%s", name, pair))
	}
	if len(failures) > 0 {
		return DoctorCheck{
			Name:   "cert-manager readiness",
			OK:     false,
			Detail: strings.Join(failures, "; "),
			Remedy: "install cert-manager first or let setup install it by not using --skip-cert-manager-install",
		}
	}
	return DoctorCheck{
		Name:   "cert-manager readiness",
		OK:     true,
		Detail: strings.Join(ready, "; "),
	}
}

func doctorDeploymentReplicaStatus(kubectl core.KubectlRunner, namespace, name string) (string, bool, error) {
	cmd, err := kubectl.CommandArgs([]string{"get", "deploy", "-n", namespace, name, "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"})
	if err != nil {
		return "", false, core.WrapWithSentinel(core.ErrDoctorKubectlError, err, fmt.Sprintf("kubectl error: %v", err))
	}
	out, execErr := cmd.Output()
	pair := strings.TrimSpace(string(out))
	if execErr != nil || pair == "" {
		return "", false, core.NewWithSentinel(core.ErrDoctorDeploymentNotFound, "deployment not found")
	}
	parts := strings.SplitN(pair, "/", 2)
	if len(parts) != 2 {
		return pair, false, core.NewWithSentinel(core.ErrDoctorUnexpectedReplicaStatus, fmt.Sprintf("unexpected replica status %q", pair))
	}
	readyReplicas, readyErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	desiredReplicas, desiredErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if readyErr != nil || desiredErr != nil {
		return pair, false, core.NewWithSentinel(core.ErrDoctorUnexpectedReplicaStatus, fmt.Sprintf("unexpected replica status %q", pair))
	}
	if desiredReplicas == 0 || readyReplicas < desiredReplicas {
		return pair, false, nil
	}
	return pair, true, nil
}

func checkDoctorTLSClusterIssuer(kubectl core.KubectlRunner) DoctorCheck {
	name := strings.TrimSpace(os.Getenv(doctorEnvTLSClusterIssuer))
	if name == "" {
		return DoctorCheck{
			Name:   "TLS ClusterIssuer",
			OK:     true,
			Detail: "MCP_TLS_CLUSTER_ISSUER not set; skipping issuer lookup",
		}
	}
	cmd, err := kubectl.CommandArgs([]string{"get", "clusterissuer", name, "-o", "jsonpath={.metadata.name}"})
	if err != nil {
		return DoctorCheck{
			Name:   "TLS ClusterIssuer",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "install the ClusterIssuer first or fix MCP_TLS_CLUSTER_ISSUER",
		}
	}
	out, execErr := cmd.Output()
	if execErr != nil || strings.TrimSpace(string(out)) != name {
		return DoctorCheck{
			Name:   "TLS ClusterIssuer",
			OK:     false,
			Detail: fmt.Sprintf("ClusterIssuer %q not found", name),
			Remedy: "install the ClusterIssuer first or fix MCP_TLS_CLUSTER_ISSUER",
		}
	}
	return DoctorCheck{
		Name:   "TLS ClusterIssuer",
		OK:     true,
		Detail: fmt.Sprintf("ClusterIssuer %s found", name),
	}
}

func checkDoctorACMEHTTP01Exposure(kubectl core.KubectlRunner, distro Distribution) DoctorCheck {
	email := strings.TrimSpace(os.Getenv(doctorEnvACMEEmail))
	if email == "" {
		return DoctorCheck{
			Name:   "ACME HTTP-01 exposure",
			OK:     true,
			Detail: "MCP_ACME_EMAIL not set; skipping ACME HTTP-01 preflight",
		}
	}
	endpoint, _, ok := resolveDoctorTraefikWebEndpoint(kubectl, distro)
	if !ok {
		return DoctorCheck{
			Name:   "ACME HTTP-01 exposure",
			OK:     false,
			Detail: "active Traefik web entrypoint not found",
			Remedy: "expose Traefik on public port 80 before requesting Let's Encrypt certificates",
		}
	}
	if endpoint.WebPort != 80 {
		return DoctorCheck{
			Name:   "ACME HTTP-01 exposure",
			OK:     false,
			Detail: fmt.Sprintf("%s web entrypoint listens on service port %d", endpoint.label(), endpoint.WebPort),
			Remedy: "Let's Encrypt HTTP-01 must reach Traefik on public port 80",
		}
	}
	exposure := checkTraefikServiceExposureAt(kubectl, endpoint, distro)
	if !exposure.OK {
		return DoctorCheck{
			Name:   "ACME HTTP-01 exposure",
			OK:     false,
			Detail: exposure.Detail,
			Remedy: "ensure Traefik port 80 is reachable through a LoadBalancer or NodePort before requesting ACME certificates",
		}
	}
	return DoctorCheck{
		Name:   "ACME HTTP-01 exposure",
		OK:     true,
		Detail: fmt.Sprintf("%s with MCP_ACME_EMAIL=%s", exposure.Detail, email),
	}
}

// registryDeniedByPolicy reports whether curl resolved the registry host but
// could not connect (timeout or refusal), which is how a NetworkPolicy deny
// appears. A resolution failure is never treated as a deny.
func registryDeniedByPolicy(logs string) bool {
	if strings.Contains(logs, "Could not resolve host") || strings.Contains(logs, "curl: (6)") {
		return false
	}
	return strings.Contains(logs, "curl: (28)") || strings.Contains(logs, "curl: (7)") ||
		strings.Contains(logs, "Connection timed out") || strings.Contains(logs, "Connection refused")
}

func checkMCPServersDNSAndNetwork(kubectl core.KubectlRunner) DoctorCheck {
	podName := fmt.Sprintf("mcp-runtime-doctor-dns-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	registryURL := doctorRegistryServiceURL(kubectl)
	// -S keeps curl's error text so a policy-blocked connection can be told
	// apart from a DNS failure.
	curlArgs := []string{
		"-skIS", "--connect-timeout", "5", "--max-time", "15",
		registryURL,
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", doctorMCPServersNamespace, "--ignore-not-found"})
	}()
	args := []string{
		"run", podName,
		"-n", doctorMCPServersNamespace,
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", curlArgs...),
	}
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return DoctorCheck{
			Name:   "mcp-servers DNS/network",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check kubeconfig and namespace access",
		}
	}
	createOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   "mcp-servers DNS/network",
			OK:     false,
			Detail: strings.TrimSpace(string(createOut)),
			Remedy: "check CoreDNS and network policies for namespace mcp-servers",
		}
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, doctorMCPServersNamespace, 90*time.Second); err != nil {
		logs, _ := readKubectlOutput(kubectl, []string{"logs", podName, "-n", doctorMCPServersNamespace, "--tail=50"})
		if registryDeniedByPolicy(logs) {
			// Tenant namespaces are intentionally denied registry access; image
			// pulls go through the kubelet, so DNS resolving is what must work.
			return DoctorCheck{
				Name:   "mcp-servers DNS/network",
				OK:     true,
				Detail: "registry Service name resolves from mcp-servers; direct pod access is blocked by NetworkPolicy (expected)",
			}
		}
		detail := fmt.Sprintf("helper pod did not succeed: %v", err)
		if strings.TrimSpace(logs) != "" {
			detail += ": " + strings.TrimSpace(logs)
		}
		return DoctorCheck{
			Name:   "mcp-servers DNS/network",
			OK:     false,
			Detail: detail,
			Remedy: "check CoreDNS and network policies for namespace mcp-servers",
		}
	}
	out, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", doctorMCPServersNamespace})
	if logsErr != nil {
		return DoctorCheck{
			Name:   "mcp-servers DNS/network",
			OK:     false,
			Detail: fmt.Sprintf("failed reading helper pod logs: %v", logsErr),
			Remedy: "inspect pod events: `kubectl -n mcp-servers describe pod " + podName + "`",
		}
	}
	if !hasHTTP200Status(out) {
		return DoctorCheck{
			Name:   "mcp-servers DNS/network",
			OK:     false,
			Detail: fmt.Sprintf("unexpected response: %q", strings.TrimSpace(out)),
			Remedy: "check CoreDNS and service routing from namespace mcp-servers",
		}
	}
	return DoctorCheck{
		Name:   "mcp-servers DNS/network",
		OK:     true,
		Detail: fmt.Sprintf("can resolve and reach registry service from mcp-servers namespace via %s", doctorRegistryServiceScheme(registryURL)),
	}
}

func checkIngressRouteProbe(kubectl core.KubectlRunner, namespace string, distro Distribution) DoctorCheck {
	route, err := resolveIngressRouteProbeTarget(kubectl, namespace)
	if err != nil {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     true,
			Detail: "no ingress resources found in mcp-servers; skipping live route probe",
		}
	}
	if route.Name == "" {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     true,
			Detail: "no ingress resources found in mcp-servers; skipping live route probe",
		}
	}
	host := strings.TrimSpace(route.Host)
	path := doctorNormalizePath(strings.TrimSpace(route.Path))
	if path == "" {
		path = "/"
	}
	traefik, traefikDetail, ok := resolveDoctorTraefikWebEndpoint(kubectl, distro)
	if !ok {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     false,
			Detail: fmt.Sprintf("failed resolving active Traefik service for route probe: %s", traefikDetail),
			Remedy: traefikRemedy(distro),
		}
	}
	podName := fmt.Sprintf("mcp-runtime-doctor-ingress-%d", time.Now().UnixNano())
	image := "curlimages/curl:8.7.1"
	probeArgs := []string{
		"-sS", "-o", "/tmp/doctor-response",
		"-w", "%{http_code}",
		"--connect-timeout", "5",
		"--max-time", "20",
		"-H", "content-type: application/json",
		"-H", "accept: application/json, text/event-stream",
		"-H", "Mcp-Protocol-Version: 2025-06-18",
	}
	if host != "" {
		probeArgs = append(probeArgs, "-H", "Host: "+host)
	}
	probeURL := fmt.Sprintf("http://%s:%d%s", doctorServiceDNS(traefik.Name, traefik.Namespace), traefik.WebPort, path)
	if route.TLS {
		securePort := 0
		for _, port := range parseDoctorServicePorts(traefikDetail) {
			if port.Name == "websecure" || port.Name == "https" {
				securePort = port.Port
				break
			}
		}
		if host == "" || securePort == 0 {
			return DoctorCheck{Name: "ingress route probe", OK: false, Detail: "TLS ingress requires a public host and a Traefik websecure/https service port", Remedy: "inspect ingress host and Traefik secure service ports"}
		}
		// Connect inside the cluster while preserving public TLS SNI and
		// certificate verification. A Host header alone cannot select TLS SNI.
		probeArgs = append(probeArgs, "--connect-to", fmt.Sprintf("%s:443:%s:%d", host, doctorServiceDNS(traefik.Name, traefik.Namespace), securePort))
		probeURL = "https://" + host + path
	}
	probeArgs = append(probeArgs,
		"-d", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		probeURL,
	)
	curlArgs := []string{
		"run", podName, "-n", namespace,
		"--restart=Never",
		"--image=" + image,
		"--overrides=" + restrictedRunOverrides(podName, image, "curl", probeArgs...),
	}
	defer func() {
		_ = kubectl.Run([]string{"delete", "pod", podName, "-n", namespace, "--ignore-not-found"})
	}()
	cmd, err := kubectl.CommandArgs(curlArgs)
	if err != nil {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     false,
			Detail: fmt.Sprintf("kubectl error: %v", err),
			Remedy: "check kubectl connectivity and helper pod image access",
		}
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     false,
			Detail: fmt.Sprintf("failed creating probe pod: %v: %s", runErr, strings.TrimSpace(string(out))),
			Remedy: "inspect Traefik logs and ingress rules",
		}
	}
	if err := waitForDoctorPodSucceeded(kubectl, podName, namespace, 90*time.Second); err != nil {
		logs, _ := readKubectlOutput(kubectl, []string{"logs", podName, "-n", namespace, "--tail=50"})
		return DoctorCheck{Name: "ingress route probe", OK: false, Detail: fmt.Sprintf("probe pod did not complete: %v: %s", err, strings.TrimSpace(logs)), Remedy: "inspect Traefik service, NetworkPolicies, and helper pod events"}
	}
	logs, logsErr := readKubectlOutput(kubectl, []string{"logs", podName, "-n", namespace})
	if logsErr != nil {
		return DoctorCheck{Name: "ingress route probe", OK: false, Detail: fmt.Sprintf("failed reading probe logs: %v", logsErr), Remedy: "check helper pod log access"}
	}
	status := strings.TrimSpace(logs)
	if status == "" {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     false,
			Detail: "probe returned empty HTTP status",
			Remedy: "inspect Traefik service and ingress path rules",
		}
	}
	if status == "404" {
		return DoctorCheck{
			Name:   "ingress route probe",
			OK:     false,
			Detail: fmt.Sprintf("ingress %s returned HTTP 404 for path %s", route.Name, path),
			Remedy: "confirm MCPServer ingress path/host matches the public route",
		}
	}
	return DoctorCheck{
		Name:   "ingress route probe",
		OK:     true,
		Detail: fmt.Sprintf("ingress %s returned HTTP %s for %s via %s/%s", route.Name, status, path, traefik.Namespace, traefik.Name),
	}
}

func resolveIngressRouteProbeTarget(kubectl core.KubectlRunner, namespace string) (doctorIngressRoute, error) {
	out, err := readKubectlOutput(kubectl, []string{"get", "ingress", "-n", namespace, "-o", "jsonpath={range .items[*]}{.metadata.name}|{.spec.rules[0].host}|{.spec.rules[0].http.paths[0].path}|{.metadata.annotations.traefik\\.ingress\\.kubernetes\\.io/router\\.tls}|{.spec.tls[0].secretName}{\"\\n\"}{end}"})
	if err != nil {
		return doctorIngressRoute{}, err
	}
	for _, line := range filterNonEmptyLines(out) {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) == 0 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" || strings.HasPrefix(name, "doctor-smoke-") {
			continue
		}
		route := doctorIngressRoute{Name: name}
		if len(parts) > 1 {
			route.Host = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			route.Path = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			route.TLS = strings.TrimSpace(parts[3]) == "true"
		}
		if len(parts) > 4 && strings.TrimSpace(parts[4]) != "" {
			route.TLS = true
		}
		return route, nil
	}
	return doctorIngressRoute{}, nil
}
