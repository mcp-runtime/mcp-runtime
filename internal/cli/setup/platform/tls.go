package platform

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/setup/ingressmanifest"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// validateTLSSetupCLIFlags enforces ACME / internal-issuer mutual exclusion and
// requires --with-tls when any TLS or cert-manager-related options are set.
func ValidateTLSSetupCLIFlags(
	tlsEnabled bool,
	providedTLSSecrets bool, acmeEmailResolved, tlsCIResolved string,
	acmeStagingResolved, skipCertManagerInstall bool,
) error {
	if providedTLSSecrets && (acmeEmailResolved != "" || tlsCIResolved != "") {
		return core.NewWithSentinel(core.ErrFieldRequired, "--provided-tls-secrets cannot be combined with --acme-email or --tls-cluster-issuer; import enterprise TLS Secrets before setup instead")
	}
	if acmeEmailResolved != "" && tlsCIResolved != "" {
		return core.NewWithSentinel(core.ErrFieldRequired, "use either --acme-email (or MCP_ACME_EMAIL) for public Let's Encrypt, or --tls-cluster-issuer (or MCP_TLS_CLUSTER_ISSUER) for an existing internal ClusterIssuer, not both")
	}
	if !tlsEnabled && (providedTLSSecrets || tlsCIResolved != "" || acmeEmailResolved != "" || acmeStagingResolved || skipCertManagerInstall) {
		return core.NewWithSentinel(core.ErrFieldRequired, "--with-tls is required when using --provided-tls-secrets, --acme-email, --tls-cluster-issuer, --acme-staging, --skip-cert-manager-install, or related environment variables")
	}
	return nil
}

// ValidateMTLSSetupCLIFlags enforces the mTLS setup flag contract. The mTLS auth
// path is enabled by naming a workload issuer with --mtls-cluster-issuer (test
// mode defaults it to the bundled mcp-runtime-ca). It requires --with-tls because
// Traefik terminates the caller's mTLS on the websecure (TLS) entrypoint; test
// mode is exempt (it serves the bundled self-signed default certificate there).
func ValidateMTLSSetupCLIFlags(testMode, tlsEnabled bool, mtlsClusterIssuer string) error {
	if strings.TrimSpace(mtlsClusterIssuer) != "" && !tlsEnabled && !testMode {
		return core.NewWithSentinel(core.ErrFieldRequired, "--mtls-cluster-issuer requires --with-tls: mTLS terminates at the ingress on the websecure (TLS) entrypoint")
	}
	return nil
}

func setupTLSStep(logger *zap.Logger, plan setupplan.Plan, deps SetupDeps) error {
	// Step 3: Configure TLS (if enabled)
	core.Step("Step 3: Configure TLS")
	if !plan.TLSEnabled {
		core.Info("Skipped (TLS disabled, use --with-tls to enable)")
		return nil
	}
	if err := deps.SetupTLS(logger, plan); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, fmt.Sprintf("TLS setup failed: %v", err))
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

	core.Step("Configure workload mTLS PKI")
	if plan.InstallCertManager {
		if err := ensureCertManagerInstalledClientGo(logger); err != nil {
			return err
		}
	} else if err := checkCertManagerInstalledClientGo(); err != nil {
		return core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "workload mTLS requires cert-manager; install it or omit --skip-cert-manager-install")
	}

	// Managed mode: provision the bundled mcp-runtime-ca workload issuer whenever
	// it is the selected issuer (test mode defaults to it; prod can name it
	// explicitly). Otherwise the issuer is enterprise-managed and must exist.
	if issuer == setupplan.DefaultTestMTLSClusterIssuer {
		if err := ensureManagedWorkloadCA(plan, time.Now()); err != nil {
			return err
		}
		// Reusing an already-ready managed issuer is important on long-lived
		// contributor clusters: client-go's generic merge can reject an
		// otherwise harmless re-apply of cert-manager's ClusterIssuer.
		if err := checkNamedClusterIssuerClientGo(issuer); err != nil {
			if applyErr := applyManifestFile("config/cert-manager/cluster-issuer.yaml", "", os.Stdout); applyErr != nil {
				return core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, applyErr, "apply managed workload ClusterIssuer")
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

func setupTLSLetsEncryptClientGo(logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Configuring TLS with Let's Encrypt (cert-manager HTTP-01)")
	if err := certmanager.ValidateACMEHostnameForPublicCA(); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid configuration for Let's Encrypt")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid configuration for Let's Encrypt")
		}
		return wrappedErr
	}
	authHost, err := mcpAuthTLSHost(plan)
	if err != nil {
		return err
	}
	if authHost != "" {
		if err := certmanager.ValidateACMEHostnamesForPublicCA(authHost); err != nil {
			return err
		}
	}
	if err := certmanager.ValidateIngressManifestForACME(plan.Ingress.Manifest); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Ingress configuration blocks Let's Encrypt")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Ingress configuration blocks Let's Encrypt")
		}
		return wrappedErr
	}
	if plan.InstallCertManager {
		if err := ensureCertManagerInstalledClientGo(logger); err != nil {
			return err
		}
	} else if err := checkCertManagerInstalledClientGo(); err != nil {
		err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
		core.Error("Cert-manager not installed")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cert-manager not installed")
		}
		return err
	}
	if err := waitForTraefikDeploymentForACMEClientGo(); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Traefik is not ready for HTTP-01")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Traefik is not ready for HTTP-01")
		}
		return wrappedErr
	}
	core.Info("Checking TCP connectivity to your ACME hostnames on port 80 (best effort from this machine)")
	acmeHosts := certmanager.ACMETLSDNSNames()
	if authHost != "" {
		acmeHosts = append(acmeHosts, authHost)
	}
	certmanager.PreflightACMEHostnamesPort80(acmeHosts)

	name := certmanager.ClusterIssuerNameForACME(plan.ACMEStaging)
	manifest := certmanager.RenderLetsEncryptClusterIssuerManifest(name, plan.ACMEmail, acmeServerURLForClientGo(plan.ACMEStaging))
	core.Info("Applying Let's Encrypt ClusterIssuer")
	if err := applyManifestYAML(manifest, "", os.Stdout); err != nil {
		return err
	}

	if err := ensureNamespaceWithLabels(core.NamespaceRegistry, nil); err != nil {
		return wrapRegistryNamespaceError(err, logger)
	}
	// Existing registries already deny ingress by default. Permit only Traefik
	// to reach HTTP-01 solver pods before requesting or renewing certificates.
	if err := applyManifestFile("config/registry/base/acme-networkpolicy.yaml", "", os.Stdout); err != nil {
		return fmt.Errorf("allow registry ACME HTTP-01 solver traffic: %w", err)
	}
	if err := ensureRegistryCertificateOwnershipClientGo(logger); err != nil {
		return err
	}

	dnsNames := certmanager.ACMETLSDNSNames()
	core.Info("Applying Certificate for registry (Let's Encrypt SANs)")
	if err := applyRegistryCertificateClientGo(certmanager.RegistryCertificateName, certmanager.RegistryTLSSecretName, dnsNames, nil, name); err != nil {
		return wrapApplyCertificateError(err, logger, certmanager.RegistryCertificateName)
	}
	certTimeout := core.GetCertTimeout()
	if certTimeout < 5*time.Minute {
		certTimeout = 5 * time.Minute
	}
	if err := waitForCertificateReadyClientGo(certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout, logger, "certificate"); err != nil {
		return err
	}
	if err := setupMCPAuthTLSClientGo(logger, plan, name); err != nil {
		return err
	}
	core.Success("Certificate issued successfully")
	return setupBundledRegistryInternalTLSClientGo(logger, plan)
}

func setupTLSWithExistingClusterIssuerClientGo(logger *zap.Logger, plan setupplan.Plan) error {
	issuerName := strings.TrimSpace(plan.TLSClusterIssuer)
	core.Info("Configuring TLS with existing ClusterIssuer: " + issuerName)
	if plan.InstallCertManager {
		if err := ensureCertManagerInstalledClientGo(logger); err != nil {
			return err
		}
	} else if err := checkCertManagerInstalledClientGo(); err != nil {
		err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
		core.Error("Cert-manager not installed")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cert-manager not installed")
		}
		return err
	}
	if err := checkNamedClusterIssuerClientGo(issuerName); err != nil {
		core.Error("Cluster issuer not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cluster issuer not found")
		}
		return err
	}
	if err := ensureNamespaceWithLabels(core.NamespaceRegistry, nil); err != nil {
		return wrapRegistryNamespaceError(err, logger)
	}
	if err := applyManifestFile("config/registry/base/acme-networkpolicy.yaml", "", os.Stdout); err != nil {
		return fmt.Errorf("allow registry ACME HTTP-01 solver traffic: %w", err)
	}
	if err := ensureRegistryCertificateOwnershipClientGo(logger); err != nil {
		return err
	}
	dnsNames, ipAddresses := registryCertificateSANs(plan)
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		err := core.NewWithSentinel(core.ErrSetupTLSCertificateSANsEmpty, "no DNS names or IP addresses resolved for the Certificate; set MCP_PLATFORM_DOMAIN, MCP_REGISTRY_HOST, or MCP_REGISTRY_INGRESS_HOST (and optional MCP_MCP_INGRESS_HOST)")
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid TLS host configuration")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid TLS host configuration")
		}
		return wrappedErr
	}
	core.Info("Applying Certificate for registry (custom ClusterIssuer)")
	if err := applyRegistryCertificateClientGo(certmanager.RegistryCertificateName, certmanager.RegistryTLSSecretName, dnsNames, ipAddresses, issuerName); err != nil {
		return wrapApplyCertificateError(err, logger, certmanager.RegistryCertificateName)
	}
	certTimeout := core.GetCertTimeout()
	if certTimeout < 5*time.Minute {
		certTimeout = 5 * time.Minute
	}
	if err := waitForCertificateReadyClientGo(certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout, logger, "certificate"); err != nil {
		return err
	}
	if err := setupMCPAuthTLSClientGo(logger, plan, issuerName); err != nil {
		return err
	}
	core.Success("Certificate issued successfully")
	return setupBundledRegistryInternalTLSClientGo(logger, plan)
}

func setupTLSPrivateCAClientGo(logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Checking cert-manager installation")
	if err := checkCertManagerInstalledClientGo(); err != nil {
		err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it first:\n  helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace --set crds.enabled=true\n  or run setup with --with-tls --acme-email <addr> to install cert-manager automatically")
		core.Error("Cert-manager not installed")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cert-manager not installed")
		}
		return err
	}
	core.Info("cert-manager CRDs found")

	core.Info("Checking CA secret")
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		created, err := ensureCASecretClientGo()
		if err != nil {
			err := core.WrapWithSentinel(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' could not be generated in cert-manager namespace. Create a private CA manually:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
			core.Error("CA secret unavailable")
			if logger != nil {
				core.LogStructuredError(logger, err, "CA secret unavailable")
			}
			return err
		}
		if created {
			core.Info("Generated cert-manager/mcp-runtime-ca for bundled HTTPS registry TLS; configure every Kubernetes node to trust its tls.crt before pulling from the bundled HTTPS registry")
		}
	} else if err := checkCASecretClientGo(); err != nil {
		err := core.WrapWithSentinel(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' not found in cert-manager namespace. For Let's Encrypt use --acme-email, or create a private CA:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
		core.Error("CA secret not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "CA secret not found")
		}
		return err
	}
	core.Info("CA secret found")

	core.Info("Applying ClusterIssuer")
	if err := applyManifestFile("config/cert-manager/cluster-issuer.yaml", "", os.Stdout); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply ClusterIssuer: %v", err))
		core.Error("Failed to apply ClusterIssuer")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply ClusterIssuer")
		}
		return wrappedErr
	}
	if err := ensureNamespaceWithLabels(core.NamespaceRegistry, nil); err != nil {
		return wrapRegistryNamespaceError(err, logger)
	}
	if err := ensureRegistryCertificateOwnershipClientGo(logger); err != nil {
		return err
	}
	core.Info("Applying Certificate for registry")
	var certErr error
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		dnsNames, ipAddresses := registryCertificateSANs(plan)
		certErr = applyRegistryCertificateClientGo(certmanager.RegistryCertificateName, certmanager.RegistryTLSSecretName, dnsNames, ipAddresses, certmanager.CertClusterIssuerName)
	} else {
		certErr = applyRegistryCertificateFromTemplateClientGo()
	}
	if certErr != nil {
		return wrapApplyCertificateError(certErr, logger, certmanager.RegistryCertificateName)
	}
	certTimeout := core.GetCertTimeout()
	if err := waitForCertificateReadyClientGo(certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout, logger, "certificate"); err != nil {
		return err
	}
	if err := setupMCPAuthTLSClientGo(logger, plan, certmanager.CertClusterIssuerName); err != nil {
		return err
	}
	core.Success("Certificate issued successfully")
	return setupBundledRegistryInternalTLSClientGo(logger, plan)
}

func acmeServerURLForClientGo(staging bool) string {
	if staging {
		return "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	return "https://acme-v02.api.letsencrypt.org/directory"
}

func ensureCertManagerInstalledClientGo(logger *zap.Logger) error {
	if err := checkCertManagerInstalledClientGo(); err == nil {
		// CRDs exist but pods may not be running (e.g. after a k3s restart).
		// Verify deployments are available; if they aren't, fall through to reinstall.
		if podErr := waitForCertManagerDeploymentsClientGo(logger, 2*time.Minute, false); podErr == nil {
			core.Info("cert-manager already installed")
			return nil
		}
		core.Warn("cert-manager CRDs present but deployments not ready — reinstalling")
	}
	core.Info("Installing cert-manager v1.16.2")
	warnMsg := "If this fails (no network), install cert-manager manually, then re-run setup with --skip-cert-manager-install"
	core.Warn(warnMsg)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(certmanager.CertManagerInstallManifestURL()) // #nosec G107 -- fixed cert-manager release URL.
	if err != nil {
		wrapped := core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager install failed: %v. %s", err, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "cert-manager install failed")
		}
		return wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := core.NewWithSentinel(core.ErrCertManagerInstallFailed, fmt.Sprintf("cert-manager install manifest download failed: HTTP %d. %s", resp.StatusCode, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, err, "cert-manager install failed")
		}
		return err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("read cert-manager install manifest: %v", err))
	}
	if err := applyManifestYAML(string(body), "", os.Stdout); err != nil {
		wrapped := core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager install failed: %v. %s", err, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "cert-manager install failed")
		}
		return wrapped
	}
	return waitForCertManagerDeploymentsClientGo(logger, 5*time.Minute, true)
}

// waitForCertManagerDeploymentsClientGo waits for all three cert-manager
// deployments to be available within timeout. When logErrors is false errors
// are returned silently (used for pre-flight readiness probes).
func waitForCertManagerDeploymentsClientGo(logger *zap.Logger, timeout time.Duration, logErrors bool) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	start := time.Now()
	if logErrors {
		core.Info(fmt.Sprintf("Waiting for cert-manager deployments (combined timeout %s across three deployments)", timeout))
	}
	for _, dep := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
		remaining := time.Until(start.Add(timeout))
		if remaining <= 0 {
			err := core.NewWithSentinel(core.ErrCertManagerInstallFailed, fmt.Sprintf("timed out waiting for cert-manager before deployment/%s", dep))
			if logErrors {
				core.Error("cert-manager did not become ready")
				if logger != nil {
					core.LogStructuredError(logger, err, "cert-manager did not become ready")
				}
			}
			return err
		}
		if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, "cert-manager", dep, remaining.Round(time.Second)); err != nil {
			wrapped := core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager component %s not ready: %v", dep, err))
			if logErrors {
				core.Error("cert-manager did not become ready")
				if logger != nil {
					core.LogStructuredError(logger, wrapped, "cert-manager did not become ready")
				}
			}
			return wrapped
		}
	}
	if logErrors {
		core.Info("cert-manager is ready")
	}
	return nil
}

func checkCertManagerInstalledClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.CheckCRDExists(context.Background(), clients, core.CertManagerCRDName); err != nil {
		return core.ErrCertManagerNotInstalled
	}
	return nil
}

func waitForTraefikDeploymentForACMEClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if _, err := k8sclient.GetDeployment(context.Background(), clients, "traefik", "traefik"); err != nil {
		if apierrors.IsNotFound(err) {
			core.Warn("No traefik/traefik deployment found; skipping Traefik wait. cert-manager still needs the Traefik ingress class to serve HTTP-01, with port 80 on your public hostnames")
			return nil
		}
		return err
	}
	core.Info("Waiting for traefik/traefik (ingress must be up before the ACME request)")
	if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, "traefik", "traefik", 3*time.Minute); err != nil {
		return core.WrapWithSentinel(core.ErrCertTraefikNotReady, err, fmt.Sprintf("traefik not ready: %v", err))
	}
	core.Info("traefik/traefik is available")
	return nil
}

func checkNamedClusterIssuerClientGo(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return core.NewWithSentinel(core.ErrClusterIssuerNotFound, "ClusterIssuer name is empty (set --tls-cluster-issuer or MCP_TLS_CLUSTER_ISSUER)")
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.CheckClusterIssuer(context.Background(), clients, name); err != nil {
		return core.WrapWithSentinel(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("ClusterIssuer %q not found. Install your org issuer first (cert-manager) or fix --tls-cluster-issuer / MCP_TLS_CLUSTER_ISSUER: %v", name, err))
	}
	return nil
}

// ensureManagedWorkloadCA gates the bundled mcp-runtime-ca root.
//
// Test mode keeps the convenience of generating a missing CA. Production never
// silently mints a new root (a lost Secret would otherwise re-key trust without
// an overlap window); it requires an existing, validated CA restored from
// backup or supplied by the operator. Invalid or expired CAs always fail;
// near-expiry fails in production and warns in test mode.
func ensureManagedWorkloadCA(plan setupplan.Plan, now time.Time) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	secret, err := clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Get(context.Background(), certmanager.CertCASecretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return core.WrapWithSentinel(core.ErrCASecretNotFound, err, "read managed workload CA Secret")
		}
		if !plan.TestMode {
			return core.NewWithSentinel(core.ErrCASecretNotFound, fmt.Sprintf(
				"managed workload CA Secret %s/%s is missing. Production setup will not generate a new root. Restore it from your encrypted backup, create it from your CA (kubectl create secret tls %s --cert=ca.crt --key=ca.key -n %s), or use an enterprise issuer via --mtls-cluster-issuer; see docs/cli.md (Bundled workload CA lifecycle)",
				certmanager.CertManagerNamespace, certmanager.CertCASecretName, certmanager.CertCASecretName, certmanager.CertManagerNamespace))
		}
		if _, err := ensureCASecretClientGo(); err != nil {
			return core.WrapWithSentinel(core.ErrCASecretNotFound, err, "create managed workload CA")
		}
		return nil
	}
	health, err := certmanager.ValidateCAKeyPair(secret.Data["tls.crt"], secret.Data["tls.key"], now)
	if err != nil {
		return core.WrapWithSentinel(core.ErrCASecretInvalid, err, fmt.Sprintf("managed workload CA %s/%s is not usable: %v. Restore a valid CA from backup or rotate it per docs/cli.md (Bundled workload CA lifecycle)", certmanager.CertManagerNamespace, certmanager.CertCASecretName, err))
	}
	days := int(health.Remaining.Hours() / 24)
	if health.NearExpiry {
		msg := fmt.Sprintf("managed workload CA expires %s (%d days remaining, minimum %d); rotate with dual trust per docs/cli.md (Bundled workload CA lifecycle)", health.NotAfter.UTC().Format(time.RFC3339), days, int(certmanager.MinCARemainingLifetime.Hours()/24))
		if !plan.TestMode {
			return core.NewWithSentinel(core.ErrCANearExpiry, msg)
		}
		core.Warn(msg)
		return nil
	}
	core.Info(fmt.Sprintf("Managed workload CA healthy: %d days remaining", days))
	return nil
}

func checkCASecretClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if _, err := clients.Clientset.CoreV1().Secrets("cert-manager").Get(context.Background(), "mcp-runtime-ca", metav1.GetOptions{}); err != nil {
		return core.ErrCASecretNotFound
	}
	return nil
}

func ensureCASecretClientGo() (bool, error) {
	if err := checkCASecretClientGo(); err == nil {
		return false, nil
	}
	manifest, err := certmanager.RenderGeneratedCASecretManifest(time.Now())
	if err != nil {
		return false, err
	}
	if err := applyManifestYAML(manifest, "", os.Stdout); err != nil {
		return false, err
	}
	return true, nil
}

func applyRegistryCertificateClientGo(certName, secretName string, dnsNames, ipAddresses []string, issuerName string) error {
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		return core.NewWithSentinel(core.ErrCertCertificateSANsEmpty, fmt.Sprintf("%s TLS has no DNS names or IP addresses to request", certName))
	}
	manifest := certmanager.RenderRegistryCertificate(certName, secretName, dnsNames, ipAddresses, issuerName)
	return applyManifestYAML(manifest, "", os.Stdout)
}

func applyCertificateClientGo(certName, secretName, namespace string, dnsNames, ipAddresses []string, issuerName string) error {
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		return core.NewWithSentinel(core.ErrCertCertificateSANsEmpty, fmt.Sprintf("%s TLS has no DNS names or IP addresses to request", certName))
	}
	manifest := certmanager.RenderCertificate(certName, secretName, namespace, dnsNames, ipAddresses, issuerName)
	return applyManifestYAML(manifest, "", os.Stdout)
}

func setupMCPAuthTLSClientGo(logger *zap.Logger, plan setupplan.Plan, issuerName string) error {
	if !plan.DeployMCPAuthServer || plan.TestMode || plan.ProvidedTLSSecrets {
		return nil
	}
	authHost, err := mcpAuthTLSHost(plan)
	if err != nil {
		return err
	}
	authNamespace := core.ComponentNamespace("mcp-auth")
	if err := ensureNamespaceWithLabels(authNamespace, nil); err != nil {
		return err
	}
	const certificateName = "mcp-auth-server-cert"
	core.Info("Applying Certificate for bundled mcp-auth")
	if err := applyCertificateClientGo(certificateName, plan.MCPAuthTLSSecret, authNamespace, []string{authHost}, nil, issuerName); err != nil {
		return wrapApplyCertificateError(err, logger, certificateName)
	}
	certTimeout := core.GetCertTimeout()
	if certTimeout < 5*time.Minute {
		certTimeout = 5 * time.Minute
	}
	if err := waitForCertificateReadyClientGo(certificateName, authNamespace, certTimeout, logger, "mcp-auth certificate"); err != nil {
		return err
	}
	core.Success("mcp-auth certificate issued successfully")
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

func applyRegistryCertificateFromTemplateClientGo() error {
	content, err := os.ReadFile("config/cert-manager/example-registry-certificate.yaml")
	if err != nil {
		return err
	}
	manifest := string(content)
	if host := strings.TrimSpace(core.GetRegistryIngressHost()); host != "" && host != "registry.local" {
		manifest = strings.ReplaceAll(manifest, "registry.local", host)
	}
	return applyManifestYAML(manifest, core.NamespaceRegistry, os.Stdout)
}

func waitForCertificateReadyClientGo(name, namespace string, timeout time.Duration, logger *zap.Logger, label string) error {
	core.Info(fmt.Sprintf("Waiting for %s to be issued (timeout: %s)", label, timeout))
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.WaitForCertificateReady(context.Background(), clients, namespace, name, timeout); err != nil {
		err := core.NewWithSentinel(core.ErrCertificateNotReady, fmt.Sprintf("%s not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", label, timeout))
		core.Error("Certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Certificate not ready")
		}
		return err
	}
	return nil
}

func wrapRegistryNamespaceError(err error, logger *zap.Logger) error {
	wrappedErr := core.WrapWithSentinelAndContext(
		core.ErrCreateRegistryNamespaceFailed,
		err,
		fmt.Sprintf("failed to create registry namespace: %v", err),
		map[string]any{"namespace": core.NamespaceRegistry, "component": "setup"},
	)
	core.Error("Failed to create registry namespace")
	if logger != nil {
		core.LogStructuredError(logger, wrappedErr, "Failed to create registry namespace")
	}
	return wrappedErr
}

func wrapApplyCertificateError(err error, logger *zap.Logger, name string) error {
	wrappedErr := core.WrapWithSentinelAndContext(
		core.ErrApplyCertificateFailed,
		err,
		fmt.Sprintf("failed to apply Certificate: %v", err),
		map[string]any{"certificate": name, "namespace": core.NamespaceRegistry, "component": "setup"},
	)
	core.Error("Failed to apply Certificate")
	if logger != nil {
		core.LogStructuredError(logger, wrappedErr, "Failed to apply Certificate")
	}
	return wrappedErr
}

func ensureRegistryCertificateOwnershipClientGo(logger *zap.Logger) error {
	core.Info("Checking registry TLS Certificate ownership")
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.RemoveIngressAnnotation(context.Background(), clients, core.NamespaceRegistry, core.RegistryServiceName, "cert-manager.io/cluster-issuer"); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"ingress": core.RegistryServiceName, "namespace": core.NamespaceRegistry, "component": "setup"})
		core.Error("Failed to remove registry ingress-shim annotation")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to remove registry ingress-shim annotation")
		}
		return wrappedErr
	}
	owners, err := k8sclient.CertificateOwnersForSecret(context.Background(), clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"resource_name": certmanager.RegistryTLSSecretName, "namespace": core.NamespaceRegistry, "component": "setup"})
		core.Error("Registry TLS Certificate conflict")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Registry TLS Certificate conflict")
		}
		return wrappedErr
	}
	if len(owners) == 0 || (len(owners) == 1 && owners[0] == certmanager.RegistryCertificateName) {
		return nil
	}
	msg := fmt.Sprintf("registry TLS secret %q in namespace %q is already referenced by Certificate(s) %s; setup owns this secret with Certificate %q. Delete or rename the extra Certificate resource before re-running setup (old ingress-shim drift is usually fixed with: kubectl delete certificate registry-tls -n registry)", certmanager.RegistryTLSSecretName, core.NamespaceRegistry, strings.Join(owners, ", "), certmanager.RegistryCertificateName)
	err = core.NewWithSentinel(core.ErrCertRegistryTLSSecretConflict, msg)
	wrappedErr := core.WrapWithSentinelAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"resource_name": certmanager.RegistryTLSSecretName, "namespace": core.NamespaceRegistry, "component": "setup"})
	core.Error("Registry TLS Certificate conflict")
	if logger != nil {
		core.LogStructuredError(logger, wrappedErr, "Registry TLS Certificate conflict")
	}
	return wrappedErr
}

func setupBundledRegistryInternalTLSClientGo(logger *zap.Logger, plan setupplan.Plan) error {
	if plan.RegistryMode != setupplan.RegistryModeBundledHTTPS {
		return nil
	}
	issuerName, err := bundledRegistryInternalIssuerNameClientGo(plan)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("failed to inspect internal registry ClusterIssuer: %v", err))
		core.Error("Internal registry issuer unavailable")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Internal registry issuer unavailable")
		}
		return wrappedErr
	}
	if issuerName == certmanager.CertClusterIssuerName {
		core.Info("Ensuring internal registry CA secret")
		created, err := ensureCASecretClientGo()
		if err != nil {
			err := core.WrapWithSentinel(core.ErrCASecretNotFound, err, "bundled HTTPS registry pulls need an internal CA for registry-internal-tls. Setup could not create cert-manager/mcp-runtime-ca; pass --tls-cluster-issuer for an existing internal issuer or create the CA secret manually")
			core.Error("Internal registry CA secret unavailable")
			if logger != nil {
				core.LogStructuredError(logger, err, "Internal registry CA secret unavailable")
			}
			return err
		}
		if created {
			core.Info("Generated cert-manager/mcp-runtime-ca for internal registry TLS; configure every Kubernetes node to trust its tls.crt before pulling from the bundled HTTPS registry")
		}
		core.Info("Applying internal registry ClusterIssuer")
		if err := applyManifestFile("config/cert-manager/cluster-issuer.yaml", "", os.Stdout); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply internal registry ClusterIssuer: %v", err))
			core.Error("Failed to apply internal registry ClusterIssuer")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to apply internal registry ClusterIssuer")
			}
			return wrappedErr
		}
	}

	dnsNames, ipAddresses := registryInternalCertificateSANs(plan)
	core.Info("Applying Certificate for internal registry pod TLS")
	if err := applyRegistryCertificateClientGo(certmanager.RegistryInternalCertificateName, certmanager.RegistryInternalTLSSecretName, dnsNames, ipAddresses, issuerName); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyCertificateFailed,
			err,
			fmt.Sprintf("failed to apply internal registry Certificate: %v", err),
			map[string]any{"certificate": certmanager.RegistryInternalCertificateName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to apply internal registry Certificate")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply internal registry Certificate")
		}
		return wrappedErr
	}

	certTimeout := core.GetCertTimeout()
	if certTimeout < 2*time.Minute {
		certTimeout = 2 * time.Minute
	}
	if err := waitForCertificateReadyClientGo(certmanager.RegistryInternalCertificateName, core.NamespaceRegistry, certTimeout, logger, "internal registry certificate"); err != nil {
		return err
	}
	core.Success("Internal registry certificate issued successfully")
	return nil
}

func bundledRegistryInternalIssuerNameClientGo(plan setupplan.Plan) (string, error) {
	issuerName := strings.TrimSpace(plan.TLSClusterIssuer)
	if issuerName == "" {
		return certmanager.CertClusterIssuerName, nil
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	usesACME, err := k8sclient.ClusterIssuerUsesACME(context.Background(), clients, issuerName)
	if err != nil {
		return "", err
	}
	if usesACME {
		core.Warn(fmt.Sprintf("ClusterIssuer %q uses ACME and cannot issue internal registry DNS names; using %s for registry-internal-tls", issuerName, certmanager.CertClusterIssuerName))
		return certmanager.CertClusterIssuerName, nil
	}
	return issuerName, nil
}

func registryCertificateSANs(plan setupplan.Plan) ([]string, []string) {
	dnsNames := append([]string{}, certmanager.ACMETLSDNSNames()...)
	return dedupeStrings(dnsNames), nil
}

func registryInternalCertificateSANs(plan setupplan.Plan) ([]string, []string) {
	var dnsNames []string
	var ipAddresses []string
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		dnsNames = append(dnsNames,
			core.DefaultRegistryIngressHost,
			"registry.registry.svc",
			"registry.registry.svc.cluster.local",
		)
		addRegistrySAN(strings.TrimSpace(core.GetRegistryEndpoint()), &dnsNames, &ipAddresses)
	}
	return dedupeStrings(dnsNames), dedupeStrings(ipAddresses)
}

func bundledRegistryInternalIssuerName(kubectl core.KubectlRunner, plan setupplan.Plan) (string, error) {
	issuerName := strings.TrimSpace(plan.TLSClusterIssuer)
	if issuerName == "" {
		return certmanager.CertClusterIssuerName, nil
	}
	usesACME, err := clusterIssuerUsesACME(kubectl, issuerName)
	if err != nil {
		return "", err
	}
	if usesACME {
		core.Warn(fmt.Sprintf("ClusterIssuer %q uses ACME and cannot issue internal registry DNS names; using %s for registry-internal-tls", issuerName, certmanager.CertClusterIssuerName))
		return certmanager.CertClusterIssuerName, nil
	}
	return issuerName, nil
}

func clusterIssuerUsesACME(kubectl core.KubectlRunner, name string) (bool, error) {
	if kubectl == nil {
		return false, core.NewWithSentinel(core.ErrSetupTLSKubectlRunnerNil, "kubectl runner is nil")
	}
	cmd, err := kubectl.CommandArgs([]string{"get", "clusterissuer", name, "-o", "jsonpath={.spec.acme.server}"})
	if err != nil {
		return false, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return false, core.WrapWithSentinel(core.ErrSetupInspectClusterIssuerFailed, err, fmt.Sprintf("inspect ClusterIssuer %q: %v: %s", name, err, detail))
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func addRegistrySAN(raw string, dnsNames, ipAddresses *[]string) {
	host := registryEndpointHost(raw)
	if host == "" {
		return
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		*ipAddresses = append(*ipAddresses, ip.String())
		return
	}
	*dnsNames = append(*dnsNames, host)
}

func registryEndpointHost(raw string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
			trimmed = parsed.Host
		}
	}
	if slash := strings.Index(trimmed, "/"); slash >= 0 {
		trimmed = trimmed[:slash]
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		return strings.Trim(host, "[]")
	}
	if idx := strings.LastIndex(trimmed, ":"); idx >= 0 && strings.Count(trimmed, ":") == 1 {
		return strings.Trim(trimmed[:idx], "[]")
	}
	return strings.Trim(trimmed, "[]")
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func setupTLSLetsEncrypt(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Configuring TLS with Let's Encrypt (cert-manager HTTP-01)")
	if err := certmanager.ValidateACMEHostnameForPublicCA(); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid configuration for Let's Encrypt")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid configuration for Let's Encrypt")
		}
		return wrappedErr
	}
	if err := certmanager.ValidateIngressManifestForACME(plan.Ingress.Manifest); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Ingress configuration blocks Let's Encrypt")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Ingress configuration blocks Let's Encrypt")
		}
		return wrappedErr
	}
	if plan.InstallCertManager {
		if err := certmanager.EnsureCertManagerInstalled(kubectl, logger); err != nil {
			return err
		}
	} else {
		core.Info("Checking cert-manager installation (--skip-cert-manager-install)")
		if err := certmanager.CheckCertManagerInstalledWithKubectl(kubectl); err != nil {
			err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
			core.Error("Cert-manager not installed")
			if logger != nil {
				core.LogStructuredError(logger, err, "Cert-manager not installed")
			}
			return err
		}
		core.Info("cert-manager CRDs found")
	}
	if err := certmanager.WaitForTraefikDeploymentForACME(kubectl); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Traefik is not ready for HTTP-01")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Traefik is not ready for HTTP-01")
		}
		return wrappedErr
	}
	core.Info("Checking TCP connectivity to your ACME hostnames on port 80 (best effort from this machine)")
	certmanager.PreflightACMEHostnamesPort80(certmanager.ACMETLSDNSNames())

	core.Info("Applying Let's Encrypt ClusterIssuer")
	if err := certmanager.ApplyLetsEncryptClusterIssuer(kubectl, plan.ACMEmail, plan.ACMEStaging, logger); err != nil {
		return err
	}

	if err := kube.EnsureNamespace(kubectl.CommandArgs, core.NamespaceRegistry); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrCreateRegistryNamespaceFailed,
			err,
			fmt.Sprintf("failed to create registry namespace: %v", err),
			map[string]any{"namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to create registry namespace")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to create registry namespace")
		}
		return wrappedErr
	}
	if err := ensureRegistryCertificateOwnership(kubectl, logger); err != nil {
		return err
	}
	if err := kube.ApplyManifestFromFile(kubectl.CommandArgs, "config/registry/base/acme-networkpolicy.yaml", os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("allow registry ACME HTTP-01 solver traffic: %w", err)
	}

	issuerName := certmanager.ClusterIssuerNameForACME(plan.ACMEStaging)
	dnsNames := certmanager.ACMETLSDNSNames()
	core.Info("Applying Certificate for registry (Let's Encrypt SANs)")
	if err := certmanager.ApplyRegistryCertificateForACME(kubectl, dnsNames, issuerName); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyCertificateFailed,
			err,
			fmt.Sprintf("failed to apply Certificate: %v", err),
			map[string]any{"certificate": certmanager.RegistryCertificateName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to apply Certificate")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply Certificate")
		}
		return wrappedErr
	}

	certTimeout := core.GetCertTimeout()
	if certTimeout < 5*time.Minute {
		certTimeout = 5 * time.Minute
	}
	core.Info(fmt.Sprintf("Waiting for certificate to be issued (timeout: %s)", certTimeout))
	if err := certmanager.WaitForCertificateReadyWithKubectl(kubectl, certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout); err != nil {
		err := core.NewWithSentinel(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
		core.Error("Certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Certificate not ready")
		}
		return err
	}
	core.Success("Certificate issued successfully")
	if err := setupBundledRegistryInternalTLSStep(kubectl, logger, plan); err != nil {
		return err
	}
	return nil
}

// setupTLSWithExistingClusterIssuer issues the registry (and optional mcp SAN) Certificate using a
// ClusterIssuer that already exists in the cluster (internal / enterprise CA).
//
//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func setupTLSWithExistingClusterIssuer(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	issuerName := strings.TrimSpace(plan.TLSClusterIssuer)
	core.Info("Configuring TLS with existing ClusterIssuer: " + issuerName)
	if plan.InstallCertManager {
		if err := certmanager.EnsureCertManagerInstalled(kubectl, logger); err != nil {
			return err
		}
	} else {
		core.Info("Checking cert-manager installation (--skip-cert-manager-install)")
		if err := certmanager.CheckCertManagerInstalledWithKubectl(kubectl); err != nil {
			err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
			core.Error("Cert-manager not installed")
			if logger != nil {
				core.LogStructuredError(logger, err, "Cert-manager not installed")
			}
			return err
		}
		core.Info("cert-manager CRDs found")
	}

	if err := certmanager.CheckNamedClusterIssuerWithKubectl(kubectl, issuerName); err != nil {
		core.Error("Cluster issuer not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cluster issuer not found")
		}
		return err
	}

	if err := kube.EnsureNamespace(kubectl.CommandArgs, core.NamespaceRegistry); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrCreateRegistryNamespaceFailed,
			err,
			fmt.Sprintf("failed to create registry namespace: %v", err),
			map[string]any{"namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to create registry namespace")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to create registry namespace")
		}
		return wrappedErr
	}
	if err := ensureRegistryCertificateOwnership(kubectl, logger); err != nil {
		return err
	}
	if err := kube.ApplyManifestFromFile(kubectl.CommandArgs, "config/registry/base/acme-networkpolicy.yaml", os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("allow registry ACME HTTP-01 solver traffic: %w", err)
	}

	dnsNames, ipAddresses := registryCertificateSANs(plan)
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		err := core.NewWithSentinel(core.ErrSetupTLSCertificateSANsEmpty, "no DNS names or IP addresses resolved for the Certificate; set MCP_PLATFORM_DOMAIN, MCP_REGISTRY_HOST, or MCP_REGISTRY_INGRESS_HOST (and optional MCP_MCP_INGRESS_HOST)")
		wrappedErr := core.WrapWithSentinel(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid TLS host configuration")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid TLS host configuration")
		}
		return wrappedErr
	}

	core.Info("Applying Certificate for registry (custom ClusterIssuer)")
	if err := certmanager.ApplyRegistryCertificate(kubectl, dnsNames, ipAddresses, issuerName); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyCertificateFailed,
			err,
			fmt.Sprintf("failed to apply Certificate: %v", err),
			map[string]any{"certificate": certmanager.RegistryCertificateName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to apply Certificate")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply Certificate")
		}
		return wrappedErr
	}

	certTimeout := core.GetCertTimeout()
	if certTimeout < 5*time.Minute {
		certTimeout = 5 * time.Minute
	}
	core.Info(fmt.Sprintf("Waiting for certificate to be issued (timeout: %s)", certTimeout))
	if err := certmanager.WaitForCertificateReadyWithKubectl(kubectl, certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout); err != nil {
		err := core.NewWithSentinel(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager and your ClusterIssuer configuration: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
		core.Error("Certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Certificate not ready")
		}
		return err
	}
	core.Success("Certificate issued successfully")
	if err := setupBundledRegistryInternalTLSStep(kubectl, logger, plan); err != nil {
		return err
	}
	return nil
}

// setupTLSPrivateCA uses mcp-runtime-ca in cert-manager; bundled HTTPS setup
// generates it when missing, while other private-CA paths require it up front.
func setupTLSPrivateCA(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Checking cert-manager installation")
	if err := certmanager.CheckCertManagerInstalledWithKubectl(kubectl); err != nil {
		err := core.WrapWithSentinel(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it first:\n  helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace --set crds.enabled=true\n  or run setup with --with-tls --acme-email <addr> to install cert-manager automatically")
		core.Error("Cert-manager not installed")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cert-manager not installed")
		}
		return err
	}
	core.Info("cert-manager CRDs found")

	core.Info("Checking CA secret")
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		created, err := certmanager.EnsureCASecretWithKubectl(kubectl)
		if err != nil {
			err := core.WrapWithSentinel(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' could not be generated in cert-manager namespace. Create a private CA manually:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
			core.Error("CA secret unavailable")
			if logger != nil {
				core.LogStructuredError(logger, err, "CA secret unavailable")
			}
			return err
		}
		if created {
			core.Info("Generated cert-manager/mcp-runtime-ca for bundled HTTPS registry TLS; configure every Kubernetes node to trust its tls.crt before pulling from the bundled HTTPS registry")
		}
	} else if err := certmanager.CheckCASecretWithKubectl(kubectl); err != nil {
		err := core.WrapWithSentinel(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' not found in cert-manager namespace. For Let's Encrypt use --acme-email, or create a private CA:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
		core.Error("CA secret not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "CA secret not found")
		}
		return err
	}
	core.Info("CA secret found")

	core.Info("Applying ClusterIssuer")
	if err := certmanager.ApplyClusterIssuerWithKubectl(kubectl); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply ClusterIssuer: %v", err))
		core.Error("Failed to apply ClusterIssuer")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply ClusterIssuer")
		}
		return wrappedErr
	}

	if err := kube.EnsureNamespace(kubectl.CommandArgs, core.NamespaceRegistry); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrCreateRegistryNamespaceFailed,
			err,
			fmt.Sprintf("failed to create registry namespace: %v", err),
			map[string]any{"namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to create registry namespace")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to create registry namespace")
		}
		return wrappedErr
	}
	if err := ensureRegistryCertificateOwnership(kubectl, logger); err != nil {
		return err
	}

	core.Info("Applying Certificate for registry")
	var certErr error
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS {
		dnsNames, ipAddresses := registryCertificateSANs(plan)
		certErr = certmanager.ApplyRegistryCertificate(kubectl, dnsNames, ipAddresses, certmanager.CertClusterIssuerName)
	} else {
		certErr = certmanager.ApplyRegistryCertificateWithKubectl(kubectl)
	}
	if certErr != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyCertificateFailed,
			certErr,
			fmt.Sprintf("failed to apply Certificate: %v", certErr),
			map[string]any{"certificate": certmanager.RegistryCertificateName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to apply Certificate")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply Certificate")
		}
		return wrappedErr
	}

	certTimeout := core.GetCertTimeout()
	core.Info(fmt.Sprintf("Waiting for certificate to be issued (timeout: %s)", certTimeout))
	if err := certmanager.WaitForCertificateReadyWithKubectl(kubectl, certmanager.RegistryCertificateName, core.NamespaceRegistry, certTimeout); err != nil {
		err := core.NewWithSentinel(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
		core.Error("Certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Certificate not ready")
		}
		return err
	}
	core.Success("Certificate issued successfully")
	if err := setupBundledRegistryInternalTLSStep(kubectl, logger, plan); err != nil {
		return err
	}
	return nil
}

func setupBundledRegistryInternalTLSStep(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	if plan.RegistryMode != setupplan.RegistryModeBundledHTTPS {
		return nil
	}
	issuerName, err := bundledRegistryInternalIssuerName(kubectl, plan)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("failed to inspect internal registry ClusterIssuer: %v", err))
		core.Error("Internal registry issuer unavailable")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Internal registry issuer unavailable")
		}
		return wrappedErr
	}
	if issuerName == certmanager.CertClusterIssuerName {
		core.Info("Ensuring internal registry CA secret")
		created, err := certmanager.EnsureCASecretWithKubectl(kubectl)
		if err != nil {
			err := core.WrapWithSentinel(
				core.ErrCASecretNotFound,
				err,
				"bundled HTTPS registry pulls need an internal CA for registry-internal-tls. Setup could not create cert-manager/mcp-runtime-ca; pass --tls-cluster-issuer for an existing internal issuer or create the CA secret manually",
			)
			core.Error("Internal registry CA secret unavailable")
			if logger != nil {
				core.LogStructuredError(logger, err, "Internal registry CA secret unavailable")
			}
			return err
		}
		if created {
			core.Info("Generated cert-manager/mcp-runtime-ca for internal registry TLS; configure every Kubernetes node to trust its tls.crt before pulling from the bundled HTTPS registry")
		}
		core.Info("Applying internal registry ClusterIssuer")
		if err := certmanager.ApplyClusterIssuerWithKubectl(kubectl); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply internal registry ClusterIssuer: %v", err))
			core.Error("Failed to apply internal registry ClusterIssuer")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to apply internal registry ClusterIssuer")
			}
			return wrappedErr
		}
	}

	dnsNames, ipAddresses := registryInternalCertificateSANs(plan)
	core.Info("Applying Certificate for internal registry pod TLS")
	if err := certmanager.ApplyRegistryInternalCertificate(kubectl, dnsNames, ipAddresses, issuerName); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyCertificateFailed,
			err,
			fmt.Sprintf("failed to apply internal registry Certificate: %v", err),
			map[string]any{"certificate": certmanager.RegistryInternalCertificateName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to apply internal registry Certificate")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply internal registry Certificate")
		}
		return wrappedErr
	}

	certTimeout := core.GetCertTimeout()
	if certTimeout < 2*time.Minute {
		certTimeout = 2 * time.Minute
	}
	core.Info(fmt.Sprintf("Waiting for internal registry certificate to be issued (timeout: %s)", certTimeout))
	if err := certmanager.WaitForCertificateReadyWithKubectl(kubectl, certmanager.RegistryInternalCertificateName, core.NamespaceRegistry, certTimeout); err != nil {
		err := core.NewWithSentinel(core.ErrCertificateNotReady, fmt.Sprintf("internal registry certificate not ready after %s. Check cert-manager and your internal issuer configuration", certTimeout))
		core.Error("Internal registry certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Internal registry certificate not ready")
		}
		return err
	}
	core.Success("Internal registry certificate issued successfully")
	return nil
}

func ensureRegistryCertificateOwnership(kubectl core.KubectlRunner, logger *zap.Logger) error {
	core.Info("Checking registry TLS Certificate ownership")
	if err := certmanager.RemoveRegistryIngressShimAnnotationWithKubectl(kubectl); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrTLSSetupFailed,
			err,
			err.Error(),
			map[string]any{"ingress": core.RegistryServiceName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Failed to remove registry ingress-shim annotation")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to remove registry ingress-shim annotation")
		}
		return wrappedErr
	}
	if err := certmanager.CheckRegistryCertificateOwnershipWithKubectl(kubectl); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrTLSSetupFailed,
			err,
			err.Error(),
			map[string]any{"resource_name": certmanager.RegistryTLSSecretName, "namespace": core.NamespaceRegistry, "component": "setup"},
		)
		core.Error("Registry TLS Certificate conflict")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Registry TLS Certificate conflict")
		}
		return wrappedErr
	}
	return nil
}
