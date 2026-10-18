package platform

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
	"os"
	"strings"
	"time"
)

func setupTLSLetsEncryptClientGo(logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Configuring TLS with Let's Encrypt (cert-manager HTTP-01)")
	if err := certmanager.ValidateACMEHostnameForPublicCA(); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
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
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
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
		err := core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
		core.Error("Cert-manager not installed")
		if logger != nil {
			core.LogStructuredError(logger, err, "Cert-manager not installed")
		}
		return err
	}
	if err := waitForTraefikDeploymentForACMEClientGo(); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
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
		err := core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
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
		err := core.NewWithBase(core.ErrSetupTLSCertificateSANsEmpty, "no DNS names or IP addresses resolved for the Certificate; set MCP_PLATFORM_DOMAIN, MCP_REGISTRY_HOST, or MCP_REGISTRY_INGRESS_HOST (and optional MCP_MCP_INGRESS_HOST)")
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
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
		err := core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it first:\n  helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace --set crds.enabled=true\n  or run setup with --with-tls --acme-email <addr> to install cert-manager automatically")
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
			err := core.WrapWithBase(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' could not be generated in cert-manager namespace. Create a private CA manually:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
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
		err := core.WrapWithBase(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' not found in cert-manager namespace. For Let's Encrypt use --acme-email, or create a private CA:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
		core.Error("CA secret not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "CA secret not found")
		}
		return err
	}
	core.Info("CA secret found")

	core.Info("Applying ClusterIssuer")
	if err := applyManifestFile("config/cert-manager/cluster-issuer.yaml", "", os.Stdout); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply ClusterIssuer: %v", err))
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

func applyCertificateClientGo(certName, secretName, namespace string, dnsNames, ipAddresses []string, issuerName string) error {
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		return core.NewWithBase(core.ErrCertCertificateSANsEmpty, fmt.Sprintf("%s TLS has no DNS names or IP addresses to request", certName))
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

func waitForCertificateReadyClientGo(name, namespace string, timeout time.Duration, logger *zap.Logger, label string) error {
	core.Info(fmt.Sprintf("Waiting for %s to be issued (timeout: %s)", label, timeout))
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.WaitForCertificateReady(context.Background(), clients, namespace, name, timeout); err != nil {
		err := core.NewWithBase(core.ErrCertificateNotReady, fmt.Sprintf("%s not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", label, timeout))
		core.Error("Certificate not ready")
		if logger != nil {
			core.LogStructuredError(logger, err, "Certificate not ready")
		}
		return err
	}
	return nil
}

func wrapApplyCertificateError(err error, logger *zap.Logger, name string) error {
	wrappedErr := core.WrapWithBaseAndContext(
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
