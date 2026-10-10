package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

// nativeRegistryAuthTimeout bounds broker rollout, credential issuance for all
// managed namespaces, and the registry rollout performed by activation.
const nativeRegistryAuthTimeout = 20 * time.Minute

// nativeRegistryAuthRequired reports whether setup must finish by enabling
// Distribution token authentication on the bundled registry backend (#531).
//
// It applies to production-shaped installs: non-test, bundled registry, TLS,
// the platform stack (the token broker lives in platform-api), and a public
// platform host for the HTTPS token realm. Test mode and plain-HTTP lab
// installs keep an unauthenticated backend protected only by NetworkPolicy;
// setup warns about that through nativeRegistryAuthWarning.
func nativeRegistryAuthRequired(plan setupplan.Plan, usingExternalRegistry bool) bool {
	if plan.TestMode || usingExternalRegistry || !plan.TLSEnabled || !plan.DeployAnalytics {
		return false
	}
	return nativeRegistryRealm() != ""
}

// nativeRegistryRealm returns the public HTTPS token realm, or "" when no
// public platform host is configured.
func nativeRegistryRealm() string {
	if !publicHostEnvConfigured() {
		return ""
	}
	host := strings.TrimSpace(core.GetPlatformIngressHost())
	if host == "" || strings.ContainsAny(host, "/?#@ ") || isDevRegistryURL(host) {
		return ""
	}
	return "https://" + host + "/api/v1/registry/token"
}

// nativeRegistryAuthWarning explains why a non-test bundled install keeps an
// unauthenticated registry backend, or returns "" when none applies.
func nativeRegistryAuthWarning(plan setupplan.Plan, usingExternalRegistry bool) string {
	if plan.TestMode || usingExternalRegistry || nativeRegistryAuthRequired(plan, usingExternalRegistry) {
		return ""
	}
	reason := "it needs --with-tls and a public platform host (MCP_PLATFORM_DOMAIN or MCP_PLATFORM_INGRESS_HOST)"
	if !plan.DeployAnalytics {
		reason = "the platform stack (the token broker) is not installed"
	}
	return fmt.Sprintf("The bundled registry backend will not require authentication because %s. Only the registry NetworkPolicy keeps tenant workloads away from it; use this install for labs only.", reason)
}

type registryNativeAuthStep struct{}

func (s registryNativeAuthStep) Name() string { return "registry-native-auth" }
func (s registryNativeAuthStep) Run(logger *zap.Logger, deps SetupDeps, _ *SetupContext) error {
	core.Step("Step 7: Require registry authentication")
	realm := nativeRegistryRealm()
	core.Info(fmt.Sprintf("Enabling repository-scoped registry authentication (token realm %s)", realm))
	if err := deps.EnableNativeRegistryAuth(logger, realm); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrSetupStepFailed,
			err,
			fmt.Sprintf("enable native registry authentication: %v; fix the cause and rerun setup (activation resumes and never turns authentication off)", err),
			map[string]any{"component": "registry", "realm": realm},
		)
		core.Error("Registry authentication could not be enabled")
		core.LogStructuredError(logger, wrappedErr, "Registry authentication could not be enabled")
		return wrappedErr
	}
	return nil
}

// enableNativeRegistryAuthClientGo activates native registry authentication
// with a platform administrator key held by the cluster.
func enableNativeRegistryAuthClientGo(_ *zap.Logger, realm string) error {
	apiKey, err := setupRegistryAdminAPIKey()
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(realm, "/api/v1/registry/token")
	ctx, cancel := context.WithTimeout(context.Background(), nativeRegistryAuthTimeout)
	defer cancel()
	return registry.EnableNativeAuthForSetup(ctx, registry.SetupNativeAuthOptions{Realm: realm, APIBaseURL: base, APIKey: apiKey})
}

func setupRegistryAdminAPIKey() (string, error) {
	namespace, name, err := ownedCredential("ADMIN_API_KEYS")
	if err != nil {
		return "", err
	}
	value, err := existingSecretDataValueClientGo(namespace, name, "ADMIN_API_KEYS")
	if err != nil {
		return "", fmt.Errorf("read platform administrator keys: %w", err)
	}
	key := firstCSVValue(value)
	if key == "" {
		return "", fmt.Errorf("platform administrator keys are empty in %s/%s", namespace, name)
	}
	return key, nil
}

func firstCSVValue(value string) string {
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			return part
		}
	}
	return ""
}

// nativeRegistryPullSecret reports whether the namespace pull Secret holds a
// scoped node credential issued by native registry authentication.
func nativeRegistryPullSecret(ctx context.Context, clients *k8sclient.Clients, namespace string) bool {
	if clients == nil || clients.Clientset == nil {
		return false
	}
	secret, err := clients.Clientset.CoreV1().Secrets(namespace).Get(ctx, defaultRegistrySecretName, metav1.GetOptions{})
	return err == nil && secret.Labels["mcpruntime.org/registry-auth"] == "pull"
}
