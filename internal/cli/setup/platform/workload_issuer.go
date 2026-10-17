package platform

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

// Production delegates signing-key custody, encrypted backup, recovery and
// rollover to a dedicated enterprise issuer. It never repurposes the bundled
// registry root, even through a differently named CA ClusterIssuer.
func validateProductionWorkloadIssuer(plan setupplan.Plan, now time.Time) error {
	if err := ValidateMTLSSetupCLIFlags(plan.TestMode, plan.TLSEnabled, plan.MTLSClusterIssuer); err != nil {
		return err
	}
	issuerName := strings.TrimSpace(plan.MTLSClusterIssuer)
	if plan.TestMode || issuerName == "" {
		return nil
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()
	issuer, err := clients.Dynamic.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}).Get(ctx, issuerName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read dedicated workload ClusterIssuer %q before TLS setup: %w", issuerName, err)
	}
	conditions, _, _ := unstructured.NestedSlice(issuer.Object, "status", "conditions")
	ready := false
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		observed, _, _ := unstructured.NestedInt64(condition, "observedGeneration")
		if condition["type"] == "Ready" && condition["status"] == "True" && observed >= issuer.GetGeneration() {
			ready = true
		}
	}
	if !ready {
		return fmt.Errorf("dedicated workload ClusterIssuer %q must be Ready for its current generation before TLS setup", issuerName)
	}
	registrySecretName := certmanager.CertCASecretName
	if plan.RegistryMode == setupplan.RegistryModeBundledHTTPS && strings.TrimSpace(plan.TLSClusterIssuer) != "" {
		registryIssuerName := strings.TrimSpace(plan.TLSClusterIssuer)
		registryIssuer, err := clients.Dynamic.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}).Get(ctx, registryIssuerName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("inspect registry issuer before accepting workload root: %w", err)
		}
		if _, acme, _ := unstructured.NestedFieldNoCopy(registryIssuer.Object, "spec", "acme"); !acme {
			if registryIssuerName == issuerName {
				return fmt.Errorf("workload identity and internal registry TLS must use separate signing roots and ClusterIssuers")
			}
			if name, _, _ := unstructured.NestedString(registryIssuer.Object, "spec", "ca", "secretName"); name != "" {
				registrySecretName = name
			}
		}
	}
	secretName, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName")
	if secretName == "" {
		// Externally backed enterprise issuers own their key health and custody.
		// ACME/server-TLS and self-signed issuers do not provide this contract.
		if _, exists, _ := unstructured.NestedFieldNoCopy(issuer.Object, "spec", "acme"); exists {
			return fmt.Errorf("workload ClusterIssuer %q must use a dedicated CA or enterprise signer, not ACME", issuerName)
		}
		if _, exists, _ := unstructured.NestedFieldNoCopy(issuer.Object, "spec", "selfSigned"); exists {
			return fmt.Errorf("workload ClusterIssuer %q must use a dedicated CA or enterprise signer, not selfSigned", issuerName)
		}
		if _, vault, _ := unstructured.NestedFieldNoCopy(issuer.Object, "spec", "vault"); !vault {
			if _, venafi, _ := unstructured.NestedFieldNoCopy(issuer.Object, "spec", "venafi"); !venafi {
				return fmt.Errorf("workload ClusterIssuer %q has no supported dedicated signer", issuerName)
			}
		}
		return nil
	}
	if secretName == certmanager.CertCASecretName || secretName == registrySecretName {
		return fmt.Errorf("workload ClusterIssuer %q shares the bundled registry CA Secret; provision a separate workload signing root and preserve registry trust", issuerName)
	}
	secret, err := clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read dedicated workload CA Secret before TLS setup: %w", err)
	}
	health, err := certmanager.ValidateCAKeyPair(secret.Data["tls.crt"], secret.Data["tls.key"], now)
	if err != nil {
		return fmt.Errorf("dedicated workload CA is invalid: %w", err)
	}
	if health.NearExpiry {
		return core.NewWithBase(core.ErrCANearExpiry, "dedicated workload CA has under 180 days remaining; complete the enterprise dual-trust rotation before setup")
	}
	workloadChain, err := caSigningChain(secret.Data["tls.crt"], now)
	if err != nil {
		return fmt.Errorf("cannot verify independent workload signing root: %w", err)
	}
	if workloadChain[len(workloadChain)-1].NotAfter.Sub(now) < certmanager.MinCARemainingLifetime {
		return core.NewWithBase(core.ErrCANearExpiry, "dedicated workload signing root has under 180 days remaining")
	}
	registryCA, err := clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Get(ctx, registrySecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify workload and bundled registry signing-root separation: %w", err)
	}
	registryChain, err := caSigningChain(registryCA.Data["tls.crt"], now)
	if err != nil {
		return fmt.Errorf("cannot verify bundled registry CA separation: %w", err)
	}
	for _, workload := range workloadChain {
		for _, registry := range registryChain {
			if bytes.Equal(workload.RawSubjectPublicKeyInfo, registry.RawSubjectPublicKeyInfo) {
				return fmt.Errorf("workload and registry CAs share a signing key; provision independent workload signing keys throughout the chain")
			}
		}
	}
	return nil
}

func caSigningChain(data []byte, now time.Time) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	for remaining := bytes.TrimSpace(data); len(remaining) > 0; {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) || len(chain) >= 16 {
			return nil, fmt.Errorf("CA chain must contain only public certificates, at most 16")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("CA public certificate is missing or invalid")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("CA chain contains an invalid signing certificate")
		}
		chain = append(chain, cert)
		remaining = bytes.TrimSpace(rest)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("CA public certificate is missing")
	}
	root := chain[len(chain)-1]
	if !bytes.Equal(root.RawSubject, root.RawIssuer) || root.CheckSignatureFrom(root) != nil {
		return nil, fmt.Errorf("complete CA chain ending in its self-signed root is required to verify signing-root separation")
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	for i := 0; i < len(chain)-1; i++ {
		if chain[i].CheckSignatureFrom(chain[i+1]) != nil {
			return nil, fmt.Errorf("CA chain is incomplete or out of order")
		}
		if i > 0 {
			intermediates.AddCert(chain[i])
		}
	}
	if _, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("CA chain is not currently valid")
	}
	return chain, nil
}
