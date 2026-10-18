package platform

import (
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/ingressmanifest"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"net/url"
	"os"
	"strings"
	"time"
)

// validateTLSSetupCLIFlags enforces ACME / internal-issuer mutual exclusion and
// requires --with-tls when any TLS or cert-manager-related options are set.
func ValidateTLSSetupCLIFlags(
	tlsEnabled bool,
	providedTLSSecrets bool, acmeEmailResolved, tlsCIResolved string,
	acmeStagingResolved, skipCertManagerInstall bool,
) error {
	if providedTLSSecrets && (acmeEmailResolved != "" || tlsCIResolved != "") {
		return core.NewWithBase(core.ErrFieldRequired, "--provided-tls-secrets cannot be combined with --acme-email or --tls-cluster-issuer; import enterprise TLS Secrets before setup instead")
	}
	if acmeEmailResolved != "" && tlsCIResolved != "" {
		return core.NewWithBase(core.ErrFieldRequired, "use either --acme-email (or MCP_ACME_EMAIL) for public Let's Encrypt, or --tls-cluster-issuer (or MCP_TLS_CLUSTER_ISSUER) for an existing internal ClusterIssuer, not both")
	}
	if !tlsEnabled && (providedTLSSecrets || tlsCIResolved != "" || acmeEmailResolved != "" || acmeStagingResolved || skipCertManagerInstall) {
		return core.NewWithBase(core.ErrFieldRequired, "--with-tls is required when using --provided-tls-secrets, --acme-email, --tls-cluster-issuer, --acme-staging, --skip-cert-manager-install, or related environment variables")
	}
	return nil
}

// ValidateMTLSSetupCLIFlags enforces the mTLS setup flag contract. The mTLS auth
// path is enabled by naming a workload issuer with --mtls-cluster-issuer (test
// mode defaults it to the bundled mcp-runtime-ca). It requires --with-tls because
// Traefik terminates the caller's mTLS on the websecure (TLS) entrypoint; test
// mode is exempt (it serves the bundled self-signed default certificate there).
func ValidateMTLSSetupCLIFlags(testMode, tlsEnabled bool, mtlsClusterIssuer string) error {
	if !testMode && strings.TrimSpace(mtlsClusterIssuer) == setupplan.DefaultTestMTLSClusterIssuer {
		return core.NewWithBase(core.ErrFieldRequired, "the shared bundled mcp-runtime-ca is test-only for workload identity. Provision a dedicated enterprise-managed workload ClusterIssuer with a separate signing root, then use --with-tls --mtls-cluster-issuer=<issuer>; preserve the existing registry CA. See docs/cli-reference.md (Bundled workload CA lifecycle)")
	}
	if strings.TrimSpace(mtlsClusterIssuer) != "" && !tlsEnabled && !testMode {
		return core.NewWithBase(core.ErrFieldRequired, "--mtls-cluster-issuer requires --with-tls: mTLS terminates at the ingress on the websecure (TLS) entrypoint")
	}
	return nil
}

func setupTLSStep(logger *zap.Logger, plan setupplan.Plan, deps SetupDeps) error {
	// Validate before registry TLS can create the bundled root. Otherwise a
	// missing production workload root could be silently replaced by that step.
	if err := validateProductionWorkloadIssuer(plan, time.Now()); err != nil {
		return err
	}
	// Step 3: Configure TLS (if enabled)
	core.Step("Step 3: Configure TLS")
	if !plan.TLSEnabled && (plan.TestMode || !plan.DeployAnalytics) {
		core.Info("Skipped (TLS disabled, use --with-tls to enable)")
		return nil
	}
	if err := deps.SetupTLS(logger, plan); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, fmt.Sprintf("TLS setup failed: %v", err))
		core.Error("TLS setup failed")
		core.LogStructuredError(logger, wrappedErr, "TLS setup failed")
		return wrappedErr
	}
	core.Success("TLS configured successfully")
	return nil
}

func setupWorkloadPKI(logger *zap.Logger, plan setupplan.Plan) error {
	issuer := strings.TrimSpace(plan.MTLSClusterIssuer)
	if issuer == "" {
		return nil
	}
	if err := validateProductionWorkloadIssuer(plan, time.Now()); err != nil {
		return err
	}

	core.Step("Configure workload mTLS PKI")
	if plan.InstallCertManager {
		if err := ensureCertManagerInstalledClientGo(logger); err != nil {
			return err
		}
	} else if err := checkCertManagerInstalledClientGo(); err != nil {
		return core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "workload mTLS requires cert-manager; install it or omit --skip-cert-manager-install")
	}

	// Only test mode provisions the shared bundled root. Production requires a
	// dedicated, already-ready enterprise workload issuer checked above.
	if issuer == setupplan.DefaultTestMTLSClusterIssuer {
		if err := ensureManagedWorkloadCA(plan, time.Now()); err != nil {
			return err
		}
		// Reusing an already-ready managed issuer is important on long-lived
		// contributor clusters: client-go's generic merge can reject an
		// otherwise harmless re-apply of cert-manager's ClusterIssuer.
		if err := checkNamedClusterIssuerClientGo(issuer); err != nil {
			if applyErr := applyManifestFile("config/cert-manager/cluster-issuer.yaml", "", os.Stdout); applyErr != nil {
				return core.WrapWithBase(core.ErrClusterIssuerApplyFailed, applyErr, "apply managed workload ClusterIssuer")
			}
		}
	} else if err := checkNamedClusterIssuerClientGo(issuer); err != nil {
		return err
	}

	core.Success("Workload mTLS issuer ready: " + issuer)
	return nil
}

// setupTLSWithKubectlAndPlan provisions TLS: Let's Encrypt when plan.ACMEmail is set, an existing
// ClusterIssuer when plan.TLSClusterIssuer is set, otherwise the bundled private CA (mcp-runtime-ca).
//
//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func setupTLSWithKubectlAndPlan(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	if plan.ProvidedTLSSecrets {
		return setupTLSProvidedSecretsWithKubectl(kubectl, plan)
	}
	if strings.TrimSpace(plan.ACMEmail) != "" {
		return setupTLSLetsEncrypt(kubectl, logger, plan)
	}
	if strings.TrimSpace(plan.TLSClusterIssuer) != "" {
		return setupTLSWithExistingClusterIssuer(kubectl, logger, plan)
	}
	return setupTLSPrivateCA(kubectl, logger, plan)
}

func setupTLSWithClientGoAndPlan(logger *zap.Logger, plan setupplan.Plan) error {
	if err := setupPublicTLSWithClientGoAndPlan(logger, plan); err != nil {
		return err
	}
	return setupAnalyticsTransportTLS(logger, plan)
}

func setupPublicTLSWithClientGoAndPlan(logger *zap.Logger, plan setupplan.Plan) error {
	if !plan.TLSEnabled {
		return nil
	}
	if plan.ProvidedTLSSecrets {
		return setupTLSProvidedSecrets(plan)
	}
	if strings.TrimSpace(plan.ACMEmail) != "" {
		return setupTLSLetsEncryptClientGo(logger, plan)
	}
	if strings.TrimSpace(plan.TLSClusterIssuer) != "" {
		return setupTLSWithExistingClusterIssuerClientGo(logger, plan)
	}
	return setupTLSPrivateCAClientGo(logger, plan)
}

// setupTLSProvidedSecrets verifies operator-managed TLS Secrets. It deliberately
// does not create Certificate resources: enterprise IT owns issuance and rotation.
func setupTLSProvidedSecrets(plan setupplan.Plan) error {
	return setupTLSProvidedSecretsWithKubectl(core.DefaultKubectlClient(), plan)
}

func setupTLSProvidedSecretsWithKubectl(kubectl core.KubectlRunner, plan setupplan.Plan) error {
	checks := []struct{ namespace, name, purpose string }{}
	if plan.RegistryMode != setupplan.RegistryModeExternal {
		checks = append(checks, struct{ namespace, name, purpose string }{core.NamespaceRegistry, certmanager.RegistryTLSSecretName, "registry"})
	}
	if strings.TrimSpace(core.GetPlatformIngressHost()) != "" {
		checks = append(checks, struct{ namespace, name, purpose string }{core.ComponentNamespace("gateway"), ingressmanifest.PlatformTLSSecretName, "platform"})
	}
	if plan.DeployMCPAuthServer && !plan.TestMode {
		checks = append(checks, struct{ namespace, name, purpose string }{core.ComponentNamespace("mcp-auth"), plan.MCPAuthTLSSecret, "mcp-auth"})
	}
	for _, check := range checks {
		if err := checkTLSSecretWithKubectl(kubectl, check.namespace, check.name, check.purpose); err != nil {
			return err
		}
	}
	core.Success("Using operator-provided TLS Secrets; cert-manager will not manage their renewal")
	return nil
}

func checkTLSSecretWithKubectl(kubectl core.KubectlRunner, namespace, name, purpose string) error {
	for _, key := range []string{"tls.crt", "tls.key"} {
		keyPath := strings.ReplaceAll(key, ".", `\.`)
		cmd, err := kubectl.CommandArgs([]string{"get", "secret", name, "-n", namespace, "-o", "jsonpath={.data." + keyPath + "}"})
		if err != nil {
			return fmt.Errorf("prepare %s TLS Secret check: %w", purpose, err)
		}
		value, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(value)) == "" {
			return fmt.Errorf("%s TLS Secret %q in namespace %q must exist with non-empty tls.crt and tls.key; create it with `kubectl -n %s create secret tls %s --cert=fullchain.pem --key=privkey.pem` before setup", purpose, name, namespace, namespace, name)
		}
	}
	return nil
}

func mcpAuthTLSHost(plan setupplan.Plan) (string, error) {
	if !plan.DeployMCPAuthServer || plan.TestMode {
		return "", nil
	}
	parsed, err := url.Parse(strings.TrimSpace(plan.MCPAuthIssuerURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("mcp-auth issuer URL %q must be an absolute HTTPS URL with a certificate hostname", plan.MCPAuthIssuerURL)
	}
	if parsed.Port() != "" {
		return "", fmt.Errorf("mcp-auth issuer URL %q must not include a port because Kubernetes Ingress hosts do not accept ports", plan.MCPAuthIssuerURL)
	}
	return parsed.Hostname(), nil
}
