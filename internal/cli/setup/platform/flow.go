package platform

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/platforminventory"
)

// printPlatformEntrypoints prints the public URLs derived from
// MCP_PLATFORM_DOMAIN / MCP_*_INGRESS_HOST so the operator knows which
// hostnames must resolve in DNS and what the dashboard URL is.
func printPlatformEntrypoints(tlsEnabled bool) {
	scheme := "http://"
	if tlsEnabled {
		scheme = "https://"
	}
	registry := strings.TrimSpace(core.GetRegistryIngressHost())
	mcp := strings.TrimSpace(core.GetMcpIngressHost())
	platform := strings.TrimSpace(core.GetPlatformIngressHost())
	if registry == "" && mcp == "" && platform == "" {
		return
	}
	fmt.Println()
	fmt.Println("Public entrypoints:")
	if platform != "" {
		fmt.Printf("  Dashboard:  %s%s%s/\n", scheme, platform, core.DefaultCLIConfig.PublicRoutes.PlatformPrefix())
	}
	if registry != "" {
		fmt.Printf("  Registry:   %s%s/v2/\n", scheme, registry)
	}
	if mcp != "" {
		fmt.Printf("  MCP:        %s%s/<server-name>/mcp\n", scheme, mcp)
	}
	if platform != "" {
		fmt.Println("  (Point DNS A/AAAA records for these configured hostnames at the cluster ingress.)")
	}
}

func resolveRegistrySetup(logger *zap.Logger, plan setupplan.Plan, deps SetupDeps) (*config.ExternalRegistryConfig, bool, string, error) {
	mode, _ := setupplan.NormalizeRegistryMode(plan.RegistryMode)
	flagCfg := externalRegistryFlagConfig(plan)
	if (mode == setupplan.RegistryModeBundledHTTP || mode == setupplan.RegistryModeBundledHTTPS) && flagCfg != nil {
		return nil, false, defaultRegistrySecretName, core.NewWithBase(
			core.ErrRegistryURLRequired,
			"--external-registry-* flags require --registry-mode external or --registry-mode auto",
		)
	}
	if mode == setupplan.RegistryModeBundledHTTP || mode == setupplan.RegistryModeBundledHTTPS {
		return nil, false, defaultRegistrySecretName, nil
	}

	extRegistry, err := deps.ResolveExternalRegistryConfig(flagCfg)
	if err != nil {
		if mode == setupplan.RegistryModeExternal || flagCfg != nil {
			return nil, false, defaultRegistrySecretName, err
		}
		core.Warn(fmt.Sprintf("Could not load external registry config: %v", err))
	}
	if (mode == setupplan.RegistryModeExternal || flagCfg != nil) && (extRegistry == nil || strings.TrimSpace(extRegistry.URL) == "") {
		return nil, false, defaultRegistrySecretName, core.NewWithBase(
			core.ErrRegistryURLRequired,
			"external registry url is required (use --external-registry-url, PROVISIONED_REGISTRY_URL, or mcp-runtime registry provision)",
		)
	}
	usingExternalRegistry := extRegistry != nil && strings.TrimSpace(extRegistry.URL) != ""
	return extRegistry, usingExternalRegistry, defaultRegistrySecretName, nil
}

func externalRegistryFlagConfig(plan setupplan.Plan) *config.ExternalRegistryConfig {
	if strings.TrimSpace(plan.ExternalRegistryURL) == "" && plan.ExternalRegistryUser == "" && plan.ExternalRegistryPass == "" {
		return nil
	}
	return &config.ExternalRegistryConfig{
		URL:      strings.TrimSpace(plan.ExternalRegistryURL),
		Username: plan.ExternalRegistryUser,
		Password: plan.ExternalRegistryPass,
	}
}

func validateNonTestSetup(plan setupplan.Plan, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry bool) error {
	return validateNonTestSetupWithAuthConfig(plan, extRegistry, usingExternalRegistry, nil)
}

func validateNonTestSetupWithAuthConfig(plan setupplan.Plan, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry bool, existingAuthConfig map[string]string) error {
	if plan.TestMode {
		return nil
	}
	if err := validateRequiredPlatformEnv(plan, usingExternalRegistry, existingAuthConfig); err != nil {
		return err
	}
	if !plan.StrictProd {
		return nil
	}
	if !plan.TLSEnabled {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			"strict production setup requires --with-tls; use normal setup for local HTTP/internal registry flows",
		)
	}
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTP {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			"strict production setup requires --registry-mode bundled-https or --registry-mode external; bundled-http is for local/dev clusters",
		)
	}
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		return nil
	}
	if usingExternalRegistry && extRegistry != nil && strings.TrimSpace(extRegistry.URL) != "" {
		if isDevRegistryURL(extRegistry.URL) {
			return core.NewWithBase(
				core.ErrSetupStepFailed,
				fmt.Sprintf("strict production setup requires a stable production registry, got dev-only registry URL %q", extRegistry.URL),
			)
		}
		return nil
	}
	if plan.RegistryMode == setupplan.RegistryModeAuto {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			"strict production setup with the bundled registry requires --registry-mode bundled-https; use --registry-mode external for a provisioned registry",
		)
	}
	if isDevRegistryURL(core.GetRegistryEndpoint()) {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			fmt.Sprintf("strict production setup requires a stable internal registry endpoint; set MCP_REGISTRY_ENDPOINT (current %q)", core.GetRegistryEndpoint()),
		)
	}
	return nil
}

func validateRequiredPlatformEnv(plan setupplan.Plan, usingExternalRegistry bool, existingAuthConfig map[string]string) error {
	if !platformEnvValidationRequired(plan) {
		return nil
	}
	if missing := missingPublicHostEnv(); len(missing) > 0 {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			fmt.Sprintf(
				"platform host configuration is incomplete; set MCP_PLATFORM_DOMAIN or set %s before running setup",
				strings.Join(missing, ", "),
			),
		)
	}
	if !platformAdminEnvConfigured() {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			"platform admin configuration is incomplete; set MCP_PLATFORM_ADMIN_EMAIL (or ADMIN_USERS) before running production setup",
		)
	}
	if err := ValidatePublicPlatformAuthConfig(plan.PlatformMode, plan.TLSEnabled, plan.TestMode, existingAuthConfig); err != nil {
		return err
	}
	if !usingExternalRegistry && plan.RegistryMode == setupplan.RegistryModeAuto && !registryEndpointEnvExplicitlyConfigured() {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			"bundled registry setup with --registry-mode auto requires MCP_REGISTRY_ENDPOINT set to the exact registry host:port Kubernetes nodes can pull from; use --registry-mode bundled-http, bundled-https, or external when the platform should discover the internal registry Service automatically",
		)
	}
	return nil
}

func existingPublicAuthConfigForSetup(plan setupplan.Plan) (map[string]string, error) {
	if !publicPlatformAuthConfigRequired(plan.PlatformMode, plan.TLSEnabled, plan.TestMode) {
		return nil, nil
	}
	if publicBrowserLoginConfigConfigured(nil) {
		return nil, nil
	}
	return existingConfigMapDataClientGo(core.ComponentNamespace("platform-api"), platforminventory.SharedConfigName)
}

func platformAdminEnvConfigured() bool {
	return setupSecretEnvValue("MCP_PLATFORM_ADMIN_EMAIL", "PLATFORM_ADMIN_EMAIL", "MCP_ADMIN_USERS", "ADMIN_USERS") != ""
}

func platformEnvValidationRequired(plan setupplan.Plan) bool {
	return plan.TLSEnabled || publicHostEnvConfigured()
}

func publicHostEnvConfigured() bool {
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

func missingPublicHostEnv() []string {
	if strings.TrimSpace(os.Getenv("MCP_PLATFORM_DOMAIN")) != "" {
		return nil
	}
	var missing []string
	for _, key := range []string{
		"MCP_PLATFORM_INGRESS_HOST",
		"MCP_REGISTRY_INGRESS_HOST",
		"MCP_MCP_INGRESS_HOST",
	} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func setupWarnings(plan setupplan.Plan, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry bool) []string {
	if plan.TestMode {
		return nil
	}

	var warnings []string
	if !plan.TLSEnabled {
		warnings = append(warnings, "Non-test setup is running without TLS. This is fine for local/internal registries but not recommended for production.")
	}
	switch plan.RegistryMode {
	case setupplan.RegistryModeBundledHTTP:
		warnings = append(warnings, "Registry mode bundled-http uses the bundled registry over plain HTTP for in-cluster platform image pulls. Each Kubernetes node must trust that exact image host as an insecure registry.")
	case setupplan.RegistryModeBundledHTTPS:
		warnings = append(warnings, "Registry mode bundled-https serves the bundled registry over HTTPS. Each Kubernetes node must trust the issuing CA and be able to resolve or mirror the rendered registry image host.")
	}

	if usingExternalRegistry && extRegistry != nil && strings.TrimSpace(extRegistry.URL) != "" {
		registryURL := strings.TrimSpace(extRegistry.URL)
		if strings.HasPrefix(strings.ToLower(registryURL), "http://") {
			warnings = append(warnings, fmt.Sprintf("External registry %q is using HTTP. This is acceptable for local environments but not recommended for production.", registryURL))
		}
		if isDevRegistryURL(registryURL) {
			warnings = append(warnings, fmt.Sprintf("External registry %q looks local/internal. Normal setup allows this, but use --strict-prod to enforce production-style validation.", registryURL))
		}
		return warnings
	}
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		return warnings
	}

	registryEndpoint := strings.TrimSpace(core.GetRegistryEndpoint())
	if registryEndpoint == "" {
		warnings = append(warnings, "Internal registry host is empty; setup will fall back to service DNS. This is fine for local clusters but not recommended for production.")
		return warnings
	}
	if isDevRegistryURL(registryEndpoint) {
		warnings = append(warnings, fmt.Sprintf("Internal registry endpoint %q looks local/internal. Normal setup allows this for local clusters, but use --strict-prod to enforce production-style validation.", registryEndpoint))
	}
	return warnings
}

func isDevRegistryURL(raw string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "http://") {
		return true
	}

	host := trimmed
	if strings.Contains(trimmed, "://") {
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
			host = parsed.Host
		}
	}
	if slash := strings.Index(host, "/"); slash >= 0 {
		host = host[:slash]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if idx := strings.LastIndex(host, ":"); idx >= 0 && strings.Count(host, ":") == 1 {
		host = host[:idx]
	}

	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "", "localhost", "registry.local":
		return true
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".svc.cluster.local") {
		return true
	}
	return net.ParseIP(host) != nil
}
