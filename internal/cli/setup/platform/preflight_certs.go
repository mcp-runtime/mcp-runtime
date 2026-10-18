package platform

import (
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/metadata"
	"strings"
)

// checkCertManagerCRDs ensures cert-manager CRDs are installed when
// --skip-cert-manager-install is set. Without them every TLS step fails.
func checkCertManagerCRDs(ctx context.Context, clients *k8sclient.Clients, installCertManager bool) []preflightIssue {
	if installCertManager {
		return nil // setup will install cert-manager itself
	}
	if err := k8sclient.CheckCRDExists(ctx, clients, "certificates.cert-manager.io"); err != nil {
		return []preflightIssue{{
			fatal:   true,
			message: "cert-manager CRDs not found, but --skip-cert-manager-install is set. Install cert-manager before running setup.",
			cleanup: []string{
				"helm repo add jetstack https://charts.jetstack.io --force-update",
				"helm install cert-manager jetstack/cert-manager -n cert-manager --create-namespace --set crds.enabled=true",
				"# or omit --skip-cert-manager-install / MCP_SETUP_SKIP_CERT_MANAGER_INSTALL to let setup install it",
			},
		}}
	}
	return nil
}

// checkClusterIssuerExists verifies that a named ClusterIssuer exists when
// --tls-cluster-issuer is set. A missing issuer causes the TLS step to fail.
func checkClusterIssuerExists(ctx context.Context, clients *k8sclient.Clients, issuerName string) []preflightIssue {
	if strings.TrimSpace(issuerName) == "" {
		return nil
	}
	if err := k8sclient.CheckClusterIssuer(ctx, clients, issuerName); err != nil {
		if apierrors.IsNotFound(err) {
			return []preflightIssue{{
				fatal:   true,
				message: fmt.Sprintf("ClusterIssuer %q not found — it must exist before setup configures TLS.", issuerName),
				cleanup: []string{
					fmt.Sprintf("# Create the ClusterIssuer %q and then re-run setup.", issuerName),
					"# See: https://cert-manager.io/docs/configuration/",
				},
			}}
		}
	}
	return nil
}

// checkStaleRegistryCertificate detects a previously-failed Certificate whose
// DNS names no longer match the expected registry host. cert-manager will not
// re-issue for the new host until the old object is removed, causing a 5m
// timeout during the TLS step.
func checkStaleRegistryCertificate(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	dnsNames, err := k8sclient.GetCertificateDNSNames(ctx, clients, core.NamespaceRegistry, certmanager.RegistryCertificateName)
	if err != nil || len(dnsNames) == 0 {
		return nil
	}

	// Use metadata.ResolveRegistryHost() (reads env directly) instead of
	// core.GetRegistryIngressHost() (reads the init-time DefaultCLIConfig snapshot)
	// so tests using t.Setenv and setup runs after --env-file loading see the
	// correct value.
	expectedHost := metadata.ResolveRegistryHost()
	if expectedHost == "" || containsString(dnsNames, expectedHost) {
		return nil
	}

	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Stale Certificate %q in namespace %q has DNS names %v but the expected registry host is %q.\n"+
				"         cert-manager will not re-issue until this Certificate is removed.",
			certmanager.RegistryCertificateName, core.NamespaceRegistry, dnsNames, expectedHost,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificate -n %s %s", core.NamespaceRegistry, certmanager.RegistryCertificateName),
			fmt.Sprintf("kubectl delete certificaterequest -n %s --all", core.NamespaceRegistry),
		},
	}}
}

// checkFailedCertificateRequests warns about failed CertificateRequests in the
// registry namespace. They don't block re-issuance on their own but can delay
// it; cleaning them up ensures cert-manager creates a fresh request immediately.
func checkFailedCertificateRequests(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	failed, _ := k8sclient.ListFailedCertificateRequestNames(ctx, clients, core.NamespaceRegistry)
	if len(failed) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"%d failed CertificateRequest(s) in namespace %q (%s) — removing them ensures cert-manager creates a fresh request immediately.",
			len(failed), core.NamespaceRegistry, strings.Join(failed, ", "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificaterequest -n %s --all", core.NamespaceRegistry),
		},
	}}
}

// checkConflictingSecretOwners detects when the registry-tls Secret is claimed
// by more than one Certificate. cert-manager refuses to overwrite a Secret
// owned by a different Certificate, blocking TLS setup silently.
func checkConflictingSecretOwners(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	owners, err := k8sclient.CertificateOwnersForSecret(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || len(owners) <= 1 {
		return nil
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Secret %q in namespace %q is claimed by multiple Certificates %v — cert-manager cannot update it.",
			certmanager.RegistryTLSSecretName, core.NamespaceRegistry, owners,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificate -n %s --all", core.NamespaceRegistry),
			fmt.Sprintf("kubectl delete secret -n %s %s", core.NamespaceRegistry, certmanager.RegistryTLSSecretName),
		},
	}}
}

// checkRegistryHostForTLS verifies that the registry hostname is a real public
// name. When MCP_PLATFORM_DOMAIN is unset and no explicit ingress host vars are
// configured, GetRegistryIngressHost returns "registry.local" which Let's
// Encrypt (and any real CA) will refuse to issue a certificate for.
func checkRegistryHostForTLS() []preflightIssue {
	// Read directly from env so this check isn't affected by the DefaultCLIConfig
	// init-time snapshot (same reason we reload DefaultCLIConfig after loadEnvFile).
	host := metadata.ResolveRegistryHost()
	if host != "" && !isDevRegistryURL(host) {
		return nil
	}
	displayed := host
	if displayed == "" {
		displayed = "(empty)"
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Registry host is %q — a real public hostname is required for TLS certificate issuance.\n"+
				"         Set MCP_PLATFORM_DOMAIN (e.g. mcpruntime.org) or MCP_REGISTRY_INGRESS_HOST in your env file.",
			displayed,
		),
		cleanup: []string{
			"# In your env file:",
			"export MCP_PLATFORM_DOMAIN=your-domain.example.com",
			"# or",
			"export MCP_REGISTRY_INGRESS_HOST=registry.your-domain.example.com",
		},
	}}
}

// checkOrphanedRegistryTLSSecret warns when the registry-tls Secret exists but
// no cert-manager Certificate claims it. cert-manager will not overwrite an
// unowned Secret, so TLS setup silently skips re-issuance, leaving a stale cert.
func checkOrphanedRegistryTLSSecret(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	exists, err := k8sclient.SecretExists(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || !exists {
		return nil
	}
	owners, err := k8sclient.CertificateOwnersForSecret(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || len(owners) > 0 {
		return nil // has owner(s) — handled by checkConflictingSecretOwners if >1
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Secret %q in namespace %q exists but is not owned by any cert-manager Certificate.\n"+
				"         cert-manager will not update it — delete the secret so setup can issue a fresh one.",
			certmanager.RegistryTLSSecretName, core.NamespaceRegistry,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete secret -n %s %s", core.NamespaceRegistry, certmanager.RegistryTLSSecretName),
		},
	}}
}
