// Package setup owns routing for the setup top-level command.
package setup

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	setupplatform "mcp-runtime/internal/cli/setup/platform"
	"mcp-runtime/pkg/egresscidrs"
	"mcp-runtime/pkg/publicroutes"
)

// loadEnvFile reads KEY=VALUE pairs from path and sets any that are not already
// present in the process environment. Explicit env vars and CLI flags always
// take precedence over values in the file.
func loadEnvFile(path string) error {
	// #nosec G304 -- path is an explicit user-supplied CLI flag value.
	envs, err := godotenv.Read(path)
	if err != nil {
		return err
	}
	for key, val := range envs {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("setting %s: %w", key, err)
			}
		}
	}
	return nil
}

type manager struct {
	logger     *zap.Logger
	clusterMgr setupplatform.ClusterManagerAPI
}

func newManager(runtime *core.Runtime, clusterMgr setupplatform.ClusterManagerAPI) *manager {
	return &manager{logger: runtime.Logger(), clusterMgr: clusterMgr}
}

// New returns the setup command. clusterMgr is the cluster operator that setup
// uses for cluster init and ingress configuration; it is supplied by the
// composition root so setup does not import the cluster command package.
func New(runtime *core.Runtime, clusterMgr setupplatform.ClusterManagerAPI) *cobra.Command {
	var envFile string
	var routes publicroutes.Routes
	var registryType string
	var registryStorageSize string
	var registryMode string
	var externalRegistryURL string
	var externalRegistryUsername string
	var externalRegistryPassword string
	var storageMode string
	var platformMode string
	var kubeconfig string
	var kubeContext string
	var ingressMode string
	var ingressManifest string
	var forceIngressInstall bool
	var tlsEnabled bool
	var providedTLSSecrets bool
	var testMode bool
	var testMultiReplica bool
	var parallelBuilds bool
	var strictProd bool
	var withoutPlatformStack bool
	var withMCPAuthServer bool
	var mcpAuthServerImage string
	var mcpAuthIssuerURL string
	var mcpAuthResourceURLs []string
	var mcpAuthTLSSecret string
	var mcpAuthSigningKeySecret string
	var mcpAuthConnectorsFile string
	var mcpAuthConnector string
	var operatorMetricsAddr string
	var operatorProbeAddr string
	var operatorLeaderElect bool
	var acmeEmail string
	var acmeStaging bool
	var tlsClusterIssuer string
	var mtlsClusterIssuer string
	var skipCertManagerInstall bool
	var podEgressCIDRs string
	var podEgressExceptCIDRs string
	mgr := newManager(runtime, clusterMgr)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Setup the complete MCP platform",
		Long: `Setup the complete MCP platform including:
- Kubernetes cluster initialization
- Internal container registry deployment (Docker Registry)
- Operator deployment
- Ingress controller configuration

The platform deploys an internal Docker registry by default, which teams
will use to push and pull container images.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if envFile != "" {
				if err := loadEnvFile(envFile); err != nil {
					return fmt.Errorf("--env-file %s: %w", envFile, err)
				}
				// DefaultCLIConfig is initialized at package load time, before the env
				// file is applied. Reload it now so registry/ingress host resolution
				// picks up MCP_PLATFORM_DOMAIN and related vars from the env file.
				core.DefaultCLIConfig = core.LoadCLIConfig()
			}

			// Apply env var fallbacks for every flag that was not explicitly set on
			// the command line. Primary names follow the MCP_SETUP_* convention used
			// in config/deployments/mcpruntime-org.env; legacy names are kept for
			// backward compatibility where they already existed.
			envStr := func(flag string, val *string, vars ...string) {
				if cmd.Flags().Changed(flag) {
					return
				}
				for _, envVar := range vars {
					if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
						*val = v
						return
					}
				}
			}
			// envCSV mirrors envStr for repeatable/comma-separated flags.
			envCSV := func(flag string, val *[]string, vars ...string) {
				if cmd.Flags().Changed(flag) {
					return
				}
				for _, envVar := range vars {
					v := strings.TrimSpace(os.Getenv(envVar))
					if v == "" {
						continue
					}
					values := []string{}
					for _, part := range strings.Split(v, ",") {
						if trimmed := strings.TrimSpace(part); trimmed != "" {
							values = append(values, trimmed)
						}
					}
					if len(values) > 0 {
						*val = values
						return
					}
				}
			}
			envBool := func(flag string, val *bool, vars ...string) {
				if cmd.Flags().Changed(flag) {
					return
				}
				for _, envVar := range vars {
					v := strings.TrimSpace(os.Getenv(envVar))
					if v == "1" || strings.EqualFold(v, "true") {
						*val = true
						return
					}
				}
			}

			// Cluster access
			envStr("kubeconfig", &kubeconfig, "MCP_SETUP_KUBECONFIG")
			envStr("context", &kubeContext, "MCP_KUBE_CONTEXT")

			// Registry
			envStr("registry-type", &registryType, "MCP_REGISTRY_TYPE")
			envStr("registry-storage", &registryStorageSize, "MCP_REGISTRY_STORAGE_SIZE")
			envStr("registry-mode", &registryMode, "MCP_SETUP_REGISTRY_MODE", "MCP_REGISTRY_MODE")
			envStr("external-registry-url", &externalRegistryURL, "PROVISIONED_REGISTRY_URL")
			envStr("external-registry-username", &externalRegistryUsername, "PROVISIONED_REGISTRY_USERNAME")
			envStr("external-registry-password", &externalRegistryPassword, "PROVISIONED_REGISTRY_PASSWORD")

			// Storage / platform
			envStr("storage-mode", &storageMode, "MCP_STORAGE_MODE")
			envStr("platform-mode", &platformMode, "MCP_SETUP_PLATFORM_MODE", "MCP_PLATFORM_MODE")
			envStr("pod-egress-cidrs", &podEgressCIDRs, "MCP_POD_EGRESS_CIDRS")
			envStr("pod-egress-except-cidrs", &podEgressExceptCIDRs, "MCP_POD_EGRESS_EXCEPT_CIDRS")
			if err := os.Setenv("MCP_POD_EGRESS_CIDRS", strings.TrimSpace(podEgressCIDRs)); err != nil {
				return err
			}
			if err := os.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", strings.TrimSpace(podEgressExceptCIDRs)); err != nil {
				return err
			}
			allows, err := egresscidrs.Parse(podEgressCIDRs)
			if err != nil {
				return err
			}
			except, err := egresscidrs.Parse(podEgressExceptCIDRs)
			if err != nil {
				return err
			}
			if _, err := egresscidrs.Blocks(allows, except); err != nil {
				return err
			}

			// Ingress
			envStr("ingress", &ingressMode, "MCP_SETUP_INGRESS")
			envStr("ingress-manifest", &ingressManifest, "MCP_SETUP_INGRESS_MANIFEST")
			envBool("force-ingress-install", &forceIngressInstall, "MCP_FORCE_INGRESS_INSTALL")

			envStr("platform-path-prefix", &routes.Platform, "MCP_PLATFORM_PATH_PREFIX")
			envStr("grafana-path-prefix", &routes.Grafana, "MCP_GRAFANA_PATH_PREFIX")
			envStr("registry-path", &routes.Registry, "MCP_REGISTRY_PATH")
			envStr("docs-path", &routes.Docs, "MCP_DOCS_PATH")
			envStr("docs-url", &routes.DocsURL, "MCP_DOCS_URL")

			// TLS
			envBool("with-tls", &tlsEnabled, "MCP_SETUP_WITH_TLS")
			envBool("provided-tls-secrets", &providedTLSSecrets, "MCP_SETUP_PROVIDED_TLS_SECRETS")
			envStr("acme-email", &acmeEmail, "MCP_ACME_EMAIL")
			envBool("acme-staging", &acmeStaging, "MCP_ACME_STAGING")
			envStr("tls-cluster-issuer", &tlsClusterIssuer, "MCP_SETUP_TLS_CLUSTER_ISSUER", "MCP_TLS_CLUSTER_ISSUER")
			envStr("mtls-cluster-issuer", &mtlsClusterIssuer, "MCP_SETUP_MTLS_CLUSTER_ISSUER", "MCP_MTLS_CLUSTER_ISSUER")
			envBool("skip-cert-manager-install", &skipCertManagerInstall, "MCP_SETUP_SKIP_CERT_MANAGER_INSTALL")

			// Deployment behaviour
			envBool("test-mode", &testMode, "MCP_SETUP_TEST_MODE")
			envBool("test-multi-replica", &testMultiReplica, "MCP_SETUP_TEST_MULTI_REPLICA")
			if testMultiReplica && !testMode {
				return fmt.Errorf("--test-multi-replica requires --test-mode")
			}
			envBool("parallel-builds", &parallelBuilds, "MCP_PARALLEL_BUILDS")
			envBool("strict-prod", &strictProd, "MCP_STRICT_PROD")
			envBool("without-platform-stack", &withoutPlatformStack, "MCP_WITHOUT_PLATFORM_STACK")
			envBool("with-mcp-auth-server", &withMCPAuthServer, "MCP_SETUP_WITH_MCP_AUTH_SERVER")
			envStr("mcp-auth-server-image", &mcpAuthServerImage, "MCP_SETUP_MCP_AUTH_SERVER_IMAGE")
			envStr("mcp-auth-issuer-url", &mcpAuthIssuerURL, "MCP_SETUP_MCP_AUTH_ISSUER_URL")
			if withMCPAuthServer && !testMode && strings.TrimSpace(mcpAuthIssuerURL) == "" {
				mcpAuthIssuerURL = setupplatform.DefaultMCPAuthIssuerURL()
			}
			envCSV("mcp-auth-resource-url", &mcpAuthResourceURLs, "MCP_SETUP_MCP_AUTH_RESOURCE_URL")
			envStr("mcp-auth-tls-secret", &mcpAuthTLSSecret, "MCP_SETUP_MCP_AUTH_TLS_SECRET")
			envStr("mcp-auth-signing-key-secret", &mcpAuthSigningKeySecret, "MCP_SETUP_MCP_AUTH_SIGNING_KEY_SECRET")
			envStr("mcp-auth-connectors-file", &mcpAuthConnectorsFile, "MCP_SETUP_MCP_AUTH_CONNECTORS_FILE")
			envStr("mcp-auth-connector", &mcpAuthConnector, "MCP_SETUP_MCP_AUTH_CONNECTOR")
			if withMCPAuthServer && withoutPlatformStack {
				return fmt.Errorf("--with-mcp-auth-server requires the bundled platform stack")
			}
			if withMCPAuthServer && !testMode {
				if !tlsEnabled {
					return fmt.Errorf("--with-mcp-auth-server requires --with-tls outside --test-mode so its HTTPS issuer has a managed or provided certificate")
				}
				parsed, err := url.Parse(strings.TrimSpace(mcpAuthIssuerURL))
				if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
					return fmt.Errorf("--mcp-auth-issuer-url must be an absolute HTTPS URL outside --test-mode")
				}
				if mcpAuthConnectorsFile == "" || mcpAuthConnector == "" {
					return fmt.Errorf("production mcp-auth deployment requires --mcp-auth-connectors-file and --mcp-auth-connector")
				}
				if strings.TrimSpace(mcpAuthSigningKeySecret) == "" {
					return fmt.Errorf("production mcp-auth deployment requires --mcp-auth-signing-key-secret; an ephemeral signing key would invalidate every issued token on restart")
				}
				// Resource audiences are reconciled from OAuth MCPServer objects;
				// this flag only supplies an optional initial bootstrap list.
				for _, value := range mcpAuthResourceURLs {
					resource, err := url.Parse(strings.TrimSpace(value))
					if err != nil || resource.Scheme != "https" || resource.Host == "" {
						return fmt.Errorf("--mcp-auth-resource-url must be absolute HTTPS URLs outside --test-mode; each must equal spec.auth.audience of an MCP server this authorization server issues tokens for")
					}
				}
			}
			if mcpAuthConnector != "" && mcpAuthConnectorsFile == "" {
				return fmt.Errorf("--mcp-auth-connector requires --mcp-auth-connectors-file")
			}
			if mcpAuthConnectorsFile != "" && !withMCPAuthServer {
				return fmt.Errorf("--mcp-auth-connectors-file requires --with-mcp-auth-server")
			}

			// Validate public paths before any cluster mutation.
			if err := routes.Validate(); err != nil {
				return err
			}
			resolvedRoutes := routes.WithDefaults()
			if core.GetPlatformIngressHost() == "" && (resolvedRoutes.Platform != "/" || resolvedRoutes.Grafana != "/grafana") {
				return fmt.Errorf("custom platform or Grafana paths require MCP_PLATFORM_INGRESS_HOST or MCP_PLATFORM_DOMAIN so setup can render their public ingress routes")
			}
			if withMCPAuthServer {
				issuer := mcpAuthIssuerURL
				if issuer == "" && testMode {
					issuer = "http://localhost:18080/mcp-auth"
				}
				if err := routes.ValidateAuth(core.GetPlatformIngressHost(), issuer); err != nil {
					return err
				}
			}
			core.DefaultCLIConfig.PublicRoutes = routes.WithDefaults()

			// Validate after all env vars are applied.
			if err := setupplatform.ValidateStorageMode(storageMode); err != nil {
				return err
			}
			if err := setupplatform.ValidateRegistryMode(registryMode); err != nil {
				return err
			}
			if err := setupplatform.ValidatePlatformMode(platformMode); err != nil {
				return err
			}

			operatorArgs := setupplatform.BuildOperatorArgs(
				operatorMetricsAddr,
				operatorProbeAddr,
				operatorLeaderElect,
				cmd.Flags().Changed("operator-leader-elect"),
			)

			if err := setupplatform.ValidateTLSSetupCLIFlags(tlsEnabled, providedTLSSecrets, acmeEmail, tlsClusterIssuer, acmeStaging, skipCertManagerInstall); err != nil {
				return err
			}
			if err := setupplatform.ValidateRegistryTLSMode(registryMode, tlsEnabled, acmeEmail); err != nil {
				return err
			}
			if err := setupplatform.ValidateMTLSSetupCLIFlags(testMode, tlsEnabled, mtlsClusterIssuer); err != nil {
				return err
			}

			plan := setupplan.Build(setupplan.Input{
				Kubeconfig:              kubeconfig,
				Context:                 kubeContext,
				RegistryType:            registryType,
				RegistryStorageSize:     registryStorageSize,
				RegistryMode:            registryMode,
				ExternalRegistryURL:     externalRegistryURL,
				ExternalRegistryUser:    externalRegistryUsername,
				ExternalRegistryPass:    externalRegistryPassword,
				StorageMode:             storageMode,
				PlatformMode:            platformMode,
				IngressMode:             ingressMode,
				IngressManifest:         ingressManifest,
				IngressManifestChanged:  cmd.Flags().Changed("ingress-manifest"),
				ForceIngressInstall:     forceIngressInstall,
				TLSEnabled:              tlsEnabled,
				ProvidedTLSSecrets:      providedTLSSecrets,
				TestMode:                testMode,
				TestMultiReplica:        testMultiReplica,
				ParallelBuilds:          parallelBuilds,
				StrictProd:              strictProd,
				DeployAnalytics:         !withoutPlatformStack,
				DeployMCPAuthServer:     withMCPAuthServer,
				MCPAuthServerImage:      mcpAuthServerImage,
				MCPAuthIssuerURL:        mcpAuthIssuerURL,
				MCPAuthResourceURLs:     mcpAuthResourceURLs,
				MCPAuthTLSSecret:        mcpAuthTLSSecret,
				MCPAuthSigningKeySecret: mcpAuthSigningKeySecret,
				MCPAuthConnectorsFile:   mcpAuthConnectorsFile,
				MCPAuthConnector:        mcpAuthConnector,
				OperatorArgs:            operatorArgs,
				ACMEmail:                acmeEmail,
				ACMEStaging:             acmeStaging,
				TLSClusterIssuer:        tlsClusterIssuer,
				MTLSClusterIssuer:       mtlsClusterIssuer,
				InstallCertManager:      !skipCertManagerInstall,
			})

			return setupplatform.SetupPlatform(mgr.logger, plan, mgr.clusterMgr)
		},
	}

	cmd.Flags().StringVar(&routes.Platform, "platform-path-prefix", "/", "Public dashboard path prefix (e.g. /platform); session and asset routes use this prefix")
	cmd.Flags().StringVar(&routes.Grafana, "grafana-path-prefix", "/grafana", "Public admin-only Grafana path prefix")
	cmd.Flags().StringVar(&routes.Registry, "registry-path", "/registry", "Public registry browser entry path; Docker and Kubernetes clients still use /v2/")
	cmd.Flags().StringVar(&routes.Docs, "docs-path", "/docs", "Public path that redirects to the configured documentation URL")
	cmd.Flags().StringVar(&routes.DocsURL, "docs-url", publicroutes.DefaultDocsURL, "HTTPS documentation destination used by the docs route and dashboard links")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Path to an env file to source before setup (e.g. config/deployments/mcpruntime-org.env); variables already in the environment are not overridden")
	cmd.Flags().StringVar(&registryType, "registry-type", "docker", "Registry type (docker; harbor coming soon)")
	cmd.Flags().StringVar(&registryStorageSize, "registry-storage", "20Gi", "Registry storage size (default: 20Gi)")
	cmd.Flags().StringVar(&registryMode, "registry-mode", "auto", "Registry setup mode (auto|bundled-http|bundled-https|external). auto uses a provisioned registry config when present, otherwise the bundled registry")
	cmd.Flags().StringVar(&externalRegistryURL, "external-registry-url", "", "External/provisioned registry URL for --registry-mode external (overrides PROVISIONED_REGISTRY_URL and registry provision config)")
	cmd.Flags().StringVar(&externalRegistryUsername, "external-registry-username", "", "External/provisioned registry username for --registry-mode external")
	cmd.Flags().StringVar(&externalRegistryPassword, "external-registry-password", "", "External/provisioned registry password for --registry-mode external (prefer PROVISIONED_REGISTRY_PASSWORD for shells)")
	cmd.Flags().StringVar(&storageMode, "storage-mode", "dynamic", "Storage mode for local/dev clusters (dynamic|hostpath). Use hostpath for single-node k3s/minikube/kind without a provisioner.")
	cmd.Flags().StringVar(&platformMode, "platform-mode", "tenant", "Platform access model (tenant|org|public). public exposes the catalog without login and lets signed-in users publish to the public preview namespace.")
	cmd.Flags().StringVar(&podEgressCIDRs, "pod-egress-cidrs", "", "Comma-separated destination CIDRs that team MCP servers may reach on TCP 443. Empty installs nothing. 0.0.0.0/0 is rejected. Private ranges also require --pod-egress-except-cidrs. Overrides env MCP_POD_EGRESS_CIDRS")
	cmd.Flags().StringVar(&podEgressExceptCIDRs, "pod-egress-except-cidrs", "", "Cluster pod and service CIDRs to keep out of --pod-egress-cidrs. Required when a destination is private. Overrides env MCP_POD_EGRESS_EXCEPT_CIDRS")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config; auto-detects /etc/rancher/k3s/k3s.yaml on k3s hosts)")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubernetes context to use")
	cmd.Flags().StringVar(&ingressMode, "ingress", "traefik", "Ingress controller to install automatically during setup (traefik|none)")
	cmd.Flags().StringVar(&ingressManifest, "ingress-manifest", "config/ingress/overlays/http", "Manifest to apply when installing the ingress controller")
	cmd.Flags().BoolVar(&forceIngressInstall, "force-ingress-install", false, "Force repo-managed ingress install when only an IngressClass exists; refuses active external Traefik")
	cmd.Flags().BoolVar(&tlsEnabled, "with-tls", false, "Enable TLS overlays (ingress/registry). Use --acme-email for public Let's Encrypt, --tls-cluster-issuer for an org ClusterIssuer, or the bundled mcp-runtime-ca private CA (no ACME) when neither is set")
	cmd.Flags().BoolVar(&providedTLSSecrets, "provided-tls-secrets", false, "Use operator-created Kubernetes TLS Secrets instead of issuing certificates. Requires --with-tls; do not combine with --acme-email or --tls-cluster-issuer")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Contact email for Let's Encrypt (HTTP-01 via cert-manager). Mutually exclusive with --tls-cluster-issuer. Overrides env MCP_ACME_EMAIL")
	cmd.Flags().StringVar(&tlsClusterIssuer, "tls-cluster-issuer", "", "Use an existing cert-manager ClusterIssuer (e.g. internal CA; setup does not create it). Mutually exclusive with --acme-email. Overrides env MCP_SETUP_TLS_CLUSTER_ISSUER")
	cmd.Flags().StringVar(&mtlsClusterIssuer, "mtls-cluster-issuer", "", "Enable adapter client-certificate authentication on gateway server routes using this workload ClusterIssuer (requires --with-tls). Name an enterprise issuer, or the bundled mcp-runtime-ca to have setup provision it. Test mode defaults to mcp-runtime-ca. Production must also set MCP_TRUST_DOMAIN. Overrides env MCP_SETUP_MTLS_CLUSTER_ISSUER")
	cmd.Flags().BoolVar(&acmeStaging, "acme-staging", false, "Use Let's Encrypt staging CA (also set MCP_ACME_STAGING=1)")
	cmd.Flags().BoolVar(&skipCertManagerInstall, "skip-cert-manager-install", false, "Do not install cert-manager; require CRDs to already exist")
	cmd.Flags().BoolVar(&testMode, "test-mode", false, "Test mode for local Kind/dev installs; builds and pushes latest-tag runtime images, provisions a local workload mTLS issuer, and relaxes production guardrails")
	cmd.Flags().BoolVar(&testMultiReplica, "test-multi-replica", false, "Use normal platform replica counts with --test-mode for features that need multiple replicas (default test mode uses one replica)")
	cmd.Flags().BoolVar(&parallelBuilds, "parallel-builds", false, "Build and publish setup images in parallel; keeps cluster, registry, TLS, and rollout sequencing unchanged")
	cmd.Flags().BoolVar(&strictProd, "strict-prod", false, "Require production-style registry and TLS validation for non-test setup")
	cmd.Flags().BoolVar(&withoutPlatformStack, "without-platform-stack", false, "Skip deploying the bundled platform stack")
	cmd.Flags().BoolVar(&withMCPAuthServer, "with-mcp-auth-server", false, "Deploy the optional bundled mcp-auth authorization server; production requires a platform domain, connector, and TLS-enabled ingress")
	cmd.Flags().StringVar(&mcpAuthServerImage, "mcp-auth-server-image", "docker.io/princekrroshan01/mcp-auth-server:0.4.4", "Container image for the optional bundled mcp-auth authorization server")
	cmd.Flags().StringVar(&mcpAuthIssuerURL, "mcp-auth-issuer-url", "", "Public HTTPS issuer URL for the bundled mcp-auth authorization server (defaults to https://auth.<MCP_PLATFORM_DOMAIN>/mcp-auth)")
	cmd.Flags().StringSliceVar(&mcpAuthResourceURLs, "mcp-auth-resource-url", nil, "Optional initial resource URI for the bundled mcp-auth server; the operator reconciles this list from OAuth MCPServer audiences")
	cmd.Flags().StringVar(&mcpAuthSigningKeySecret, "mcp-auth-signing-key-secret", "", "Secret holding the mcp-auth RSA signing key as private-key.pem (required outside --test-mode)")
	cmd.Flags().StringVar(&mcpAuthTLSSecret, "mcp-auth-tls-secret", "", "Override the managed TLS Secret for the bundled mcp-auth ingress (default: mcp-auth-server-tls)")
	cmd.Flags().StringVar(&mcpAuthConnectorsFile, "mcp-auth-connectors-file", "", "Provider connector JSON file for the bundled mcp-auth authorization server")
	cmd.Flags().StringVar(&mcpAuthConnector, "mcp-auth-connector", "", "Provider connector name to activate (requires --mcp-auth-connectors-file)")
	cmd.Flags().StringVar(&operatorMetricsAddr, "operator-metrics-addr", "", "Operator metrics bind address (default: :8080 from manager.yaml)")
	cmd.Flags().StringVar(&operatorProbeAddr, "operator-probe-addr", "", "Operator health probe bind address (default: :8081 from manager.yaml)")
	cmd.Flags().BoolVar(&operatorLeaderElect, "operator-leader-elect", false, "Override operator leader election when set")

	return cmd
}
