package doctor

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/mcpdefaults"
)

// Distribution identifies a Kubernetes flavor for remediation messaging.
type Distribution string

const (
	DistroK3s           Distribution = "k3s"
	DistroKind          Distribution = "kind"
	DistroMinikube      Distribution = "minikube"
	DistroDockerDesktop Distribution = "docker-desktop"
	DistroGeneric       Distribution = "generic"
)

// DoctorCheck is a single preflight check result.
type DoctorCheck struct {
	Name   string
	OK     bool
	Detail string
	Remedy string // Short hint; detailed steps come from the distro checklist.
}

// DoctorReport aggregates the full preflight result.
type DoctorReport struct {
	Distribution Distribution
	Checks       []DoctorCheck
}

// DoctorCheckProgress is called before each doctor check starts. It returns an
// optional completion callback that receives the finished check result.
type DoctorCheckProgress func(DoctorCheckProgressEvent) func(DoctorCheck)

// DoctorCheckProgressEvent describes the check that is about to run.
type DoctorCheckProgressEvent struct {
	Name   string
	Detail string
	Index  int
	Total  int
}

type doctorCheckSpec struct {
	Name   string
	Detail string
	Run    func() DoctorCheck
}

const (
	doctorMCPServersNamespace = mcpdefaults.MCPServersNamespace
	doctorTraefikNamespace    = "traefik"
	doctorK3sTraefikNamespace = "kube-system"
	doctorTraefikServiceName  = "traefik"
	doctorTraefikWebPort      = 8000
	doctorK3sTraefikWebPort   = 80
	doctorSentinelNamespace   = "mcp-sentinel"
	doctorPlatformAPIService  = "mcp-platform-api"
	doctorPlatformAPIPort     = 8080
	doctorRuntimeAPIService   = "mcp-runtime-api"
	doctorRuntimeAPIPort      = 8084
	doctorAnalyticsAPIService = "mcp-analytics-api"
	doctorAnalyticsAPIPort    = 8085
	doctorRestrictedRunAsUser = int64(65532)
	doctorProbePodRunTimeout  = "90s"

	registryHTTPPullMismatch = "http: server gave HTTP response to HTTPS client"

	// imagePullListSep separates list items emitted by the image-pull jsonpath.
	// ASCII Unit Separator (0x1f) avoids collisions with commas that appear
	// inside kubelet error messages.
	imagePullListSep = "\x1f"

	// imagePullDescribeLimit caps how many `kubectl describe pod` fallbacks
	// the HTTP-mismatch check issues per run. Most clusters that hit this
	// failure surface it via the waiting message first pass; the describe
	// pass is just a fallback for stale events.
	imagePullDescribeLimit = 8

	doctorEnvACMEEmail               = "MCP_ACME_EMAIL"
	doctorEnvTLSClusterIssuer        = "MCP_TLS_CLUSTER_ISSUER"
	doctorEnvIngressReadinessMode    = "MCP_INGRESS_READINESS_MODE"
	doctorIngressReadinessPermissive = "permissive"
	doctorDNSLookupTimeout           = 10 * time.Second
)

var doctorLookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

type doctorTraefikEndpoint struct {
	Namespace string
	Name      string
	WebPort   int
	Source    string
}

type doctorServicePort struct {
	Name     string
	Port     int
	NodePort string
}

type doctorSmokeTarget struct {
	Image        string
	Port         int32
	Source       string
	WaitForReady bool
}

type doctorIngressRoute struct {
	Name string
	Host string
	Path string
	TLS  bool
}

// AllOK reports whether every check passed.
func (r DoctorReport) AllOK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// RunDoctor executes cluster diagnostics and returns a report.
func RunDoctor(kubectl core.KubectlRunner) DoctorReport {
	distro := DetectDistribution(kubectl)
	return runDoctorChecks(kubectl, distro, nil)
}

// RunDoctorWithProgress executes cluster diagnostics and calls progress hooks
// before and after each check. It is useful for UIs that need live feedback.
func RunDoctorWithProgress(kubectl core.KubectlRunner, progress DoctorCheckProgress) DoctorReport {
	distro := DetectDistribution(kubectl)
	return runDoctorChecks(kubectl, distro, progress)
}

// RunSetupDoctor executes pre-setup readiness checks and returns a report.
func RunSetupDoctor(kubectl core.KubectlRunner) DoctorReport {
	distro := DetectDistribution(kubectl)
	return runDoctorChecksWithSpecs(kubectl, distro, nil, doctorSetupCheckSpecs(kubectl, distro))
}

// RunSetupDoctorWithProgress executes pre-setup readiness checks and calls
// progress hooks before and after each check.
func RunSetupDoctorWithProgress(kubectl core.KubectlRunner, progress DoctorCheckProgress) DoctorReport {
	distro := DetectDistribution(kubectl)
	return runDoctorChecksWithSpecs(kubectl, distro, progress, doctorSetupCheckSpecs(kubectl, distro))
}

// RunDoctorAndPrint streams doctor progress and results as checks execute.
func RunDoctorAndPrint(kubectl core.KubectlRunner) DoctorReport {
	core.Section("Cluster Doctor")
	core.Info("Detecting Kubernetes distribution — reading node kubelet versions, node names, and current context")
	distro := DetectDistribution(kubectl)
	core.Info(fmt.Sprintf("Distribution: %s", distro))

	report := runDoctorChecks(kubectl, distro, printDoctorCheckProgress)
	printDoctorReportFooter(report)
	return report
}

// RunSetupDoctorAndPrint streams setup-preflight progress and results.
func RunSetupDoctorAndPrint(kubectl core.KubectlRunner) DoctorReport {
	core.Section("Cluster Doctor")
	core.Info("Detecting Kubernetes distribution — reading node kubelet versions, node names, and current context")
	distro := DetectDistribution(kubectl)
	core.Info(fmt.Sprintf("Distribution: %s", distro))

	report := runDoctorChecksWithSpecs(kubectl, distro, printDoctorCheckProgress, doctorSetupCheckSpecs(kubectl, distro))
	printDoctorReportFooter(report)
	return report
}

// RunPostSetupSmoke executes a short post-setup operational gate for installed
// MCP Runtime clusters. It covers basic login-path and dependency readiness,
// not the full `cluster diagnostics` suite.
func RunPostSetupSmoke(kubectl core.KubectlRunner) DoctorReport {
	distro := DetectDistribution(kubectl)
	return runDoctorChecksWithSpecs(kubectl, distro, nil, doctorPostSetupCheckSpecs(kubectl))
}

// RunPostSetupSmokeAndPrint streams the post-setup operational smoke checks.
func RunPostSetupSmokeAndPrint(kubectl core.KubectlRunner) DoctorReport {
	core.Section("Post-setup operational smoke")
	core.Info("Checking nodes, Postgres, platform API, Sentinel rollouts, PVCs, and auth probe")
	distro := DetectDistribution(kubectl)
	core.Info(fmt.Sprintf("Distribution: %s", distro))

	report := runDoctorChecksWithSpecs(kubectl, distro, printDoctorCheckProgress, doctorPostSetupCheckSpecs(kubectl))
	printDoctorReportFooter(report)
	return report
}

func runDoctorChecks(kubectl core.KubectlRunner, distro Distribution, progress DoctorCheckProgress) DoctorReport {
	return runDoctorChecksWithSpecs(kubectl, distro, progress, doctorCheckSpecs(kubectl, distro))
}

func runDoctorChecksWithSpecs(kubectl core.KubectlRunner, distro Distribution, progress DoctorCheckProgress, specs []doctorCheckSpec) DoctorReport {
	checks := make([]DoctorCheck, 0, len(specs))
	for i, spec := range specs {
		finish := func(DoctorCheck) {}
		if progress != nil {
			event := DoctorCheckProgressEvent{
				Name:   spec.Name,
				Detail: spec.Detail,
				Index:  i + 1,
				Total:  len(specs),
			}
			if progressFinish := progress(event); progressFinish != nil {
				finish = progressFinish
			}
		}
		check := spec.Run()
		if check.Name == "" {
			check.Name = spec.Name
		}
		finish(check)
		checks = append(checks, check)
	}
	return DoctorReport{
		Distribution: distro,
		Checks:       checks,
	}
}

func doctorCheckSpecs(kubectl core.KubectlRunner, distro Distribution) []doctorCheckSpec {
	return []doctorCheckSpec{
		{
			Name:   fmt.Sprintf("namespace %s", doctorMCPServersNamespace),
			Detail: "reading namespace metadata from the Kubernetes API",
			Run:    func() DoctorCheck { return checkNamespaceExists(kubectl, doctorMCPServersNamespace) },
		},
		{
			Name:   fmt.Sprintf("namespace %s default serviceaccount", doctorMCPServersNamespace),
			Detail: "confirming pods in the runtime namespace have a default service account",
			Run:    func() DoctorCheck { return checkNamespaceDefaultServiceAccount(kubectl, doctorMCPServersNamespace) },
		},
		{
			Name:   fmt.Sprintf("namespace %s quota/limitrange", doctorMCPServersNamespace),
			Detail: "listing ResourceQuota and LimitRange objects that can block smoke pods",
			Run:    func() DoctorCheck { return checkNamespacePolicyGuardrails(kubectl, doctorMCPServersNamespace) },
		},
		{
			Name:   fmt.Sprintf("namespace %s pod admission", doctorMCPServersNamespace),
			Detail: "submitting a server-side dry-run pod to exercise admission webhooks and quota",
			Run:    func() DoctorCheck { return checkNamespacePodAdmission(kubectl, doctorMCPServersNamespace) },
		},
		{Name: "MCPServer CRD", Detail: "checking that the MCPServer API type is installed", Run: func() DoctorCheck { return checkMCPServerCRD(kubectl) }},
		{Name: "operator readiness", Detail: "reading ready and desired replicas for the operator deployment", Run: func() DoctorCheck { return checkOperatorReady(kubectl) }},
		{Name: "operator webhook TLS expiry", Detail: "checking operator admission webhook serving certificate expiry", Run: func() DoctorCheck { return checkOperatorWebhookCertExpiry(kubectl) }},
		{Name: "operator registry endpoint", Detail: "checking the operator uses a node-pullable registry endpoint", Run: func() DoctorCheck { return checkOperatorRegistryEndpoint(kubectl) }},
		{Name: "operator reconcile errors (last 10m)", Detail: "scanning recent operator logs for reconcile failure patterns", Run: func() DoctorCheck { return checkOperatorRecentReconcileErrors(kubectl) }},
		{Name: "operator ClusterRole rules", Detail: "verifying mcp-runtime-operator-role grants get/list/watch on the resources the informer cache needs", Run: func() DoctorCheck { return checkOperatorClusterRoleRules(kubectl) }},
		{Name: "traefik ingressClass", Detail: "checking that the traefik IngressClass exists", Run: func() DoctorCheck { return checkTraefikIngressClass(kubectl) }},
		{Name: "traefik deployment readiness", Detail: "reading ready and desired replicas for Traefik", Run: func() DoctorCheck { return checkTraefikDeploymentReady(kubectl, distro) }},
		{Name: "traefik web entrypoint", Detail: "checking the Traefik Service ports for the web entrypoint", Run: func() DoctorCheck { return checkTraefikWebEntrypoint(kubectl, distro) }},
		{Name: "traefik service exposure", Detail: "checking LoadBalancer or NodePort exposure for the web entrypoint", Run: func() DoctorCheck { return checkTraefikServiceExposure(kubectl, distro) }},
		{Name: "ingress LoadBalancer status", Detail: "checking host-based MCP Runtime ingresses for published LoadBalancer status", Run: func() DoctorCheck { return checkIngressLoadBalancerStatus(kubectl) }},
		{Name: "platform API live inventory ingress", Detail: "checking team namespace NetworkPolicies allow platform API probes to MCPServer Services", Run: func() DoctorCheck { return checkPlatformAPILiveInventoryNetworkPolicy(kubectl) }},
		{Name: "mcp-servers DNS/network", Detail: "launching a temporary curl pod in mcp-servers to reach the registry service", Run: func() DoctorCheck { return checkMCPServersDNSAndNetwork(kubectl) }},
		{
			Name:   "ingress route probe",
			Detail: "reading the first MCP ingress and launching a temporary curl pod against Traefik",
			Run:    func() DoctorCheck { return checkIngressRouteProbe(kubectl, doctorMCPServersNamespace, distro) },
		},
		{Name: "registry Service", Detail: "checking the bundled registry Service and NodePort", Run: func() DoctorCheck { return checkRegistryService(kubectl) }},
		{Name: registryIngressHostsCheckName, Detail: "checking registry Ingress rule hosts match its TLS hosts and the platform registry host", Run: func() DoctorCheck { return checkRegistryIngressHosts(kubectl) }},
		{Name: "registry reachability (in-cluster)", Detail: "launching a temporary curl pod in registry to call /v2/ over cluster DNS", Run: func() DoctorCheck { return checkRegistryReachableFromCluster(kubectl) }},
		{Name: "MCPServer registry image refs", Detail: "checking MCPServer specs for registry Service IP image references that kubelet cannot TLS-verify", Run: func() DoctorCheck { return checkRegistryServiceIPImageRefs(kubectl) }},
		{Name: "MCPServer imagePullSecrets", Detail: "checking MCPServer imagePullSecrets reference existing Secrets in each server namespace", Run: func() DoctorCheck { return checkMCPServerImagePullSecrets(kubectl) }},
		{Name: "team registry pull secrets", Detail: "checking managed team namespaces for the canonical dockerconfig pull Secret", Run: func() DoctorCheck { return checkManagedTeamRegistryPullSecrets(kubectl) }},
		{Name: "team workload serviceaccounts", Detail: "checking managed team workload serviceaccounts wire the canonical registry pull Secret", Run: func() DoctorCheck { return checkManagedTeamWorkloadServiceAccounts(kubectl) }},
		{
			Name:   "mcp-servers imagePullSecrets",
			Detail: "reading default service account pull secrets and verifying referenced Secret objects",
			Run:    func() DoctorCheck { return checkMCPServersImagePullSecrets(kubectl, doctorMCPServersNamespace) },
		},
		{
			Name:   "mcp-servers image pull smoke",
			Detail: "creating a temporary pod and waiting up to 90s for kubelet image pull readiness",
			Run:    func() DoctorCheck { return checkMCPServersImagePullSmoke(kubectl, doctorMCPServersNamespace) },
		},
		{Name: "registry HTTP pull mismatch", Detail: "listing pods and inspecting image-pull failures for HTTP-vs-HTTPS registry errors", Run: func() DoctorCheck { return checkRegistryHTTPPullMismatch(kubectl) }},
		{Name: "registry image pull diagnostics", Detail: "inspecting image-pull failures for registry TLS, auth, DNS, or corrupt-manifest errors", Run: func() DoctorCheck { return checkRegistryImagePullDiagnostics(kubectl) }},
		{Name: "sentinel Kafka readiness", Detail: "checking the bundled Kafka StatefulSet is ready so analytics ingestion can function", Run: func() DoctorCheck { return checkSentinelKafkaReadiness(kubectl) }},
		{Name: "sentinel ingest readiness", Detail: "checking the analytics ingest deployment is ready", Run: func() DoctorCheck { return checkSentinelIngestReadiness(kubectl) }},
		{Name: "sentinel platform API readiness", Detail: "checking the platform-api deployment and /health + /ready endpoints", Run: func() DoctorCheck { return checkSentinelPlatformAPIReadiness(kubectl) }},
		{Name: "sentinel workload rollout health", Detail: "detecting unavailable deployments, stuck rollouts, crash loops, and repeated container restarts", Run: func() DoctorCheck { return checkSentinelWorkloadHealth(kubectl) }},
		{Name: "sentinel Postgres credential drift", Detail: "comparing the database password Secret with the live Postgres process before API pods fail authentication", Run: func() DoctorCheck { return checkSentinelPostgresCredentialDrift(kubectl) }},
		{Name: "sentinel analytics API readiness", Detail: "checking the analytics-api deployment and /health + /ready endpoints", Run: func() DoctorCheck { return checkSentinelAnalyticsAPIReadiness(kubectl) }},
		{Name: "sentinel telemetry pipeline", Detail: "checking the collector Service, endpoints, workload, and trace pipeline configuration", Run: func() DoctorCheck { return checkSentinelTelemetryPipeline(kubectl) }},
		{Name: "persistent volume claims", Detail: "checking that discovered PVCs are Bound", Run: func() DoctorCheck { return checkPersistentVolumeClaims(kubectl) }},
		{Name: "runtime API Kubernetes API egress", Detail: "checking the runtime-api NetworkPolicy allows the cluster's actual Kubernetes API endpoint port", Run: func() DoctorCheck { return checkRuntimeAPIKubernetesAPIEgress(kubectl) }},
		{Name: "cluster image architecture", Detail: "checking MCP_IMAGE_PLATFORM matches Kubernetes node architectures", Run: func() DoctorCheck { return checkClusterImageArchitecture(kubectl) }},
		{Name: "DNS NetworkPolicy portability", Detail: "checking runtime-api egress matches the cluster's actual DNS pod labels", Run: func() DoctorCheck { return checkDNSNetworkPolicyPortability(kubectl) }},
		{Name: "storage class readiness", Detail: "checking the configured StorageClass exists and is safe for the cluster topology", Run: func() DoctorCheck { return checkStorageClassReadiness(kubectl) }},
		{Name: "sentinel session-local deployment scaling", Detail: "checking UI and gateway stay at one replica until shared session storage exists", Run: func() DoctorCheck { return checkSessionLocalDeploymentScaling(kubectl) }},
		{Name: "sentinel secrets", Detail: "reading Sentinel API, admin, UI, and ingest keys from mcp-sentinel-secrets", Run: func() DoctorCheck { return checkSentinelSecrets(kubectl) }},
		{Name: "sentinel secret consumer freshness", Detail: "checking runtime-api pods were started after the latest auth Secret update", Run: func() DoctorCheck { return checkSentinelSecretConsumerFreshness(kubectl) }},
		{Name: "sentinel OIDC configuration", Detail: "checking tenant/public login configuration is complete", Run: func() DoctorCheck { return checkSentinelOIDCConfiguration(kubectl) }},
		{Name: "gateway analytics credentials", Detail: "checking gateway sidecars have ingest credentials when analytics is enabled", Run: func() DoctorCheck { return checkGatewayAnalyticsCredentials(kubectl) }},
		{Name: "sentinel API auth probe", Detail: "launching a temporary curl pod with UI_API_KEY against runtime-api", Run: func() DoctorCheck { return checkSentinelAPIAuthProbe(kubectl) }},
		{Name: "sentinel runtime catalog probe", Detail: "calling authenticated runtime servers and tools endpoints through the in-cluster service", Run: func() DoctorCheck { return checkSentinelRuntimeCatalogProbe(kubectl) }},
		{Name: "mcp-auth deployment", Detail: "checking the optional mcp-auth authorization server rollout when installed", Run: func() DoctorCheck { return checkMCPAuthDeployment(kubectl) }},
		{Name: "mcp-auth secrets", Detail: "checking optional mcp-auth signing-key and TLS Secret material", Run: func() DoctorCheck { return checkMCPAuthSecrets(kubectl) }},
		{Name: "runtime API image display refs", Detail: "checking runtime API server listings do not leak internal registry pull hosts", Run: func() DoctorCheck { return checkRuntimeAPIImageDisplayRefs(kubectl) }},
		{Name: "node capacity", Detail: "checking node metrics, then falling back to allocatable resources if metrics-server is absent", Run: func() DoctorCheck { return checkNodeCapacity(kubectl) }},
		{Name: "node kubelet/runtime health", Detail: "checking node Ready conditions, pressure conditions, kubelet versions, and reported container runtimes", Run: func() DoctorCheck { return checkNodeRuntimeHealth(kubectl) }},
		{Name: "node architecture compatibility", Detail: "discovering node architectures and detecting mixed-architecture clusters", Run: func() DoctorCheck { return checkNodeArchitectureCompatibility(kubectl) }},
		{Name: "runtime class compatibility", Detail: "checking MCPServer RuntimeClass references against installed RuntimeClass objects", Run: func() DoctorCheck { return checkRuntimeClassCompatibility(kubectl) }},
		{Name: "storage readiness", Detail: "checking StorageClasses and Pending PVCs before persistent components fail", Run: func() DoctorCheck { return checkStorageReadiness(kubectl) }},
		{Name: "pending pods", Detail: "listing Pending pods across all namespaces", Run: func() DoctorCheck { return checkPendingPodsByNamespace(kubectl) }},
		{
			Name:   "MCPServer reconcile smoke",
			Detail: "applying a temporary MCPServer and waiting up to 150s for deployment/service/ingress resources",
			Run:    func() DoctorCheck { return checkMCPServerReconcileSmoke(kubectl, doctorMCPServersNamespace) },
		},
	}
}

func doctorSetupCheckSpecs(kubectl core.KubectlRunner, distro Distribution) []doctorCheckSpec {
	return []doctorCheckSpec{
		{Name: "Kubernetes nodes ready", Detail: "checking that the Kubernetes API is reachable and every node is Ready", Run: func() DoctorCheck { return checkClusterNodesReady(kubectl) }},
		{Name: "node capacity", Detail: "checking allocatable node capacity before scheduling Runtime workloads", Run: func() DoctorCheck { return checkNodeCapacity(kubectl) }},
		{Name: "node kubelet/runtime health", Detail: "checking node Ready conditions, pressure conditions, kubelet versions, and reported container runtimes", Run: func() DoctorCheck { return checkNodeRuntimeHealth(kubectl) }},
		{Name: "node architecture compatibility", Detail: "discovering node architectures and detecting mixed-architecture clusters", Run: func() DoctorCheck { return checkNodeArchitectureCompatibility(kubectl) }},
		{Name: "runtime class compatibility", Detail: "checking configured RuntimeClass references before workloads are installed", Run: func() DoctorCheck { return checkRuntimeClassCompatibility(kubectl) }},
		{Name: "storage class readiness", Detail: "discovering the default or configured StorageClass before setup", Run: func() DoctorCheck { return checkStorageClassReadiness(kubectl) }},
		{Name: "traefik ingressClass", Detail: "checking that the traefik IngressClass exists", Run: func() DoctorCheck { return checkTraefikIngressClass(kubectl) }},
		{Name: "traefik deployment readiness", Detail: "reading ready and desired replicas for Traefik", Run: func() DoctorCheck { return checkTraefikDeploymentReady(kubectl, distro) }},
		{Name: "traefik web entrypoint", Detail: "checking the Traefik Service ports for the web entrypoint", Run: func() DoctorCheck { return checkTraefikWebEntrypoint(kubectl, distro) }},
		{Name: "traefik service exposure", Detail: "checking LoadBalancer or NodePort exposure for the web entrypoint", Run: func() DoctorCheck { return checkTraefikServiceExposure(kubectl, distro) }},
		{Name: "public ingress host config", Detail: "resolving platform, registry, and MCP public hosts from the environment", Run: checkPublicIngressHostConfig},
		{Name: "public ingress DNS", Detail: "resolving configured public hosts through the local DNS resolver", Run: checkPublicIngressDNS},
		{Name: "cert-manager readiness", Detail: "checking cert-manager deployments when TLS preflight is requested", Run: func() DoctorCheck { return checkCertManagerReadiness(kubectl) }},
		{Name: "TLS ClusterIssuer", Detail: "checking the configured cert-manager ClusterIssuer when MCP_TLS_CLUSTER_ISSUER is set", Run: func() DoctorCheck { return checkDoctorTLSClusterIssuer(kubectl) }},
		{Name: "ACME HTTP-01 exposure", Detail: "verifying the active Traefik web entrypoint exposes public port 80 when MCP_ACME_EMAIL is set", Run: func() DoctorCheck { return checkDoctorACMEHTTP01Exposure(kubectl, distro) }},
	}
}

// doctorPostSetupCheckSpecs is the short operational gate run at the end of
// setup. Keep this list small and login/dependency focused; deeper coverage
// stays in `cluster diagnostics`.
func doctorPostSetupCheckSpecs(kubectl core.KubectlRunner) []doctorCheckSpec {
	return []doctorCheckSpec{
		{Name: "Kubernetes nodes ready", Detail: "every node must be Ready or login and platform APIs can go dark", Run: func() DoctorCheck { return checkClusterNodesReady(kubectl) }},
		{Name: "persistent volume claims", Detail: "Bound PVCs so Postgres and other stateful deps can schedule", Run: func() DoctorCheck { return checkPersistentVolumeClaims(kubectl) }},
		{Name: "pending pods", Detail: "no Pending pods left after setup that block core services", Run: func() DoctorCheck { return checkPendingPodsByNamespace(kubectl) }},
		{Name: "sentinel secrets", Detail: "platform admin and API keys exist for login", Run: func() DoctorCheck { return checkSentinelSecrets(kubectl) }},
		{Name: "sentinel Postgres credential drift", Detail: "Postgres is running and matches the rendered Secret", Run: func() DoctorCheck { return checkSentinelPostgresCredentialDrift(kubectl) }},
		{Name: "sentinel platform API readiness", Detail: "platform-api deployment plus /health and /ready", Run: func() DoctorCheck { return checkSentinelPlatformAPIReadiness(kubectl) }},
		{Name: "sentinel workload rollout health", Detail: "no CrashLoopBackOff / ImagePullBackOff on Sentinel workloads", Run: func() DoctorCheck { return checkSentinelWorkloadHealth(kubectl) }},
		{Name: "sentinel API auth probe", Detail: "authenticated runtime-api call with the UI API key", Run: func() DoctorCheck { return checkSentinelAPIAuthProbe(kubectl) }},
	}
}

// DetectDistribution inspects node info to guess which distribution is running.
// This is best-effort: callers should treat DistroGeneric as "probably kubeadm/unknown".
func DetectDistribution(kubectl core.KubectlRunner) Distribution {
	cmd, err := kubectl.CommandArgs([]string{"get", "nodes", "-o", "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"})
	if err == nil {
		if out, err := cmd.Output(); err == nil {
			v := strings.ToLower(string(out))
			if strings.Contains(v, "+k3s") {
				return DistroK3s
			}
		}
	}

	cmd, err = kubectl.CommandArgs([]string{"get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"})
	if err == nil {
		if out, err := cmd.Output(); err == nil {
			names := strings.ToLower(string(out))
			switch {
			case strings.Contains(names, "kind-"):
				return DistroKind
			case strings.Contains(names, "minikube"):
				return DistroMinikube
			case strings.Contains(names, "docker-desktop"):
				return DistroDockerDesktop
			}
		}
	}

	cmd, err = kubectl.CommandArgs([]string{"config", "current-context"})
	if err == nil {
		if out, err := cmd.Output(); err == nil {
			ctx := strings.ToLower(strings.TrimSpace(string(out)))
			switch {
			case strings.HasPrefix(ctx, "kind-"):
				return DistroKind
			case strings.HasPrefix(ctx, "minikube"):
				return DistroMinikube
			case ctx == "docker-desktop":
				return DistroDockerDesktop
			}
		}
	}

	return DistroGeneric
}

type operatorClusterRoleResource struct {
	Resource string
	APIGroup string
}

// operatorClusterRoleResources is the minimum set of resource/API group pairs
// the deployed operator ClusterRole must allow (verbs at least get;list;watch)
// for the controller-runtime informer cache to sync. A drift here typically
// means the cluster was set up against an older config/rbac/role.yaml and never
// re-ran setup; the symptom is silent: MCPServer creates land in etcd but the
// operator never reconciles them.
var operatorClusterRoleResources = []operatorClusterRoleResource{
	{Resource: "serviceaccounts", APIGroup: ""},
	{Resource: "configmaps", APIGroup: ""},
	{Resource: "services", APIGroup: ""},
	{Resource: "deployments", APIGroup: "apps"},
	{Resource: "ingresses", APIGroup: "networking.k8s.io"},
}

type imagePullPodCandidate struct {
	Namespace string
	Name      string
	Images    []string
	Reasons   []string
	Messages  []string
}

type mcpServerImageRef struct {
	Namespace string
	Name      string
	Image     string
}

type mcpServerPullSecretRef struct {
	Namespace string
	Server    string
	Secret    string
}

type doctorIngressStatus struct {
	Namespace string
	Name      string
	Hosts     []string
	LBIP      string
	LBHost    string
}
