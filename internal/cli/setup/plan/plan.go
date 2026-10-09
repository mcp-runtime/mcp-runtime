// Package plan contains pure setup planning types and default resolution.
package plan

import (
	"strings"

	"mcp-runtime/internal/cli/cluster"
)

const (
	StorageModeDynamic  = "dynamic"
	StorageModeHostpath = "hostpath"
)

const (
	PlatformModeTenant = "tenant"
	PlatformModeOrg    = "org"
	PlatformModePublic = "public"
)

const (
	RegistryModeAuto         = "auto"
	RegistryModeBundledHTTP  = "bundled-http"
	RegistryModeBundledHTTPS = "bundled-https"
	RegistryModeExternal     = "external"
)

const (
	DefaultOrgCatalogNamespace    = "mcp-servers-org"
	DefaultPublicCatalogNamespace = "mcp-servers-public"
	DefaultTestMTLSClusterIssuer  = "mcp-runtime-ca"
	DefaultMCPAuthTLSSecret       = "mcp-auth-server-tls" // #nosec G101 -- Kubernetes Secret name, not a credential.
)

// Input captures the raw CLI inputs for setup.
type Input struct {
	Kubeconfig              string
	Context                 string
	RegistryType            string
	RegistryStorageSize     string
	RegistryMode            string
	ExternalRegistryURL     string
	ExternalRegistryUser    string
	ExternalRegistryPass    string
	StorageMode             string
	PlatformMode            string
	IngressMode             string
	IngressManifest         string
	IngressManifestChanged  bool
	ForceIngressInstall     bool
	TLSEnabled              bool
	ProvidedTLSSecrets      bool
	TestMode                bool
	TestMultiReplica        bool
	ParallelBuilds          bool
	StrictProd              bool
	DeployAnalytics         bool
	DeployMCPAuthServer     bool
	MCPAuthServerImage      string
	MCPAuthIssuerURL        string
	MCPAuthResourceURLs     []string
	MCPAuthTLSSecret        string
	MCPAuthSigningKeySecret string
	MCPAuthConnectorsFile   string
	MCPAuthConnector        string
	OperatorArgs            []string
	// Let's Encrypt (HTTP-01 via cert-manager). If empty, other TLS modes apply; mutually exclusive with TLSClusterIssuer.
	ACMEmail    string
	ACMEStaging bool
	// TLSClusterIssuer is a pre-existing cert-manager.io ClusterIssuer (e.g. org internal CA / Vault / ADCS). Mutually exclusive with ACMEmail.
	TLSClusterIssuer string
	// MTLSClusterIssuer enables optional adapter client-certificate validation
	// for gateway server and adapter client certificates. Name an enterprise
	// ClusterIssuer, or the bundled mcp-runtime-ca to have setup provision it.
	// Test mode defaults this to mcp-runtime-ca automatically.
	MTLSClusterIssuer  string
	InstallCertManager bool
}

// Plan captures the resolved setup decisions.
type Plan struct {
	Kubeconfig              string
	Context                 string
	RegistryType            string
	RegistryStorageSize     string
	RegistryMode            string
	ExternalRegistryURL     string
	ExternalRegistryUser    string
	ExternalRegistryPass    string
	StorageMode             string
	PlatformMode            string
	Ingress                 cluster.IngressOptions
	RegistryManifest        string
	TLSEnabled              bool
	ProvidedTLSSecrets      bool
	TestMode                bool
	TestMultiReplica        bool
	ParallelBuilds          bool
	StrictProd              bool
	DeployAnalytics         bool
	DeployMCPAuthServer     bool
	MCPAuthServerImage      string
	MCPAuthIssuerURL        string
	MCPAuthResourceURLs     []string
	MCPAuthTLSSecret        string
	MCPAuthSigningKeySecret string
	MCPAuthConnectorsFile   string
	MCPAuthConnector        string
	OperatorArgs            []string
	ACMEmail                string
	ACMEStaging             bool
	TLSClusterIssuer        string
	MTLSClusterIssuer       string
	InstallCertManager      bool
}

func NormalizePlatformMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return PlatformModeTenant, true
	case PlatformModeTenant:
		return PlatformModeTenant, true
	case PlatformModeOrg:
		return PlatformModeOrg, true
	case PlatformModePublic:
		return PlatformModePublic, true
	default:
		return "", false
	}
}

func NormalizeRegistryMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return RegistryModeAuto, true
	case RegistryModeAuto:
		return RegistryModeAuto, true
	case RegistryModeBundledHTTP:
		return RegistryModeBundledHTTP, true
	case RegistryModeBundledHTTPS:
		return RegistryModeBundledHTTPS, true
	case RegistryModeExternal:
		return RegistryModeExternal, true
	default:
		return "", false
	}
}

func CatalogNamespaceForPlatformMode(mode string) string {
	normalized, ok := NormalizePlatformMode(mode)
	if !ok {
		normalized = PlatformModeTenant
	}
	switch normalized {
	case PlatformModeOrg:
		return DefaultOrgCatalogNamespace
	case PlatformModePublic:
		return DefaultPublicCatalogNamespace
	default:
		return ""
	}
}

// Build resolves CLI inputs into a concrete setup plan.
func Build(input Input) Plan {
	if input.StorageMode == "" {
		input.StorageMode = StorageModeDynamic
	}
	if mode, ok := NormalizeRegistryMode(input.RegistryMode); ok {
		input.RegistryMode = mode
	} else {
		input.RegistryMode = RegistryModeAuto
	}
	if mode, ok := NormalizePlatformMode(input.PlatformMode); ok {
		input.PlatformMode = mode
	} else {
		input.PlatformMode = PlatformModeTenant
	}
	// Test mode defaults to the bundled mcp-runtime-ca workload issuer so the
	// mTLS path works out of the box on local clusters. Prod opts in explicitly
	// by naming an issuer via --mtls-cluster-issuer (the bundled name is allowed
	// and provisioned by setupWorkloadPKI).
	if input.TestMode && strings.TrimSpace(input.MTLSClusterIssuer) == "" {
		input.MTLSClusterIssuer = DefaultTestMTLSClusterIssuer
	}
	if input.DeployMCPAuthServer && strings.TrimSpace(input.MCPAuthTLSSecret) == "" {
		input.MCPAuthTLSSecret = DefaultMCPAuthTLSSecret
	}

	manifestPath := input.IngressManifest
	if !input.IngressManifestChanged {
		if input.TLSEnabled {
			manifestPath = "config/ingress/overlays/prod"
		} else {
			manifestPath = "config/ingress/overlays/http"
		}
	}

	registryManifest := "config/registry"
	if input.RegistryMode == RegistryModeBundledHTTPS {
		if input.StorageMode == StorageModeHostpath {
			registryManifest = "config/registry/overlays/hostpath-internal-tls"
		} else {
			registryManifest = "config/registry/overlays/internal-tls"
		}
	} else if input.StorageMode == StorageModeHostpath {
		if input.TLSEnabled {
			registryManifest = "config/registry/overlays/hostpath-tls"
		} else {
			registryManifest = "config/registry/overlays/hostpath"
		}
	} else if input.TLSEnabled {
		registryManifest = "config/registry/overlays/tls"
	}

	return Plan{
		Kubeconfig:           input.Kubeconfig,
		Context:              input.Context,
		RegistryType:         input.RegistryType,
		RegistryStorageSize:  input.RegistryStorageSize,
		RegistryMode:         input.RegistryMode,
		ExternalRegistryURL:  strings.TrimSpace(input.ExternalRegistryURL),
		ExternalRegistryUser: input.ExternalRegistryUser,
		ExternalRegistryPass: input.ExternalRegistryPass,
		StorageMode:          input.StorageMode,
		PlatformMode:         input.PlatformMode,
		Ingress: cluster.IngressOptions{
			Mode:     input.IngressMode,
			Manifest: manifestPath,
			Force:    input.ForceIngressInstall,
		},
		RegistryManifest:        registryManifest,
		TLSEnabled:              input.TLSEnabled,
		ProvidedTLSSecrets:      input.ProvidedTLSSecrets,
		TestMode:                input.TestMode,
		TestMultiReplica:        input.TestMode && input.TestMultiReplica,
		ParallelBuilds:          input.ParallelBuilds,
		StrictProd:              input.StrictProd,
		DeployAnalytics:         input.DeployAnalytics,
		DeployMCPAuthServer:     input.DeployMCPAuthServer,
		MCPAuthServerImage:      input.MCPAuthServerImage,
		MCPAuthIssuerURL:        input.MCPAuthIssuerURL,
		MCPAuthResourceURLs:     input.MCPAuthResourceURLs,
		MCPAuthTLSSecret:        input.MCPAuthTLSSecret,
		MCPAuthSigningKeySecret: input.MCPAuthSigningKeySecret,
		MCPAuthConnectorsFile:   input.MCPAuthConnectorsFile,
		MCPAuthConnector:        input.MCPAuthConnector,
		OperatorArgs:            input.OperatorArgs,
		ACMEmail:                input.ACMEmail,
		ACMEStaging:             input.ACMEStaging,
		InstallCertManager:      input.InstallCertManager,
		TLSClusterIssuer:        input.TLSClusterIssuer,
		MTLSClusterIssuer:       input.MTLSClusterIssuer,
	}
}
