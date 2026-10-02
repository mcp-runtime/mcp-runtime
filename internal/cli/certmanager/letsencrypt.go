package certmanager

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
)

const (
	// certManagerRelease is pinned for reproducible installs (kubectl apply).
	certManagerRelease           = "v1.16.2"
	letsencryptProdURL           = "https://acme-v02.api.letsencrypt.org/directory"
	letsencryptStagingURL        = "https://acme-staging-v02.api.letsencrypt.org/directory"
	letsencryptProdIssuerName    = "letsencrypt-prod"
	letsencryptStagingIssuerName = "letsencrypt-staging"
	// acmeHTTP01DevIngressOverlay is the kustomize overlay that binds Traefik to 8000/8443 for local port-forwards, not public :80.
	acmeHTTP01DevIngressOverlay = "config/ingress/overlays/http"
	traefikManagedNamespace     = "traefik"
	traefikManagedDeployment    = "traefik"
)

func certManagerInstallManifestURL() string {
	return fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", certManagerRelease)
}

func CertManagerInstallManifestURL() string {
	return certManagerInstallManifestURL()
}

// ClusterIssuerNameForACME returns the ClusterIssuer resource name for Let's Encrypt.
func ClusterIssuerNameForACME(staging bool) string {
	if staging {
		return letsencryptStagingIssuerName
	}
	return letsencryptProdIssuerName
}

func acmeServerURL(staging bool) string {
	if staging {
		return letsencryptStagingURL
	}
	return letsencryptProdURL
}

// acmeTLSDNSNames returns the SANs for the unified registry Certificate in the
// registry namespace. The platform UI hostname is intentionally NOT included:
// the platform Ingress in the mcp-platform namespace owns its own cert via
// cert-manager's ingress-shim because Kubernetes Ingress resources cannot
// reference TLS Secrets across namespaces. Adding the platform host here would
// cause a redundant ACME order for the same name on every renewal.
func acmeTLSDNSNames() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, h := range []string{core.GetRegistryIngressHost(), core.GetMcpIngressHost()} {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

func ACMETLSDNSNames() []string {
	return acmeTLSDNSNames()
}

func validateACMEHostnameForPublicCA() error {
	names := acmeTLSDNSNames()
	if len(names) == 0 {
		return core.NewWithSentinel(core.ErrCertACMEPublicDNSNameRequired, "ACME public CA requires a public DNS name; set MCP_PLATFORM_DOMAIN, MCP_REGISTRY_HOST, MCP_REGISTRY_INGRESS_HOST, or MCP_MCP_INGRESS_HOST")
	}
	for _, host := range names {
		if isDevRegistryURL(host) {
			return core.NewWithSentinel(core.ErrCertACMEPublicDNSNameInvalid, fmt.Sprintf("ACME public CA requires a public DNS name; set MCP_PLATFORM_DOMAIN (e.g. mcpruntime.com for registry. and mcp. names) or MCP_REGISTRY_INGRESS_HOST, not %q", host))
		}
	}
	return nil
}

func ValidateACMEHostnameForPublicCA() error {
	return validateACMEHostnameForPublicCA()
}

// ValidateACMEHostnamesForPublicCA validates additional hostnames that are
// issued by namespace-local Certificates rather than the unified registry
// Certificate.
func ValidateACMEHostnamesForPublicCA(hosts ...string) error {
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if isDevRegistryURL(host) {
			return core.NewWithSentinel(core.ErrCertACMEPublicDNSNameInvalid, fmt.Sprintf("ACME public CA requires a public DNS name, not %q", host))
		}
	}
	return nil
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

// validateIngressManifestForACME rejects the dev "http" overlay, which does not listen on 80/443, so Let’s Encrypt HTTP-01 cannot work.
func validateIngressManifestForACME(ingressManifest string) error {
	m := strings.TrimSpace(ingressManifest)
	if m == "" {
		return nil
	}
	if filepath.Base(filepath.Clean(m)) == "http" {
		msg := fmt.Sprintf(
			"http-01 (Let's Encrypt) must reach your hostnames on port 80, but the %q overlay uses 8000/8443. Omit --ingress-manifest so setup uses the prod overlay, or set --ingress-manifest %q, then re-run (use --force-ingress-install if an old ingress is already present)",
			acmeHTTP01DevIngressOverlay, "config/ingress/overlays/prod",
		)
		return core.NewWithSentinel(core.ErrCertACMEIngressManifestInvalid, msg)
	}
	return nil
}

func ValidateIngressManifestForACME(ingressManifest string) error {
	return validateIngressManifestForACME(ingressManifest)
}

// waitForTraefikDeploymentForACME waits for the Traefik this repo installs in namespace "traefik". If it is missing (e.g. skipped install, or another cluster ingress), a warning is printed and we continue.
func waitForTraefikDeploymentForACME(kubectl core.KubectlRunner) error {
	if err := kubectl.RunWithOutput(
		[]string{"get", "deployment", traefikManagedDeployment, "-n", traefikManagedNamespace},
		io.Discard, io.Discard,
	); err != nil {
		core.Warn("No " + traefikManagedNamespace + "/" + traefikManagedDeployment + " deployment found; skipping Traefik wait. cert-manager still needs the Traefik ingress class to serve HTTP-01, with port 80 on your public hostnames")
		return nil
	}
	core.Info("Waiting for " + traefikManagedNamespace + "/" + traefikManagedDeployment + " (ingress must be up before the ACME request)")
	// #nosec G204 -- fixed resource names; timeout is fixed.
	if err := kubectl.RunWithOutput([]string{
		"wait", "--for=condition=Available",
		"deployment/" + traefikManagedDeployment, "-n", traefikManagedNamespace, "--timeout=3m",
	}, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinel(core.ErrCertTraefikNotReady, err, fmt.Sprintf("traefik not ready: %v", err))
	}
	core.Info(traefikManagedNamespace + "/" + traefikManagedDeployment + " is available")
	return nil
}

func WaitForTraefikDeploymentForACME(kubectl core.KubectlRunner) error {
	return waitForTraefikDeploymentForACME(kubectl)
}

// preflightACMEHostnamesPort80 tries TCP dials to host:80 from the machine running setup. Failing does not block setup (Operator may be off-node); success helps confirm DNS and a listener before a long cert wait.
func preflightACMEHostnamesPort80(dnsNames []string) {
	for _, h := range dedupeHostnames(dnsNames) {
		if h == "" {
			continue
		}
		addr := net.JoinHostPort(h, "80")
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			core.Warn("From this host, could not open TCP to " + addr + " (" + err.Error() + "). Let's Encrypt will try from the public internet, so check DNS, firewall, and that Traefik listens on port 80. If the cluster is on another network, you can ignore this if port 80 is open publicly")
			continue
		}
		_ = c.Close()
		core.Info("TCP to " + addr + " succeeded from this host (a good sign for HTTP-01)")
	}
}

func PreflightACMEHostnamesPort80(dnsNames []string) {
	preflightACMEHostnamesPort80(dnsNames)
}

// ensureCertManagerInstalled applies upstream cert-manager if CRDs are missing and waits for deployments.
func ensureCertManagerInstalled(kubectl core.KubectlRunner, logger *zap.Logger) error {
	if err := checkCertManagerInstalledWithKubectl(kubectl); err == nil {
		core.Info("cert-manager already installed")
		return nil
	}
	core.Info(fmt.Sprintf("Installing cert-manager %s", certManagerRelease))
	warnMsg := "If this fails (no network), install cert-manager manually, then re-run setup with --skip-cert-manager-install"
	core.Warn(warnMsg)
	url := certManagerInstallManifestURL()
	// #nosec G204 -- fixed release URL.
	if err := kubectl.RunWithOutput([]string{"apply", "-f", url}, os.Stdout, os.Stderr); err != nil {
		wrapped := core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager install failed: %v. %s", err, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "cert-manager install failed")
		}
		return wrapped
	}
	overall := 5 * time.Minute
	start := time.Now()
	core.Info(fmt.Sprintf("Waiting for cert-manager deployments (combined timeout %s across three deployments)", overall))
	for _, dep := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
		remaining := time.Until(start.Add(overall))
		if remaining <= 0 {
			msg := fmt.Sprintf("timed out waiting for cert-manager before deployment/%s", dep)
			err := core.NewWithSentinel(core.ErrCertManagerInstallFailed, msg)
			core.Error("cert-manager did not become ready")
			if logger != nil {
				core.LogStructuredError(logger, err, "cert-manager did not become ready")
			}
			return err
		}
		// #nosec G204 -- fixed deployment name; timeout is remaining wall-clock budget.
		if err := kubectl.RunWithOutput([]string{
			"wait", "--for=condition=Available",
			"deployment/" + dep, "-n", certManagerNamespace,
			"--timeout=" + remaining.Round(time.Second).String(),
		}, os.Stdout, os.Stderr); err != nil {
			wrapped := core.WrapWithSentinel(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager component %s not ready: %v", dep, err))
			core.Error("cert-manager did not become ready")
			if logger != nil {
				core.LogStructuredError(logger, wrapped, "cert-manager did not become ready")
			}
			return wrapped
		}
	}
	core.Info("cert-manager is ready")
	return nil
}

func EnsureCertManagerInstalled(kubectl core.KubectlRunner, logger *zap.Logger) error {
	return ensureCertManagerInstalled(kubectl, logger)
}

func applyLetsEncryptClusterIssuer(kubectl core.KubectlRunner, email string, staging bool, logger *zap.Logger) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return core.NewWithSentinel(core.ErrCertACMEEmailRequired, "ACME email is required")
	}
	name := ClusterIssuerNameForACME(staging)
	manifest := renderLetsEncryptClusterIssuerManifest(name, email, acmeServerURL(staging))
	if err := kube.ApplyManifestContent(kubectl.CommandArgs, manifest); err != nil {
		wrapped := core.WrapWithSentinel(core.ErrClusterIssuerApplyFailed, err, fmt.Sprintf("failed to apply Let's Encrypt ClusterIssuer: %v", err))
		core.Error("Failed to apply ClusterIssuer")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "Failed to apply ClusterIssuer")
		}
		return wrapped
	}
	return nil
}

func ApplyLetsEncryptClusterIssuer(kubectl core.KubectlRunner, email string, staging bool, logger *zap.Logger) error {
	return applyLetsEncryptClusterIssuer(kubectl, email, staging, logger)
}

func renderLetsEncryptClusterIssuerManifest(name, email, serverURL string) string {
	var b strings.Builder
	b.WriteString("apiVersion: cert-manager.io/v1\n")
	b.WriteString("kind: ClusterIssuer\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: ")
	b.WriteString(name)
	b.WriteString("\n")
	b.WriteString("spec:\n")
	b.WriteString("  acme:\n")
	b.WriteString("    email: ")
	b.WriteString(strconv.Quote(email))
	b.WriteString("\n")
	b.WriteString("    server: ")
	b.WriteString(strconv.Quote(serverURL))
	b.WriteString("\n")
	b.WriteString("    privateKeySecretRef:\n")
	b.WriteString("      name: ")
	b.WriteString(name)
	b.WriteString("-account-key\n")
	b.WriteString("    solvers:\n")
	b.WriteString("      - http01:\n")
	b.WriteString("          ingress:\n")
	b.WriteString("            ingressClassName: traefik\n")
	return b.String()
}

func RenderLetsEncryptClusterIssuerManifest(name, email, serverURL string) string {
	return renderLetsEncryptClusterIssuerManifest(name, email, serverURL)
}

func applyRegistryCertificate(kubectl core.KubectlRunner, dnsNames, ipAddresses []string, issuerName string) error {
	return applyCertificate(kubectl, registryCertificateName, registryTLSSecretName, dnsNames, ipAddresses, issuerName)
}

func applyRegistryInternalCertificate(kubectl core.KubectlRunner, dnsNames, ipAddresses []string, issuerName string) error {
	return applyCertificate(kubectl, registryInternalCertificateName, registryInternalTLSSecretName, dnsNames, ipAddresses, issuerName)
}

func ApplyRegistryInternalCertificate(kubectl core.KubectlRunner, dnsNames, ipAddresses []string, issuerName string) error {
	return applyRegistryInternalCertificate(kubectl, dnsNames, ipAddresses, issuerName)
}

func applyCertificate(kubectl core.KubectlRunner, certName, secretName string, dnsNames, ipAddresses []string, issuerName string) error {
	uniq := dedupeHostnames(dnsNames)
	uniqIPs := dedupeHostnames(ipAddresses)
	if len(uniq) == 0 && len(uniqIPs) == 0 {
		return core.NewWithSentinel(core.ErrCertCertificateSANsEmpty, fmt.Sprintf("%s TLS has no DNS names or IP addresses to request", certName))
	}
	manifest := renderCertificate(certName, secretName, core.NamespaceRegistry, uniq, uniqIPs, issuerName)
	return kube.ApplyManifestContent(kubectl.CommandArgs, manifest)
}

func ApplyRegistryCertificate(kubectl core.KubectlRunner, dnsNames, ipAddresses []string, issuerName string) error {
	return applyRegistryCertificate(kubectl, dnsNames, ipAddresses, issuerName)
}

func applyRegistryCertificateForACME(kubectl core.KubectlRunner, dnsNames []string, issuerName string) error {
	return applyRegistryCertificate(kubectl, dnsNames, nil, issuerName)
}

func ApplyRegistryCertificateForACME(kubectl core.KubectlRunner, dnsNames []string, issuerName string) error {
	return applyRegistryCertificateForACME(kubectl, dnsNames, issuerName)
}

func dedupeHostnames(hs []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, h := range hs {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

func renderCertificate(certName, secretName, namespace string, dnsNames, ipAddresses []string, issuerName string) string {
	uniq := dedupeHostnames(dnsNames)
	uniqIPs := dedupeHostnames(ipAddresses)
	var b strings.Builder
	b.WriteString("apiVersion: cert-manager.io/v1\n")
	b.WriteString("kind: Certificate\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: ")
	b.WriteString(certName)
	b.WriteString("\n")
	b.WriteString("  namespace: ")
	b.WriteString(namespace)
	b.WriteString("\n")
	b.WriteString("spec:\n")
	b.WriteString("  secretName: ")
	b.WriteString(secretName)
	b.WriteString("\n")
	b.WriteString("  issuerRef:\n")
	b.WriteString("    name: ")
	b.WriteString(issuerName)
	b.WriteString("\n")
	b.WriteString("    kind: ClusterIssuer\n")
	if len(uniq) > 0 {
		b.WriteString("  dnsNames:\n")
		for _, name := range uniq {
			b.WriteString("    - ")
			b.WriteString(strconv.Quote(name))
			b.WriteString("\n")
		}
	}
	if len(uniqIPs) > 0 {
		b.WriteString("  ipAddresses:\n")
		for _, ip := range uniqIPs {
			b.WriteString("    - ")
			b.WriteString(strconv.Quote(ip))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func RenderRegistryCertificate(certName, secretName string, dnsNames, ipAddresses []string, issuerName string) string {
	return renderCertificate(certName, secretName, core.NamespaceRegistry, dnsNames, ipAddresses, issuerName)
}

// RenderCertificate renders a cert-manager Certificate in an arbitrary
// namespace. It is used for namespace-local ingress certificates whose Secret
// cannot be referenced from another namespace.
func RenderCertificate(certName, secretName, namespace string, dnsNames, ipAddresses []string, issuerName string) string {
	return renderCertificate(certName, secretName, namespace, dnsNames, ipAddresses, issuerName)
}
