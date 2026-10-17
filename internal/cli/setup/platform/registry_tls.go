package platform

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

// Registry TLS phase: bundled-registry certificates, SAN derivation, internal
// issuer selection, and certificate ownership. Split from tls.go (#268); the
// cluster-wide TLS orchestration stays in tls.go.

func applyRegistryCertificateClientGo(certName, secretName string, dnsNames, ipAddresses []string, issuerName string) error {
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		return core.NewWithBase(core.ErrCertCertificateSANsEmpty, fmt.Sprintf("%s TLS has no DNS names or IP addresses to request", certName))
	}
	manifest := certmanager.RenderRegistryCertificate(certName, secretName, dnsNames, ipAddresses, issuerName)
	return applyManifestYAML(manifest, "", os.Stdout)
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

func wrapRegistryNamespaceError(err error, logger *zap.Logger) error {
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

func ensureRegistryCertificateOwnershipClientGo(logger *zap.Logger) error {
	core.Info("Checking registry TLS Certificate ownership")
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.RemoveIngressAnnotation(context.Background(), clients, core.NamespaceRegistry, core.RegistryServiceName, "cert-manager.io/cluster-issuer"); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"ingress": core.RegistryServiceName, "namespace": core.NamespaceRegistry, "component": "setup"})
		core.Error("Failed to remove registry ingress-shim annotation")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to remove registry ingress-shim annotation")
		}
		return wrappedErr
	}
	owners, err := k8sclient.CertificateOwnersForSecret(context.Background(), clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"resource_name": certmanager.RegistryTLSSecretName, "namespace": core.NamespaceRegistry, "component": "setup"})
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
	err = core.NewWithBase(core.ErrCertRegistryTLSSecretConflict, msg)
	wrappedErr := core.WrapWithBaseAndContext(core.ErrTLSSetupFailed, err, err.Error(), map[string]any{"resource_name": certmanager.RegistryTLSSecretName, "namespace": core.NamespaceRegistry, "component": "setup"})
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
		wrappedErr := core.WrapWithBase(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("failed to inspect internal registry ClusterIssuer: %v", err))
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
			err := core.WrapWithBase(core.ErrCASecretNotFound, err, "bundled HTTPS registry pulls need an internal CA for registry-internal-tls. Setup could not create cert-manager/mcp-runtime-ca; pass --tls-cluster-issuer for an existing internal issuer or create the CA secret manually")
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
			wrappedErr := core.WrapWithBase(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply internal registry ClusterIssuer: %v", err))
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		return false, core.NewWithBase(core.ErrSetupTLSKubectlRunnerNil, "kubectl runner is nil")
	}
	cmd, err := kubectl.CommandArgs([]string{"get", "clusterissuer", name, "-o", "jsonpath={.spec.acme.server}"})
	if err != nil {
		return false, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return false, core.WrapWithBase(core.ErrSetupInspectClusterIssuerFailed, err, fmt.Sprintf("inspect ClusterIssuer %q: %v: %s", name, err, detail))
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

func setupBundledRegistryInternalTLSStep(kubectl core.KubectlRunner, logger *zap.Logger, plan setupplan.Plan) error {
	if plan.RegistryMode != setupplan.RegistryModeBundledHTTPS {
		return nil
	}
	issuerName, err := bundledRegistryInternalIssuerName(kubectl, plan)
	if err != nil {
		wrappedErr := core.WrapWithBase(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("failed to inspect internal registry ClusterIssuer: %v", err))
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
			err := core.WrapWithBase(
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
			wrappedErr := core.WrapWithBase(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply internal registry ClusterIssuer: %v", err))
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		err := core.NewWithBase(core.ErrCertificateNotReady, fmt.Sprintf("internal registry certificate not ready after %s. Check cert-manager and your internal issuer configuration", certTimeout))
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
		wrappedErr := core.WrapWithBaseAndContext(
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
		wrappedErr := core.WrapWithBaseAndContext(
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
