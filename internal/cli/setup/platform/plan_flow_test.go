package platform

import (
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

func TestBuildSetupPlan_DefaultHTTP(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		Kubeconfig:             "/tmp/kubeconfig",
		Context:                "my-context",
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            "dynamic",
		IngressMode:            "traefik",
		IngressManifest:        "config/ingress/overlays/http",
		IngressManifestChanged: false,
		ForceIngressInstall:    false,
		TLSEnabled:             false,
	})

	if plan.Ingress.Manifest != "config/ingress/overlays/http" {
		t.Fatalf("expected http ingress manifest, got %q", plan.Ingress.Manifest)
	}
	if plan.Kubeconfig != "/tmp/kubeconfig" {
		t.Fatalf("expected kubeconfig to be preserved, got %q", plan.Kubeconfig)
	}
	if plan.Context != "my-context" {
		t.Fatalf("expected context to be preserved, got %q", plan.Context)
	}
	if plan.RegistryManifest != "config/registry" {
		t.Fatalf("expected default registry manifest, got %q", plan.RegistryManifest)
	}
	if plan.RegistryMode != setupplan.RegistryModeAuto {
		t.Fatalf("expected default registry mode auto, got %q", plan.RegistryMode)
	}
}

func TestBuildSetupPlan_DefaultTLS(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            "dynamic",
		IngressMode:            "traefik",
		IngressManifest:        "config/ingress/overlays/http",
		IngressManifestChanged: false,
		ForceIngressInstall:    false,
		TLSEnabled:             true,
	})

	if plan.Ingress.Manifest != "config/ingress/overlays/prod" {
		t.Fatalf("expected tls ingress manifest, got %q", plan.Ingress.Manifest)
	}
	if plan.RegistryManifest != "config/registry/overlays/tls" {
		t.Fatalf("expected tls registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestBuildSetupPlan_BundledHTTPSRegistryManifest(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryMode: setupplan.RegistryModeBundledHTTPS,
		TLSEnabled:   true,
	})

	if plan.RegistryMode != setupplan.RegistryModeBundledHTTPS {
		t.Fatalf("expected bundled HTTPS registry mode, got %q", plan.RegistryMode)
	}
	if plan.RegistryManifest != "config/registry/overlays/internal-tls" {
		t.Fatalf("expected internal tls registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestBuildSetupPlan_HostpathBundledHTTPSRegistryManifest(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryMode: setupplan.RegistryModeBundledHTTPS,
		StorageMode:  setupplan.StorageModeHostpath,
		TLSEnabled:   true,
	})

	if plan.RegistryManifest != "config/registry/overlays/hostpath-internal-tls" {
		t.Fatalf("expected hostpath internal tls registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestBuildSetupPlan_TLSClusterIssuer(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		TLSEnabled:       true,
		TLSClusterIssuer: "company-ca",
	})
	if plan.TLSClusterIssuer != "company-ca" {
		t.Fatalf("expected TLSClusterIssuer preserved, got %q", plan.TLSClusterIssuer)
	}
}

func TestBuildSetupPlan_PlatformModeCatalogNamespaces(t *testing.T) {
	if _, ok := setupplan.NormalizePlatformMode("tenent"); ok {
		t.Fatal("NormalizePlatformMode accepted misspelled tenant mode")
	}
	if got := setupplan.CatalogNamespaceForPlatformMode(""); got != "" {
		t.Fatalf("default tenant catalog namespace = %q, want empty", got)
	}
	if got := setupplan.CatalogNamespaceForPlatformMode(setupplan.PlatformModeOrg); got != setupplan.DefaultOrgCatalogNamespace {
		t.Fatalf("org catalog namespace = %q, want %q", got, setupplan.DefaultOrgCatalogNamespace)
	}
	if got := setupplan.CatalogNamespaceForPlatformMode(setupplan.PlatformModePublic); got != setupplan.DefaultPublicCatalogNamespace {
		t.Fatalf("public catalog namespace = %q, want %q", got, setupplan.DefaultPublicCatalogNamespace)
	}

	plan := setupplan.Build(setupplan.Input{PlatformMode: setupplan.PlatformModePublic})
	if plan.PlatformMode != setupplan.PlatformModePublic {
		t.Fatalf("platform mode = %q, want %q", plan.PlatformMode, setupplan.PlatformModePublic)
	}
}

func TestBuildSetupPlan_CustomIngressManifest(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            "dynamic",
		IngressMode:            "traefik",
		IngressManifest:        "custom/manifest",
		IngressManifestChanged: true,
		ForceIngressInstall:    true,
		TLSEnabled:             true,
	})

	if plan.Ingress.Manifest != "custom/manifest" {
		t.Fatalf("expected custom ingress manifest, got %q", plan.Ingress.Manifest)
	}
	if plan.RegistryManifest != "config/registry/overlays/tls" {
		t.Fatalf("expected tls registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestBuildSetupPlan_PreservesTestModeAndOperatorArgs(t *testing.T) {
	operatorArgs := []string{"--metrics-bind-address=:9090", "--leader-elect=false"}
	plan := setupplan.Build(setupplan.Input{
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            "dynamic",
		IngressMode:            "traefik",
		IngressManifest:        "config/ingress/overlays/http",
		IngressManifestChanged: false,
		ForceIngressInstall:    false,
		TLSEnabled:             false,
		TestMode:               true,
		ParallelBuilds:         true,
		StrictProd:             true,
		OperatorArgs:           operatorArgs,
	})

	if !plan.TestMode {
		t.Fatal("expected test mode to be preserved")
	}
	if !plan.ParallelBuilds {
		t.Fatal("expected parallel builds to be preserved")
	}
	if !plan.StrictProd {
		t.Fatal("expected strict prod to be preserved")
	}
	if len(plan.OperatorArgs) != len(operatorArgs) {
		t.Fatalf("expected %d operator args, got %d", len(operatorArgs), len(plan.OperatorArgs))
	}
	for i := range operatorArgs {
		if plan.OperatorArgs[i] != operatorArgs[i] {
			t.Fatalf("expected operator arg %d to be %q, got %q", i, operatorArgs[i], plan.OperatorArgs[i])
		}
	}
}

func TestBuildSetupPlan_HostpathRegistryManifest(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            setupplan.StorageModeHostpath,
		IngressMode:            "traefik",
		IngressManifest:        "config/ingress/overlays/http",
		IngressManifestChanged: false,
		ForceIngressInstall:    false,
		TLSEnabled:             false,
	})

	if plan.RegistryManifest != "config/registry/overlays/hostpath" {
		t.Fatalf("expected hostpath registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestBuildSetupPlan_HostpathRegistryManifest_TLS(t *testing.T) {
	plan := setupplan.Build(setupplan.Input{
		RegistryType:           "docker",
		RegistryStorageSize:    "20Gi",
		StorageMode:            setupplan.StorageModeHostpath,
		IngressMode:            "traefik",
		IngressManifest:        "config/ingress/overlays/http",
		IngressManifestChanged: false,
		ForceIngressInstall:    false,
		TLSEnabled:             true,
	})

	if plan.RegistryManifest != "config/registry/overlays/hostpath-tls" {
		t.Fatalf("expected hostpath tls registry manifest, got %q", plan.RegistryManifest)
	}
}

func TestValidateNonTestSetupAllowsLenientDefaultMode(t *testing.T) {
	clearProductionSetupEnv(t)
	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false},
		&config.ExternalRegistryConfig{URL: "registry.example.com"},
		true,
	)
	if err != nil {
		t.Fatalf("expected lenient default mode to allow non-TLS registry, got %v", err)
	}
}

func setPublicDomainEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MCP_PLATFORM_DOMAIN", "mcpruntime.org")
	t.Setenv("MCP_PLATFORM_ADMIN_EMAIL", "admin@mcpruntime.org")
}

func clearProductionSetupEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MCP_PLATFORM_DOMAIN",
		"MCP_PLATFORM_ADMIN_EMAIL",
		"MCP_PLATFORM_ADMIN_PASSWORD",
		"ADMIN_USERS",
		"MCP_REGISTRY_INGRESS_HOST",
		"MCP_REGISTRY_ENDPOINT",
		"MCP_REGISTRY_HOST",
	} {
		t.Setenv(key, "")
	}
}

func TestValidateNonTestSetupAllowsBundledHTTPSStableInternalRegistry(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")
	core.DefaultCLIConfig = &core.CLIConfig{RegistryEndpoint: "registry.prod.example.com", RegistryIngressHost: "registry.prod.example.com"}

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, StrictProd: true, RegistryMode: setupplan.RegistryModeBundledHTTPS},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("expected stable bundled HTTPS internal registry to be allowed, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsDevRegistryURLByDefault(t *testing.T) {
	clearProductionSetupEnv(t)
	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false},
		&config.ExternalRegistryConfig{URL: "registry.local"},
		true,
	)
	if err != nil {
		t.Fatalf("expected default mode to allow local/internal registry, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsDevInternalRegistryEndpointByDefault(t *testing.T) {
	clearProductionSetupEnv(t)
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{RegistryEndpoint: "10.43.39.164:5000", RegistryIngressHost: "registry.local"}

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("expected default mode to allow local/internal registry host, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsMissingTLSInStrictProd(t *testing.T) {
	clearProductionSetupEnv(t)
	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false, StrictProd: true},
		&config.ExternalRegistryConfig{URL: "registry.example.com"},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "--with-tls") {
		t.Fatalf("expected strict-prod TLS validation error, got %v", err)
	}
}

func TestValidateRegistryTLSModeRejectsBundledHTTPSWithoutTLS(t *testing.T) {
	err := ValidateRegistryTLSMode(setupplan.RegistryModeBundledHTTPS, false, "")
	if err == nil || !strings.Contains(err.Error(), "--with-tls") {
		t.Fatalf("expected bundled HTTPS TLS validation error, got %v", err)
	}
}

func TestValidateRegistryTLSModeAllowsBundledHTTPSWithACME(t *testing.T) {
	err := ValidateRegistryTLSMode(setupplan.RegistryModeBundledHTTPS, true, "admin@example.com")
	if err != nil {
		t.Fatalf("expected bundled HTTPS with ACME to be allowed after internal TLS split, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsMissingPublicHostsWithoutStrictProd(t *testing.T) {
	clearProductionSetupEnv(t)
	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false},
		&config.ExternalRegistryConfig{URL: "registry.example.com"},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "platform host configuration is incomplete") {
		t.Fatalf("expected platform host validation error, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsPartialPublicHostConfigWithoutStrictProd(t *testing.T) {
	clearProductionSetupEnv(t)
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false},
		&config.ExternalRegistryConfig{URL: "registry.example.com"},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "MCP_PLATFORM_INGRESS_HOST") || !strings.Contains(err.Error(), "MCP_MCP_INGRESS_HOST") {
		t.Fatalf("expected missing public host env validation error, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsMissingPlatformAdminEnv(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "mcpruntime.org")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, RegistryMode: setupplan.RegistryModeBundledHTTPS},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "platform admin configuration is incomplete") {
		t.Fatalf("expected missing platform admin env validation error, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsPublicTLSWithoutBrowserLoginEnv(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")

	err := validateNonTestSetup(
		setupplan.Plan{PlatformMode: setupplan.PlatformModePublic, TLSEnabled: true, TestMode: false, RegistryMode: setupplan.RegistryModeBundledHTTPS},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLIENT_ID") {
		t.Fatalf("expected public browser login env validation error, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsExistingPublicBrowserLoginConfig(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")

	err := validateNonTestSetupWithAuthConfig(
		setupplan.Plan{PlatformMode: setupplan.PlatformModePublic, TLSEnabled: true, TestMode: false, RegistryMode: setupplan.RegistryModeBundledHTTPS},
		nil,
		false,
		map[string]string{"GOOGLE_CLIENT_ID": "client.apps.googleusercontent.com"},
	)
	if err != nil {
		t.Fatalf("expected existing browser login config to be allowed, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsBundledHTTPWithoutExplicitRegistryEndpoint(t *testing.T) {
	// bundled-http and bundled-https auto-discover the registry service; explicit
	// MCP_REGISTRY_ENDPOINT is no longer required for these modes. Only
	// --registry-mode auto still requires an explicit endpoint.
	setPublicDomainEnv(t)
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false, RegistryMode: setupplan.RegistryModeBundledHTTP},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("expected bundled-http without MCP_REGISTRY_ENDPOINT to pass validation, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsAutoRegistryModeWithoutRegistryEndpoint(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false, RegistryMode: setupplan.RegistryModeAuto},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "MCP_REGISTRY_ENDPOINT") {
		t.Fatalf("expected registry endpoint validation error for auto mode, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsPublicDomainAndRegistryEndpointWithoutStrictProd(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.local:32000")
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: false, TestMode: false, RegistryMode: setupplan.RegistryModeBundledHTTP},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("expected platform env validation to pass, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsBundledHTTPInStrictProd(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.local:32000")
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, StrictProd: true, RegistryMode: setupplan.RegistryModeBundledHTTP},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "bundled-http") {
		t.Fatalf("expected strict-prod bundled-http validation error, got %v", err)
	}
}

func TestValidateNonTestSetupAllowsBundledHTTPSInStrictProd(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, StrictProd: true, RegistryMode: setupplan.RegistryModeBundledHTTPS},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("expected strict-prod bundled-https to be allowed, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsAutoBundledRegistryInStrictProd(t *testing.T) {
	setPublicDomainEnv(t)
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.prod.example.com")
	t.Setenv("GOOGLE_CLIENT_ID", "client.apps.googleusercontent.com")

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, StrictProd: true, RegistryMode: setupplan.RegistryModeAuto},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "bundled registry requires --registry-mode bundled-https") {
		t.Fatalf("expected strict-prod auto bundled registry validation error, got %v", err)
	}
}

func TestResolveRegistrySetupUsesExternalRegistryFlagsInAutoMode(t *testing.T) {
	var got *config.ExternalRegistryConfig
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(flagCfg *config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			got = flagCfg
			return flagCfg, nil
		},
	}

	ext, usingExternal, _, err := resolveRegistrySetup(zap.NewNop(), setupplan.Plan{
		RegistryMode:         setupplan.RegistryModeAuto,
		ExternalRegistryURL:  "registry.example.com",
		ExternalRegistryUser: "user",
		ExternalRegistryPass: "pass",
	}, deps)
	if err != nil {
		t.Fatalf("resolveRegistrySetup returned error: %v", err)
	}
	if !usingExternal || ext == nil || ext.URL != "registry.example.com" {
		t.Fatalf("expected external registry from setup flags, got ext=%+v using=%t", ext, usingExternal)
	}
	if got == nil || got.Username != "user" || got.Password != "pass" {
		t.Fatalf("expected flag config to be passed through, got %+v", got)
	}
}

func TestResolveRegistrySetupRequiresExternalURLInExternalMode(t *testing.T) {
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return nil, nil
		},
	}

	_, _, _, err := resolveRegistrySetup(zap.NewNop(), setupplan.Plan{RegistryMode: setupplan.RegistryModeExternal}, deps)
	if err == nil || !strings.Contains(err.Error(), "external registry url is required") {
		t.Fatalf("expected external registry URL error, got %v", err)
	}
}

func TestResolveRegistrySetupRejectsExternalFlagsInBundledMode(t *testing.T) {
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			t.Fatal("did not expect external registry resolver in bundled mode")
			return nil, nil
		},
	}

	_, _, _, err := resolveRegistrySetup(zap.NewNop(), setupplan.Plan{
		RegistryMode:        setupplan.RegistryModeBundledHTTP,
		ExternalRegistryURL: "registry.example.com",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "--external-registry-*") {
		t.Fatalf("expected bundled mode external flag error, got %v", err)
	}
}

func TestRegistryInternalCertificateSANsIncludesInternalNamesForBundledHTTPS(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "registry.registry.svc.cluster.local:5000",
		RegistryIngressHost: "registry.example.com",
		McpIngressHost:      "mcp.example.com",
	}

	dnsNames, ipAddresses := registryInternalCertificateSANs(setupplan.Plan{RegistryMode: setupplan.RegistryModeBundledHTTPS})
	for _, want := range []string{
		"registry.local",
		"registry.registry.svc",
		"registry.registry.svc.cluster.local",
	} {
		if !contains(dnsNames, want) {
			t.Fatalf("expected DNS SAN %q in %v", want, dnsNames)
		}
	}
	if len(ipAddresses) != 0 {
		t.Fatalf("did not expect IP SANs, got %v", ipAddresses)
	}
}

func TestRegistryInternalCertificateSANsIncludesIPRegistryEndpoint(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "10.43.24.102:5000",
		RegistryIngressHost: "registry.local",
	}

	_, ipAddresses := registryInternalCertificateSANs(setupplan.Plan{RegistryMode: setupplan.RegistryModeBundledHTTPS})
	if !contains(ipAddresses, "10.43.24.102") {
		t.Fatalf("expected IP SAN for registry endpoint, got %v", ipAddresses)
	}
}

func TestRegistryCertificateSANsStayPublicForBundledHTTPS(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "10.43.24.102:5000",
		RegistryIngressHost: "registry.example.com",
		McpIngressHost:      "mcp.example.com",
	}

	dnsNames, ipAddresses := registryCertificateSANs(setupplan.Plan{RegistryMode: setupplan.RegistryModeBundledHTTPS})
	for _, want := range []string{"registry.example.com", "mcp.example.com"} {
		if !contains(dnsNames, want) {
			t.Fatalf("expected public DNS SAN %q in %v", want, dnsNames)
		}
	}
	for _, internal := range []string{"registry.registry.svc.cluster.local", "10.43.24.102"} {
		if contains(dnsNames, internal) || contains(ipAddresses, internal) {
			t.Fatalf("public registry cert unexpectedly contains internal SAN %q (dns=%v ips=%v)", internal, dnsNames, ipAddresses)
		}
	}
}

func TestBundledRegistryInternalIssuerUsesPrivateCAForACMEIssuer(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{DefaultOutput: []byte("https://acme-v02.api.letsencrypt.org/directory")})

	got, err := bundledRegistryInternalIssuerName(kubectl, setupplan.Plan{
		RegistryMode:     setupplan.RegistryModeBundledHTTPS,
		TLSClusterIssuer: "letsencrypt-prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != certmanager.CertClusterIssuerName {
		t.Fatalf("expected internal registry issuer %q, got %q", certmanager.CertClusterIssuerName, got)
	}
}

func TestBundledRegistryInternalIssuerKeepsNonACMEIssuer(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{DefaultOutput: []byte("")})

	got, err := bundledRegistryInternalIssuerName(kubectl, setupplan.Plan{
		RegistryMode:     setupplan.RegistryModeBundledHTTPS,
		TLSClusterIssuer: "company-ca",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "company-ca" {
		t.Fatalf("expected company-ca issuer, got %q", got)
	}
}

func TestBundledRegistryInternalIssuerIncludesKubectlStderr(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		DefaultOutput: []byte("Error from server (Forbidden): clusterissuers is forbidden"),
		DefaultErr:    fmt.Errorf("exit status 1"),
	})

	_, err := bundledRegistryInternalIssuerName(kubectl, setupplan.Plan{
		RegistryMode:     setupplan.RegistryModeBundledHTTPS,
		TLSClusterIssuer: "letsencrypt-prod",
	})
	if err == nil || !strings.Contains(err.Error(), "clusterissuers is forbidden") {
		t.Fatalf("expected kubectl stderr in error, got %v", err)
	}
}

func TestValidateNonTestSetupRejectsDevRegistryURLInStrictProd(t *testing.T) {
	setPublicDomainEnv(t)

	err := validateNonTestSetup(
		setupplan.Plan{TLSEnabled: true, TestMode: false, StrictProd: true},
		&config.ExternalRegistryConfig{URL: "registry.local"},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "dev-only registry URL") {
		t.Fatalf("expected strict-prod dev registry validation error, got %v", err)
	}
}

type callRecorder struct {
	calls []string
	waits []string
}

func (c *callRecorder) add(name string) {
	c.calls = append(c.calls, name)
}

func (c *callRecorder) addWait(name string) {
	c.waits = append(c.waits, name)
}

func (c *callRecorder) has(name string) bool {
	for _, call := range c.calls {
		if call == name {
			return true
		}
	}
	return false
}

func (c *callRecorder) hasWait(name string) bool {
	for _, call := range c.waits {
		if call == name {
			return true
		}
	}
	return false
}

type fakeClusterManager struct {
	rec *callRecorder
}

func (f *fakeClusterManager) InitCluster(_, _ string) error {
	f.rec.add("cluster-init")
	return nil
}

func (f *fakeClusterManager) ConfigureCluster(cluster.IngressOptions) error {
	f.rec.add("cluster-config")
	return nil
}

type fakeRegistryManager struct {
	rec *callRecorder
}

func (f *fakeRegistryManager) ShowRegistryInfo() error {
	f.rec.add("registry-info")
	return nil
}

func (f *fakeRegistryManager) PushInCluster(_, _, _ string) error {
	f.rec.add("registry-push")
	return nil
}

func TestSetupPlatformWithDeps_ExternalRegistry(t *testing.T) {
	setPublicDomainEnv(t)

	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return &config.ExternalRegistryConfig{
				URL:      "registry.example.com",
				Username: "user",
				Password: "pass",
			}, nil
		},
		ClusterManager:              &fakeClusterManager{rec: rec},
		RegistryManager:             &fakeRegistryManager{rec: rec},
		LoginRegistry:               func(*zap.Logger, string, string, string) error { rec.add("login"); return nil },
		DeployRegistry:              func(*zap.Logger, string, int, string, string, string) error { rec.add("deploy-registry"); return nil },
		WaitForDeploymentAvailable:  func(_ *zap.Logger, name, _, _ string, _ time.Duration) error { rec.addWait(name); return nil },
		PrintDeploymentDiagnostics:  func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                    func(*zap.Logger, setupplan.Plan) error { rec.add("tls"); return nil },
		BuildOperatorImage:          func(string) error { rec.add("build"); return nil },
		PushOperatorImage:           func(string) error { rec.add("push"); return nil },
		BuildGatewayProxyImage:      func(string) error { rec.add("build-gateway"); return nil },
		PushGatewayProxyImage:       func(string) error { rec.add("push-gateway"); return nil },
		EnsureNamespace:             func(string) error { rec.add("ensure-ns"); return nil },
		ResolvePlatformRegistryURL:  func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error { rec.add("push-internal"); return nil },
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-gateway-internal")
			return nil
		},
		DeployOperatorManifests: func(*zap.Logger, string, string, []string, string) error {
			rec.add("deploy-operator")
			return nil
		},
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error {
			rec.add("configure-env")
			return nil
		},
		EnsureImagePullSecret: func(string, string, string, string, string) error {
			rec.add("pull-secret")
			return nil
		},
		RestartDeployment:    func(string, string) error { rec.add("restart"); return nil },
		CheckCRDInstalled:    func(string) error { rec.add("check-crd"); return nil },
		GetDeploymentTimeout: func() time.Duration { return time.Second },
		GetRegistryPort:      func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("operator-image")
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("gateway-image")
			return "registry.example.com/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest:   "config/registry",
		TLSEnabled:         true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err != nil {
		t.Fatalf("setupPlatformWithDeps returned error: %v", err)
	}

	if !rec.has("login") {
		t.Fatalf("expected external registry login to be called")
	}
	if rec.has("deploy-registry") {
		t.Fatalf("did not expect internal registry deployment")
	}
	if rec.has("registry-info") {
		t.Fatalf("did not expect internal registry info")
	}
	if !rec.has("build") || !rec.has("push") || !rec.has("build-gateway") || !rec.has("push-gateway") {
		t.Fatalf("expected image build/push for external registry")
	}
	if rec.has("push-internal") || rec.has("push-gateway-internal") {
		t.Fatalf("did not expect internal registry push")
	}
}

func TestSetupPlatformWithDeps_InternalRegistryTLS(t *testing.T) {
	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return nil, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry: func(*zap.Logger, string, string, string) error {
			rec.add("login")
			return nil
		},
		DeployRegistry:             func(*zap.Logger, string, int, string, string, string) error { rec.add("deploy-registry"); return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error { rec.addWait(name); return nil },
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { rec.add("tls"); return nil },
		BuildOperatorImage:         func(string) error { rec.add("build"); return nil },
		PushOperatorImage:          func(string) error { rec.add("push"); return nil },
		BuildGatewayProxyImage:     func(string) error { rec.add("build-gateway"); return nil },
		PushGatewayProxyImage:      func(string) error { rec.add("push-gateway"); return nil },
		EnsureNamespace:            func(string) error { rec.add("ensure-ns"); return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-internal")
			return nil
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-gateway-internal")
			return nil
		},
		DeployOperatorManifests: func(*zap.Logger, string, string, []string, string) error { rec.add("deploy-operator"); return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error {
			rec.add("configure-env")
			return nil
		},
		RestartDeployment:    func(string, string) error { rec.add("restart"); return nil },
		CheckCRDInstalled:    func(string) error { rec.add("check-crd"); return nil },
		GetDeploymentTimeout: func() time.Duration { return time.Second },
		GetRegistryPort:      func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("operator-image")
			return "registry.local/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("gateway-image")
			return "registry.local/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/prod",
			Force:    false,
		},
		RegistryManifest:   "config/registry/overlays/tls",
		TLSEnabled:         true,
		TestMode:           true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err != nil {
		t.Fatalf("setupPlatformWithDeps returned error: %v", err)
	}

	if !rec.has("tls") {
		t.Fatalf("expected TLS setup to be called")
	}
	if !rec.has("deploy-registry") {
		t.Fatalf("expected internal registry deployment")
	}
	if !rec.has("registry-info") {
		t.Fatalf("expected registry info")
	}
	if !rec.has("build") || !rec.has("build-gateway") || !rec.has("push-internal") || !rec.has("push-gateway-internal") || !rec.has("ensure-ns") {
		t.Fatalf("expected internal build/push path, got calls: %v", rec.calls)
	}
	if rec.has("configure-env") || rec.has("login") {
		t.Fatalf("did not expect external registry configuration")
	}
	if !rec.hasWait("registry") || !rec.hasWait("mcp-runtime-operator-controller-manager") {
		t.Fatalf("expected waits for registry and operator deployments")
	}
}

func TestSetupPlatformWithDeps_ExternalRegistryTLS(t *testing.T) {
	setPublicDomainEnv(t)

	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return &config.ExternalRegistryConfig{
				URL:      "registry.example.com",
				Username: "user",
				Password: "pass",
			}, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry: func(*zap.Logger, string, string, string) error {
			rec.add("login")
			return nil
		},
		DeployRegistry:             func(*zap.Logger, string, int, string, string, string) error { rec.add("deploy-registry"); return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error { rec.addWait(name); return nil },
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { rec.add("tls"); return nil },
		BuildOperatorImage:         func(string) error { rec.add("build"); return nil },
		PushOperatorImage:          func(string) error { rec.add("push"); return nil },
		BuildGatewayProxyImage:     func(string) error { rec.add("build-gateway"); return nil },
		PushGatewayProxyImage:      func(string) error { rec.add("push-gateway"); return nil },
		EnsureNamespace:            func(string) error { rec.add("ensure-ns"); return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-internal")
			return nil
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-gateway-internal")
			return nil
		},
		DeployOperatorManifests: func(*zap.Logger, string, string, []string, string) error { rec.add("deploy-operator"); return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error {
			rec.add("configure-env")
			return nil
		},
		EnsureImagePullSecret: func(string, string, string, string, string) error {
			rec.add("pull-secret")
			return nil
		},
		RestartDeployment:    func(string, string) error { rec.add("restart"); return nil },
		CheckCRDInstalled:    func(string) error { rec.add("check-crd"); return nil },
		GetDeploymentTimeout: func() time.Duration { return time.Second },
		GetRegistryPort:      func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("operator-image")
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			rec.add("gateway-image")
			return "registry.example.com/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/prod",
			Force:    false,
		},
		RegistryManifest:   "config/registry/overlays/tls",
		TLSEnabled:         true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err != nil {
		t.Fatalf("setupPlatformWithDeps returned error: %v", err)
	}

	if !rec.has("tls") {
		t.Fatalf("expected TLS setup to be called")
	}
	if !rec.has("login") {
		t.Fatalf("expected external registry login")
	}
	if rec.has("deploy-registry") || rec.has("registry-info") || rec.has("push-internal") || rec.has("push-gateway-internal") {
		t.Fatalf("did not expect internal registry path")
	}
	if !rec.hasWait("mcp-runtime-operator-controller-manager") {
		t.Fatalf("expected operator wait")
	}
	if rec.hasWait("registry") {
		t.Fatalf("did not expect registry wait with external registry")
	}
}

func TestSetupPlatformWithDeps_DiagnosticsOnRegistryWaitFailure(t *testing.T) {
	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return nil, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry: func(*zap.Logger, string, string, string) error {
			rec.add("login")
			return nil
		},
		DeployRegistry: func(*zap.Logger, string, int, string, string, string) error {
			rec.add("deploy-registry")
			return nil
		},
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error {
			rec.addWait(name)
			if name == "registry" {
				return fmt.Errorf("wait failed")
			}
			return nil
		},
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { return nil },
		BuildOperatorImage:         func(string) error { return nil },
		PushOperatorImage:          func(string) error { return nil },
		BuildGatewayProxyImage:     func(string) error { return nil },
		PushGatewayProxyImage:      func(string) error { return nil },
		EnsureNamespace:            func(string) error { return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			return nil
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
		DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		RestartDeployment:               func(string, string) error { return nil },
		CheckCRDInstalled:               func(string) error { return nil },
		GetDeploymentTimeout:            func() time.Duration { return time.Second },
		GetRegistryPort:                 func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.local/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.local/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest:   "config/registry",
		TLSEnabled:         true,
		TestMode:           true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err == nil {
		t.Fatalf("expected error from registry wait failure")
	}
	if !rec.has("diagnostics") {
		t.Fatalf("expected diagnostics to be printed on wait failure")
	}
}

func TestSetupPlatformWithDeps_DiagnosticsOnOperatorWaitFailure(t *testing.T) {
	setPublicDomainEnv(t)

	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return &config.ExternalRegistryConfig{URL: "registry.example.com"}, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry:   func(*zap.Logger, string, string, string) error { return nil },
		DeployRegistry:  func(*zap.Logger, string, int, string, string, string) error { return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error {
			rec.addWait(name)
			if name == "mcp-runtime-operator-controller-manager" {
				return fmt.Errorf("wait failed")
			}
			return nil
		},
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { return nil },
		BuildOperatorImage:         func(string) error { return nil },
		PushOperatorImage:          func(string) error { return nil },
		BuildGatewayProxyImage:     func(string) error { return nil },
		PushGatewayProxyImage:      func(string) error { return nil },
		EnsureNamespace:            func(string) error { return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			return nil
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
		DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		RestartDeployment:               func(string, string) error { return nil },
		CheckCRDInstalled:               func(string) error { return nil },
		GetDeploymentTimeout:            func() time.Duration { return time.Second },
		GetRegistryPort:                 func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest:   "config/registry/overlays/tls",
		TLSEnabled:         true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err == nil {
		t.Fatalf("expected error from operator wait failure")
	}
	if !rec.has("diagnostics") {
		t.Fatalf("expected diagnostics to be printed on operator wait failure")
	}
	if rec.hasWait("registry") {
		t.Fatalf("did not expect registry wait with external registry")
	}
}

func TestSetupPlatformWithDeps_CRDCheckFailure(t *testing.T) {
	setPublicDomainEnv(t)

	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return &config.ExternalRegistryConfig{URL: "registry.example.com"}, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry:   func(*zap.Logger, string, string, string) error { return nil },
		DeployRegistry:  func(*zap.Logger, string, int, string, string, string) error { return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error {
			rec.addWait(name)
			return nil
		},
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { return nil },
		BuildOperatorImage:         func(string) error { return nil },
		PushOperatorImage:          func(string) error { return nil },
		BuildGatewayProxyImage:     func(string) error { return nil },
		PushGatewayProxyImage:      func(string) error { return nil },
		EnsureNamespace:            func(string) error { return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			return nil
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
		DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		RestartDeployment:               func(string, string) error { return nil },
		CheckCRDInstalled: func(string) error {
			return fmt.Errorf("crd missing")
		},
		GetDeploymentTimeout: func() time.Duration { return time.Second },
		GetRegistryPort:      func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest:   "config/registry/overlays/tls",
		TLSEnabled:         true,
		InstallCertManager: true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err == nil {
		t.Fatalf("expected error from CRD check failure")
	}
	if rec.has("diagnostics") {
		t.Fatalf("did not expect diagnostics on CRD check failure")
	}
	if !rec.hasWait("mcp-runtime-operator-controller-manager") {
		t.Fatalf("expected operator wait before CRD check")
	}
}

func TestSetupPlatformWithDeps_InternalRegistryPushFailure(t *testing.T) {
	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return nil, nil
		},
		ClusterManager:  &fakeClusterManager{rec: rec},
		RegistryManager: &fakeRegistryManager{rec: rec},
		LoginRegistry:   func(*zap.Logger, string, string, string) error { return nil },
		DeployRegistry:  func(*zap.Logger, string, int, string, string, string) error { return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error {
			rec.addWait(name)
			return nil
		},
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { return nil },
		BuildOperatorImage:         func(string) error { rec.add("build"); return nil },
		PushOperatorImage:          func(string) error { rec.add("push"); return nil },
		BuildGatewayProxyImage:     func(string) error { rec.add("build-gateway"); return nil },
		PushGatewayProxyImage:      func(string) error { rec.add("push-gateway"); return nil },
		EnsureNamespace:            func(string) error { rec.add("ensure-ns"); return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.local" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-internal")
			return fmt.Errorf("push failed")
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error {
			rec.add("push-gateway-internal")
			return nil
		},
		DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { rec.add("deploy-operator"); return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		RestartDeployment:               func(string, string) error { return nil },
		CheckCRDInstalled:               func(string) error { return nil },
		GetDeploymentTimeout:            func() time.Duration { return time.Second },
		GetRegistryPort:                 func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.local/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.local/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest: "config/registry",
		TLSEnabled:       false,
		TestMode:         true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err == nil {
		t.Fatalf("expected error from internal registry push failure")
	}
	if rec.has("deploy-operator") {
		t.Fatalf("did not expect operator deploy after push failure")
	}
	if !rec.has("push-internal") {
		t.Fatalf("expected internal push attempt")
	}
}

// TestSetupPlatformWithDeps_RegistryAuthReenabledOnFailure verifies the
// deferred cleanup re-enables the registry ingress auth middleware when a
// pipeline step after the disable step fails. Without the defer, a failure
// here would leave the public registry without `registry-admin-auth@file`.
func TestSetupPlatformWithDeps_RegistryAuthReenabledOnFailure(t *testing.T) {
	origCfg := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origCfg })
	// Non-dev host triggers shouldStageRegistryIngressAuth, so the disable
	// step runs and ctx.RegistryAuthStaged is set to true.
	core.DefaultCLIConfig = &core.CLIConfig{RegistryIngressHost: "registry.prod.example.com"}

	restoreAttempts := 0
	rec := &callRecorder{}
	deps := SetupDeps{
		ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
			return nil, nil
		},
		ClusterManager:             &fakeClusterManager{rec: rec},
		RegistryManager:            &fakeRegistryManager{rec: rec},
		LoginRegistry:              func(*zap.Logger, string, string, string) error { return nil },
		DeployRegistry:             func(*zap.Logger, string, int, string, string, string) error { return nil },
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, _, _ string, _ time.Duration) error { rec.addWait(name); return nil },
		PrintDeploymentDiagnostics: func(string, string, string) { rec.add("diagnostics") },
		SetupTLS:                   func(*zap.Logger, setupplan.Plan) error { return nil },
		BuildOperatorImage:         func(string) error { return nil },
		PushOperatorImage:          func(string) error { return nil },
		BuildGatewayProxyImage:     func(string) error { return nil },
		PushGatewayProxyImage:      func(string) error { return nil },
		EnsureNamespace:            func(string) error { return nil },
		ResolvePlatformRegistryURL: func(*zap.Logger) string { return "registry.prod.example.com" },
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error {
			// Fail after disable runs to exercise the defer cleanup path.
			rec.add("push-internal")
			return fmt.Errorf("push failed")
		},
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
		DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { return nil },
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		DisableRegistryIngressAuth: func() error {
			rec.add("auth-disable")
			return nil
		},
		EnableRegistryIngressAuth: func() error {
			rec.add("auth-enable")
			restoreAttempts++
			if restoreAttempts == 1 {
				return fmt.Errorf("API restarting: %w", syscall.ECONNREFUSED)
			}
			return nil
		},
		RestartDeployment:    func(string, string) error { return nil },
		CheckCRDInstalled:    func(string) error { return nil },
		GetDeploymentTimeout: func() time.Duration { return time.Second },
		GetRegistryPort:      func() int { return 5000 },
		OperatorImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.prod.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
			return "registry.prod.example.com/mcp-gateway:latest"
		},
	}

	plan := setupplan.Plan{
		RegistryType:        "docker",
		RegistryStorageSize: "20Gi",
		Ingress: cluster.IngressOptions{
			Mode:     "traefik",
			Manifest: "config/ingress/overlays/http",
			Force:    false,
		},
		RegistryManifest: "config/registry",
		TLSEnabled:       false,
		TestMode:         true,
	}

	if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err == nil {
		t.Fatalf("expected error from internal registry push failure")
	}
	if !rec.has("auth-disable") {
		t.Fatalf("expected registry auth disable to run, got calls: %v", rec.calls)
	}
	if !rec.has("auth-enable") {
		t.Fatalf("expected registry auth re-enable on failure (defer), got calls: %v", rec.calls)
	}
	if restoreAttempts != 2 {
		t.Fatalf("registry auth restore attempts = %d, want 2", restoreAttempts)
	}
}

// TestSetupPlatformWithDeps_CatalogNamespace verifies setup pre-creates the
// shared catalog namespace (mcp-servers-public / mcp-servers-org) with the
// labels the platform API expects so non-admin users can publish without the
// API SA needing cluster-wide namespace-create RBAC. Tenant mode has no
// shared catalog and must not call ensure.
func TestSetupPlatformWithDeps_CatalogNamespace(t *testing.T) {
	cases := []struct {
		mode       string
		wantNS     string
		wantScope  string
		wantCalled bool
	}{
		{mode: setupplan.PlatformModePublic, wantNS: setupplan.DefaultPublicCatalogNamespace, wantScope: "public", wantCalled: true},
		{mode: setupplan.PlatformModeOrg, wantNS: setupplan.DefaultOrgCatalogNamespace, wantScope: "org", wantCalled: true},
		{mode: setupplan.PlatformModeTenant, wantCalled: false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			var (
				gotNS     string
				gotLabels map[string]string
				calls     int
			)
			deps := SetupDeps{
				ResolveExternalRegistryConfig: func(*config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
					return nil, nil
				},
				ClusterManager:                  &fakeClusterManager{rec: &callRecorder{}},
				RegistryManager:                 &fakeRegistryManager{rec: &callRecorder{}},
				LoginRegistry:                   func(*zap.Logger, string, string, string) error { return nil },
				DeployRegistry:                  func(*zap.Logger, string, int, string, string, string) error { return nil },
				WaitForDeploymentAvailable:      func(*zap.Logger, string, string, string, time.Duration) error { return nil },
				PrintDeploymentDiagnostics:      func(string, string, string) {},
				SetupTLS:                        func(*zap.Logger, setupplan.Plan) error { return nil },
				BuildOperatorImage:              func(string) error { return nil },
				PushOperatorImage:               func(string) error { return nil },
				BuildGatewayProxyImage:          func(string) error { return nil },
				PushGatewayProxyImage:           func(string) error { return nil },
				EnsureNamespace:                 func(string) error { return nil },
				ResolvePlatformRegistryURL:      func(*zap.Logger) string { return "registry.local" },
				PushOperatorImageToInternal:     func(*zap.Logger, string, string, string) error { return nil },
				PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
				DeployOperatorManifests:         func(*zap.Logger, string, string, []string, string) error { return nil },
				ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
				RestartDeployment:               func(string, string) error { return nil },
				CheckCRDInstalled:               func(string) error { return nil },
				GetDeploymentTimeout:            func() time.Duration { return time.Second },
				GetRegistryPort:                 func() int { return 5000 },
				OperatorImageFor: func(*config.ExternalRegistryConfig) string {
					return "registry.local/mcp-runtime-operator:latest"
				},
				GatewayProxyImageFor: func(*config.ExternalRegistryConfig) string {
					return "registry.local/mcp-gateway:latest"
				},
				EnsureCatalogNamespace: func(ns string, labels map[string]string) error {
					calls++
					gotNS = ns
					gotLabels = labels
					return nil
				},
			}

			plan := setupplan.Plan{
				RegistryType:        "docker",
				RegistryStorageSize: "20Gi",
				PlatformMode:        tc.mode,
				Ingress: cluster.IngressOptions{
					Mode:     "traefik",
					Manifest: "config/ingress/overlays/http",
					Force:    false,
				},
				RegistryManifest: "config/registry",
				TestMode:         true,
			}

			if err := setupPlatformWithDeps(zap.NewNop(), plan, deps); err != nil {
				t.Fatalf("setupPlatformWithDeps returned error: %v", err)
			}

			if tc.wantCalled {
				if calls != 1 {
					t.Fatalf("expected EnsureCatalogNamespace to be called exactly once, got %d", calls)
				}
				if gotNS != tc.wantNS {
					t.Fatalf("namespace = %q, want %q", gotNS, tc.wantNS)
				}
				if gotLabels["mcpruntime.org/scope"] != tc.wantScope {
					t.Fatalf("scope label = %q, want %q", gotLabels["mcpruntime.org/scope"], tc.wantScope)
				}
				if gotLabels["platform.mcpruntime.org/managed"] != "true" {
					t.Fatalf("managed label missing or wrong: %q", gotLabels["platform.mcpruntime.org/managed"])
				}
				if gotLabels["pod-security.kubernetes.io/enforce"] != "restricted" {
					t.Fatalf("PSS enforce label missing or wrong: %q", gotLabels["pod-security.kubernetes.io/enforce"])
				}
				if gotLabels["pod-security.kubernetes.io/audit"] != "restricted" {
					t.Fatalf("PSS audit label missing or wrong: %q", gotLabels["pod-security.kubernetes.io/audit"])
				}
				if gotLabels["pod-security.kubernetes.io/warn"] != "restricted" {
					t.Fatalf("PSS warn label missing or wrong: %q", gotLabels["pod-security.kubernetes.io/warn"])
				}
				if gotLabels[core.LabelManagedBy] != core.LabelManagedByValue {
					t.Fatalf("managed-by label missing or wrong: %q", gotLabels[core.LabelManagedBy])
				}
			} else {
				if calls != 0 {
					t.Fatalf("expected EnsureCatalogNamespace not to be called for %s mode, got %d calls", tc.mode, calls)
				}
			}
		})
	}
}
