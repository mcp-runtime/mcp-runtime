package platform

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

const defaultRegistrySecretName = "mcp-runtime-registry-pull" // #nosec G101 -- Kubernetes Secret object name, not credential material.

const defaultPlatformRegistryPullSecretName = "mcp-runtime-registry-pull-creds" // #nosec G101 -- Kubernetes Secret object name, not credential material.

const registryAdminAuthMiddleware = "registry-admin-auth@file"

const testModeOperatorImage = "docker.io/library/mcp-runtime-operator:latest"

const defaultGatewayProxyRepository = "mcp-sentinel-mcp-gateway"

const defaultAnalyticsIngestURL = "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events"

const gatewayOTELExporterOTLPEndpointEnv = "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT"

const defaultGatewayOTELExporterOTLPEndpoint = "http://otel-collector.mcp-sentinel.svc.cluster.local:4318"

func clusterServiceDNS(service, namespace string) string {
	domain := strings.Trim(strings.TrimSuffix(strings.TrimSpace(os.Getenv("MCP_CLUSTER_DOMAIN")), "."), ".")
	if domain == "" {
		domain = "cluster.local"
	}
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, domain)
}

func defaultAnalyticsIngestURLForCluster() string {
	return "http://" + clusterServiceDNS("mcp-sentinel-ingest", "mcp-sentinel") + ":8081/events"
}

func defaultGatewayOTELExporterOTLPEndpointForCluster() string {
	return "http://" + clusterServiceDNS("otel-collector", "mcp-sentinel") + ":4318"
}

const gatewayProxyDockerfilePath = "services/mcp-gateway/Dockerfile"

const gatewayProxyBuildContext = "."

// pathBasedSentinelIngressNames lists the dev path-based ingresses for the
// mcp-sentinel stack. Public-host installs remove these after applying the
// dedicated platform ingress so platform UI/API routes are not exposed on
// unrelated public hosts such as the MCP gateway host.
var pathBasedSentinelIngressNames = []string{
	"mcp-sentinel-gateway",
	"mcp-sentinel-gateway-observability",
	"mcp-sentinel-gateway-adapter-session",
	"mcp-sentinel-gateway-api",
	"mcp-sentinel-gateway-ingest",
}

const (
	defaultDevUserEmail     = "test@mcpruntime.org"
	defaultDevUserPassword  = "test@123"
	defaultDevAdminEmail    = "admin@mcpruntime.org"
	defaultDevAdminPassword = "admin@123"
)

var setupImageTagResolver = registry.DefaultGitTag

type setupImagePlatformCacheEntry struct {
	once     sync.Once
	platform string
	err      error
}

var setupImagePlatformCache = struct {
	sync.Mutex
	entries map[string]*setupImagePlatformCacheEntry
}{
	entries: map[string]*setupImagePlatformCacheEntry{},
}

type analyticsComponent struct {
	Name         string
	Repository   string
	Dockerfile   string
	BuildContext string
}

type AnalyticsImageSet struct {
	Ingest        string
	PlatformAPI   string
	RuntimeAPI    string
	AnalyticsAPI  string
	Processor     string
	UI            string
	DoctorSmoke   string
	Traefik       string
	ClickHouse    string
	Kafka         string
	Prometheus    string
	OTelCollector string
	Tempo         string
	Loki          string
	Promtail      string
	Grafana       string
}

var analyticsComponents = []analyticsComponent{
	{
		Name:         "ingest",
		Repository:   "mcp-sentinel-ingest",
		Dockerfile:   "services/ingest/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "platform-api",
		Repository:   "mcp-platform-api",
		Dockerfile:   "services/platform-api/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "runtime-api",
		Repository:   "mcp-runtime-api",
		Dockerfile:   "services/runtime-api/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "analytics-api",
		Repository:   "mcp-analytics-api",
		Dockerfile:   "services/analytics-api/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "processor",
		Repository:   "mcp-sentinel-processor",
		Dockerfile:   "services/processor/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "ui",
		Repository:   "mcp-sentinel-ui",
		Dockerfile:   "services/ui/Dockerfile",
		BuildContext: ".",
	},
	{
		Name:         "doctor-smoke",
		Repository:   "mcp-runtime-doctor-smoke",
		Dockerfile:   "services/doctor-smoke/Dockerfile",
		BuildContext: ".",
	},
}

func analyticsComponentsForSetup(_ bool) []analyticsComponent { return analyticsComponents }

type ClusterManagerAPI interface {
	InitCluster(kubeconfig, context string) error
	ConfigureCluster(opts cluster.IngressOptions) error
}

type RegistryManagerAPI interface {
	ShowRegistryInfo() error
	PushInCluster(source, target, helperNS string) error
}

type SetupDeps struct {
	ResolveExternalRegistryConfig   func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error)
	ClusterManager                  ClusterManagerAPI
	RegistryManager                 RegistryManagerAPI
	LoginRegistry                   func(logger *zap.Logger, registryURL, username, password string) error
	DeployRegistry                  func(logger *zap.Logger, namespace string, port int, registryType, registryStorageSize, manifestPath string) error
	WaitForDeploymentAvailable      func(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error
	WaitForDeploymentRolledOut      func(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error
	PrintDeploymentDiagnostics      func(deploy, namespace, selector string)
	SetupTLS                        func(logger *zap.Logger, plan setupplan.Plan) error
	BuildOperatorImage              func(image string) error
	PushOperatorImage               func(image string) error
	BuildGatewayProxyImage          func(image string) error
	PushGatewayProxyImage           func(image string) error
	BuildAnalyticsImage             func(image, dockerfilePath, buildContext string) error
	PushAnalyticsImage              func(image string) error
	EnsureNamespace                 func(namespace string) error
	EnsureCatalogNamespace          func(namespace string, labels map[string]string) error
	ResolvePlatformRegistryURL      func(logger *zap.Logger) string
	PushOperatorImageToInternal     func(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error
	PushGatewayProxyImageToInternal func(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error
	PushAnalyticsImageToInternal    func(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error
	DeployOperatorManifests         func(logger *zap.Logger, operatorImage, gatewayProxyImage string, operatorArgs []string, imagePullSecretName string) error
	DeployAnalyticsManifests        func(logger *zap.Logger, images AnalyticsImageSet, storageMode, platformMode string) error
	EnsureImagePullSecret           func(namespace, name, registry, username, password string) error
	DisableRegistryIngressAuth      func() error
	EnableRegistryIngressAuth       func() error
	ConfigureProvisionedRegistryEnv func(ext *config.ExternalRegistryConfig, secretName string) error
	RestartDeployment               func(name, namespace string) error
	CheckCRDInstalled               func(name string) error
	GetDeploymentTimeout            func() time.Duration
	GetRegistryPort                 func() int
	OperatorImageFor                func(ext *config.ExternalRegistryConfig) string
	GatewayProxyImageFor            func(ext *config.ExternalRegistryConfig) string
	// StampPlatformVersion records the installed platform version on platform
	// Deployment metadata after a successful setup. Nil skips stamping (tests).
	StampPlatformVersion func(version string) error
}

func (d SetupDeps) withDefaults(logger *zap.Logger) SetupDeps {
	if d.ResolveExternalRegistryConfig == nil {
		d.ResolveExternalRegistryConfig = registry.ResolveExternalRegistryConfig
	}
	if d.ClusterManager == nil {
		panic("cli: SetupDeps.ClusterManager must be set; pass it via SetupPlatform")
	}
	if d.RegistryManager == nil {
		d.RegistryManager = registry.DefaultRegistryManager(logger)
	}
	if d.LoginRegistry == nil {
		d.LoginRegistry = func(l *zap.Logger, registryURL, username, password string) error {
			return registry.DefaultRegistryManager(l).LoginRegistry(registryURL, username, password)
		}
	}
	if d.DeployRegistry == nil {
		d.DeployRegistry = deployRegistryClientGo
	}
	if d.WaitForDeploymentAvailable == nil {
		d.WaitForDeploymentAvailable = waitForDeploymentAvailable
	}
	if d.WaitForDeploymentRolledOut == nil {
		d.WaitForDeploymentRolledOut = waitForDeploymentRolledOut
	}
	if d.PrintDeploymentDiagnostics == nil {
		d.PrintDeploymentDiagnostics = printDeploymentDiagnostics
	}
	if d.SetupTLS == nil {
		d.SetupTLS = func(l *zap.Logger, p setupplan.Plan) error {
			return setupTLSWithClientGoAndPlan(l, p)
		}
	}
	if d.BuildOperatorImage == nil {
		d.BuildOperatorImage = buildOperatorImage
	}
	if d.PushOperatorImage == nil {
		d.PushOperatorImage = pushOperatorImage
	}
	if d.BuildGatewayProxyImage == nil {
		d.BuildGatewayProxyImage = buildGatewayProxyImage
	}
	if d.PushGatewayProxyImage == nil {
		d.PushGatewayProxyImage = pushGatewayProxyImage
	}
	if d.BuildAnalyticsImage == nil {
		d.BuildAnalyticsImage = buildAnalyticsImage
	}
	if d.PushAnalyticsImage == nil {
		d.PushAnalyticsImage = pushAnalyticsImage
	}
	if d.EnsureNamespace == nil {
		d.EnsureNamespace = func(namespace string) error {
			return ensureNamespaceWithLabels(namespace, nil)
		}
	}
	if d.EnsureCatalogNamespace == nil {
		d.EnsureCatalogNamespace = func(namespace string, labels map[string]string) error {
			return ensureNamespaceWithLabels(namespace, labels)
		}
	}
	if d.ResolvePlatformRegistryURL == nil {
		d.ResolvePlatformRegistryURL = resolveInternalPlatformRegistryURLClientGo
	}
	if d.PushOperatorImageToInternal == nil {
		d.PushOperatorImageToInternal = pushOperatorImageToInternalRegistry
	}
	if d.PushGatewayProxyImageToInternal == nil {
		d.PushGatewayProxyImageToInternal = pushGatewayProxyImageToInternalRegistry
	}
	if d.PushAnalyticsImageToInternal == nil {
		d.PushAnalyticsImageToInternal = pushAnalyticsImageToInternalRegistry
	}
	if d.DeployOperatorManifests == nil {
		d.DeployOperatorManifests = deployOperatorManifests
	}
	if d.DeployAnalyticsManifests == nil {
		d.DeployAnalyticsManifests = deployAnalyticsManifests
	}
	if d.EnsureImagePullSecret == nil {
		d.EnsureImagePullSecret = func(namespace, name, registryURL, username, password string) error {
			return ensureImagePullSecretWithKubectl(core.DefaultKubectlClient(), namespace, name, registryURL, username, password)
		}
	}
	if d.DisableRegistryIngressAuth == nil {
		d.DisableRegistryIngressAuth = disableRegistryIngressAuth
	}
	if d.EnableRegistryIngressAuth == nil {
		d.EnableRegistryIngressAuth = enableRegistryIngressAuth
	}
	if d.ConfigureProvisionedRegistryEnv == nil {
		d.ConfigureProvisionedRegistryEnv = configureProvisionedRegistryEnv
	}
	if d.RestartDeployment == nil {
		d.RestartDeployment = restartDeployment
	}
	if d.CheckCRDInstalled == nil {
		d.CheckCRDInstalled = checkCRDInstalled
	}
	if d.GetDeploymentTimeout == nil {
		d.GetDeploymentTimeout = core.GetDeploymentTimeout
	}
	if d.GetRegistryPort == nil {
		d.GetRegistryPort = core.GetRegistryPort
	}
	if d.OperatorImageFor == nil {
		d.OperatorImageFor = getOperatorImage
	}
	if d.GatewayProxyImageFor == nil {
		d.GatewayProxyImageFor = getGatewayProxyImage
	}
	return d
}

// buildOperatorArgs constructs operator command-line arguments from flags.
// Only includes flags that were explicitly set.
func BuildOperatorArgs(metricsAddr, probeAddr string, leaderElect, leaderElectChanged bool) []string {
	var args []string

	if metricsAddr != "" {
		args = append(args, "--metrics-bind-address="+metricsAddr)
	}
	if probeAddr != "" {
		args = append(args, "--health-probe-bind-address="+probeAddr)
	}
	if leaderElectChanged {
		args = append(args, fmt.Sprintf("--leader-elect=%t", leaderElect))
	}

	return args
}

func ValidateStorageMode(mode string) error {
	switch mode {
	case setupplan.StorageModeDynamic, setupplan.StorageModeHostpath:
		return nil
	default:
		cause := core.NewWithSentinel(core.ErrSetupInvalidStorageMode, fmt.Sprintf("invalid storage mode %q", mode))
		return core.WrapWithSentinel(core.ErrFieldRequired, cause, "invalid --storage-mode; expected dynamic or hostpath")
	}
}

func ValidatePlatformMode(mode string) error {
	if _, ok := setupplan.NormalizePlatformMode(mode); ok {
		return nil
	}
	cause := core.NewWithSentinel(core.ErrSetupInvalidPlatformMode, fmt.Sprintf("invalid platform mode %q", mode))
	return core.WrapWithSentinel(core.ErrFieldRequired, cause, "invalid --platform-mode; expected tenant, org, or public")
}

func ValidatePublicPlatformAuthEnv(platformMode string, tlsEnabled, testMode bool) error {
	return ValidatePublicPlatformAuthConfig(platformMode, tlsEnabled, testMode, nil)
}

func ValidatePublicPlatformAuthConfig(platformMode string, tlsEnabled, testMode bool, existingData map[string]string) error {
	if !publicPlatformAuthConfigRequired(platformMode, tlsEnabled, testMode) {
		return nil
	}
	if publicBrowserLoginConfigConfigured(existingData) {
		return nil
	}
	return core.NewWithSentinel(
		core.ErrFieldRequired,
		"--platform-mode public with --with-tls requires browser login configuration: set GOOGLE_CLIENT_ID or MCP_GOOGLE_CLIENT_ID for Google sign-in, or set OIDC_ISSUER and OIDC_AUDIENCE for another provider (OIDC_JWKS_URL is optional when issuer discovery is available), or rerun against a cluster whose mcp-sentinel-config already contains those values",
	)
}

func publicPlatformAuthConfigRequired(platformMode string, tlsEnabled, testMode bool) bool {
	mode, ok := setupplan.NormalizePlatformMode(platformMode)
	return ok && mode == setupplan.PlatformModePublic && tlsEnabled && !testMode
}

func publicBrowserLoginConfigConfigured(existingData map[string]string) bool {
	if publicAuthConfigValue(existingData, "GOOGLE_CLIENT_ID") != "" {
		return true
	}
	oidcIssuer := publicAuthConfigValue(existingData, "OIDC_ISSUER")
	oidcAudience := publicAuthConfigValue(existingData, "OIDC_AUDIENCE")
	return oidcIssuer != "" && oidcAudience != ""
}

func publicAuthConfigValue(existingData map[string]string, key string) string {
	if envValue := setupAnalyticsConfigEnvValue(key); envValue != "" {
		return envValue
	}
	return strings.TrimSpace(existingData[key])
}

func SetupPlatform(logger *zap.Logger, plan setupplan.Plan, clusterMgr ClusterManagerAPI) error {
	return setupPlatformWithDeps(logger, plan, SetupDeps{ClusterManager: clusterMgr, StampPlatformVersion: stampPlatformVersionClientGo}.withDefaults(logger))
}

func buildOperatorArgs(metricsAddr, probeAddr string, leaderElect, leaderElectChanged bool) []string {
	return BuildOperatorArgs(metricsAddr, probeAddr, leaderElect, leaderElectChanged)
}

func setupPlatformWithDeps(logger *zap.Logger, plan setupplan.Plan, deps SetupDeps) error {
	deps = deps.withDefaults(logger)
	initPlatformKubeconfig(plan.Kubeconfig)
	core.Section("MCP Runtime Setup")

	// Propagate test mode to build helpers so they can choose faster/safer build paths.
	if plan.TestMode {
		if err := os.Setenv("MCP_RUNTIME_TEST_MODE", "1"); err != nil {
			return core.WrapWithSentinel(core.ErrSetupSetRuntimeTestModeFailed, err, fmt.Sprintf("set MCP_RUNTIME_TEST_MODE: %v", err))
		}
		if strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN")) == "" {
			if err := os.Setenv("MCP_TRUST_DOMAIN", "cluster.local"); err != nil {
				return fmt.Errorf("set test-mode MCP_TRUST_DOMAIN: %w", err)
			}
		}
	} else {
		if err := os.Unsetenv("MCP_RUNTIME_TEST_MODE"); err != nil {
			return core.WrapWithSentinel(core.ErrSetupUnsetRuntimeTestModeFailed, err, fmt.Sprintf("unset MCP_RUNTIME_TEST_MODE: %v", err))
		}
	}
	if err := os.Setenv("MCP_PLATFORM_MODE", plan.PlatformMode); err != nil {
		return core.WrapWithSentinel(core.ErrSetupSetPlatformModeFailed, err, fmt.Sprintf("set MCP_PLATFORM_MODE: %v", err))
	}

	extRegistry, usingExternalRegistry, registrySecretName, err := resolveRegistrySetup(logger, plan, deps)
	if err != nil {
		core.LogStructuredError(logger, err, "Invalid registry setup configuration")
		return err
	}
	existingPublicAuthConfig, err := existingPublicAuthConfigForSetup(plan)
	if err != nil {
		core.LogStructuredError(logger, err, "Invalid public platform auth configuration")
		return err
	}
	if err := validateNonTestSetupWithAuthConfig(plan, extRegistry, usingExternalRegistry, existingPublicAuthConfig); err != nil {
		core.LogStructuredError(logger, err, "Invalid non-test setup configuration")
		return err
	}
	applySetupPlanToCLIConfig(plan)
	for _, warning := range setupWarnings(plan, extRegistry, usingExternalRegistry) {
		core.Warn(warning)
	}
	ctx := &SetupContext{
		Plan:                  plan,
		ExternalRegistry:      extRegistry,
		UsingExternalRegistry: usingExternalRegistry,
		RegistrySecretName:    registrySecretName,
	}
	// Re-enable registry ingress auth on every exit path. The setup pipeline
	// temporarily strips the `registry-admin-auth@file` middleware so the
	// internal in-cluster image push helper can talk to the registry while
	// the auth-resolver is still being wired. If we put the re-enable as a
	// pipeline step it would be skipped whenever an earlier step fails,
	// leaving the public registry without auth.
	defer func() {
		if !ctx.RegistryAuthStaged {
			return
		}
		if err := deps.EnableRegistryIngressAuth(); err != nil {
			core.Error("Failed to re-enable registry ingress auth after setup")
			core.LogStructuredError(logger, err, "Re-enable registry ingress auth")
			return
		}
		ctx.RegistryAuthStaged = false
	}()
	if err := runSetupSteps(logger, deps, ctx, buildSetupSteps(ctx)); err != nil {
		return err
	}

	core.Success("Platform setup complete")
	fmt.Println(core.Green("\nPlatform is ready. Use 'mcp-runtime status' to check everything."))
	printPlatformEntrypoints(plan.TLSEnabled)
	return nil
}

func setupClusterSteps(logger *zap.Logger, kubeconfig, context string, ingressOpts cluster.IngressOptions, deps SetupDeps) error {
	// Step 1: Initialize cluster
	core.Step("Step 1: Initialize cluster")
	core.Info("Installing CRD")
	if err := deps.ClusterManager.InitCluster(kubeconfig, context); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterInitFailed, err, fmt.Sprintf("failed to initialize cluster: %v", err))
		core.Error("Cluster initialization failed")
		core.LogStructuredError(logger, wrappedErr, "Cluster initialization failed")
		return wrappedErr
	}
	core.Info("Cluster initialized")

	// Step 2: Configure cluster
	core.Step("Step 2: Configure cluster")
	core.Info("Checking ingress controller")
	if err := deps.ClusterManager.ConfigureCluster(ingressOpts); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterConfigFailed, err, fmt.Sprintf("cluster configuration failed: %v", err))
		core.Error("Cluster configuration failed")
		core.LogStructuredError(logger, wrappedErr, "Cluster configuration failed")
		return wrappedErr
	}
	core.Info("Cluster configuration complete")
	return nil
}

// catalogNamespaceLabels returns the labels the platform API expects to find on
// a shared catalog namespace. Keeping these aligned with EnsureCatalogNamespace
// in services/runtime-api/internal/runtimeapi/deployments.go lets the runtime-side
// ensure call degrade to an idempotent patch instead of a create, which is
// what allows non-admin users to publish into the catalog without giving the
// API ServiceAccount cluster-wide namespace-create RBAC.
func catalogNamespaceLabels(platformMode string) map[string]string {
	return map[string]string{
		"platform.mcpruntime.org/managed":    "true",
		"mcpruntime.org/scope":               platformMode,
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		core.LabelManagedBy:                  core.LabelManagedByValue,
	}
}

func setupCatalogNamespaceStep(logger *zap.Logger, plan setupplan.Plan, deps SetupDeps) error {
	namespace := setupplan.CatalogNamespaceForPlatformMode(plan.PlatformMode)
	if namespace == "" {
		// tenant mode has no shared catalog namespace.
		return nil
	}
	core.Step(fmt.Sprintf("Provisioning %s catalog namespace %q", plan.PlatformMode, namespace))
	if err := deps.EnsureCatalogNamespace(namespace, catalogNamespaceLabels(plan.PlatformMode)); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrSetupStepFailed,
			err,
			fmt.Sprintf("ensure catalog namespace %q failed: %v", namespace, err),
			map[string]any{"namespace": namespace, "platform_mode": plan.PlatformMode, "component": "setup"},
		)
		core.Error("Catalog namespace provisioning failed")
		core.LogStructuredError(logger, wrappedErr, "Catalog namespace provisioning failed")
		return wrappedErr
	}
	core.Success(fmt.Sprintf("Catalog namespace %q ready", namespace))
	return nil
}

type traefikDeploymentSpec struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name         string   `json:"name"`
					Args         []string `json:"args"`
					VolumeMounts []struct {
						Name      string `json:"name"`
						MountPath string `json:"mountPath"`
					} `json:"volumeMounts"`
				} `json:"containers"`
				Volumes []struct {
					Name string `json:"name"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type jsonPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// analyticsFailedRollout records a failed rollout and optional tee capture from runRolloutWithOptionalDebugCapture.
type analyticsFailedRollout struct {
	kind, name, namespace, rolloutLog string
}

// operatorEnvVar represents an environment variable for the operator.
type operatorEnvVar struct {
	Name  string
	Value string
}
