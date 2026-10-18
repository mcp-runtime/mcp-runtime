package platform

import (
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"os"
	"strings"
	"time"
)

//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func setupTLSLetsEncrypt(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	core.Info("Configuring TLS with Let's Encrypt (cert-manager HTTP-01)")
	if err := certmanager.ValidateACMEHostnameForPublicCA(); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid configuration for Let's Encrypt")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid configuration for Let's Encrypt")
		}
		return wrappedErr
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
		if err := certmanager.EnsureCertManagerInstalled(kubectl, logger); err != nil {
			return err
		}
	} else {
		core.Info("Checking cert-manager installation (--skip-cert-manager-install)")
		if err := certmanager.CheckCertManagerInstalledWithKubectl(kubectl); err != nil {
			err := core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
			core.Error("Cert-manager not installed")
			if logger != nil {
				core.LogStructuredError(logger, err, "Cert-manager not installed")
			}
			return err
		}
		core.Info("cert-manager CRDs found")
	}
	if err := certmanager.WaitForTraefikDeploymentForACME(kubectl); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		err := core.NewWithBase(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
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
			err := core.WrapWithBase(core.ErrCertManagerNotInstalled, err, "cert-manager not installed. Install it, or omit --skip-cert-manager-install to let setup apply it from upstream")
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		err := core.NewWithBase(core.ErrSetupTLSCertificateSANsEmpty, "no DNS names or IP addresses resolved for the Certificate; set MCP_PLATFORM_DOMAIN, MCP_REGISTRY_HOST, or MCP_REGISTRY_INGRESS_HOST (and optional MCP_MCP_INGRESS_HOST)")
		wrappedErr := core.WrapWithBase(core.ErrTLSSetupFailed, err, err.Error())
		core.Error("Invalid TLS host configuration")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Invalid TLS host configuration")
		}
		return wrappedErr
	}

	core.Info("Applying Certificate for registry (custom ClusterIssuer)")
	if err := certmanager.ApplyRegistryCertificate(kubectl, dnsNames, ipAddresses, issuerName); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
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
		err := core.NewWithBase(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager and your ClusterIssuer configuration: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
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
		created, err := certmanager.EnsureCASecretWithKubectl(kubectl)
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
	} else if err := certmanager.CheckCASecretWithKubectl(kubectl); err != nil {
		err := core.WrapWithBase(core.ErrCASecretNotFound, err, "CA secret 'mcp-runtime-ca' not found in cert-manager namespace. For Let's Encrypt use --acme-email, or create a private CA:\n  kubectl create secret tls mcp-runtime-ca --cert=ca.crt --key=ca.key -n cert-manager")
		core.Error("CA secret not found")
		if logger != nil {
			core.LogStructuredError(logger, err, "CA secret not found")
		}
		return err
	}
	core.Info("CA secret found")

	core.Info("Applying ClusterIssuer")
	if err := certmanager.ApplyClusterIssuerWithKubectl(kubectl); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply ClusterIssuer: %v", err))
		core.Error("Failed to apply ClusterIssuer")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply ClusterIssuer")
		}
		return wrappedErr
	}

	if err := kube.EnsureNamespace(kubectl.CommandArgs, core.NamespaceRegistry); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		err := core.NewWithBase(core.ErrCertificateNotReady, fmt.Sprintf("certificate not ready after %s. Check cert-manager logs: kubectl logs -n cert-manager deployment/cert-manager", certTimeout))
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
