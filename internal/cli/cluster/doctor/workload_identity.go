package doctor

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/workloadidentity"
)

const workloadIdentityExpiryWarning = 30 * 24 * time.Hour

type publicCertificate struct {
	Subject string
	Issuer  string
	Expires time.Time
	Hash    string
}

// CheckWorkloadIdentity reports the deployed adapter-certificate configuration
// and its state for one server. It reads only selected configuration values and
// public CA certificate fields; it never reads a Secret's private-key field or
// prints Secret data.
func CheckWorkloadIdentity(kubectl core.KubectlRunner, namespace, server string) DoctorCheck {
	const name = "workload adapter identity"
	namespace = strings.TrimSpace(namespace)
	server = strings.TrimSpace(server)
	if len(validation.IsDNS1123Label(namespace)) > 0 || len(validation.IsDNS1123Subdomain(server)) > 0 {
		return DoctorCheck{Name: name, OK: false, Detail: "server must be supplied as a namespace and name", Remedy: "use `mcp-runtime cluster identity status --server <namespace>/<name>`"}
	}

	operatorNS := "mcp-runtime"
	operatorDeployment := "mcp-runtime-operator-controller-manager"
	apiNS := componentNamespace("runtime-api")
	operator, err := readIdentityDeploymentConfig(kubectl, operatorNS, operatorDeployment, "manager", []string{"MCP_ADAPTER_CERTIFICATES", "MCP_MTLS_CLUSTER_ISSUER", "MCP_TRUST_DOMAIN", "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("cannot read the operator's workload identity configuration: %v", err), Remedy: "check the operator Deployment and its declared ConfigMap sources"}
	}
	api, err := readIdentityDeploymentConfig(kubectl, apiNS, "mcp-runtime-api", "runtime-api", []string{"MCP_MTLS_CLUSTER_ISSUER", "MCP_TRUST_DOMAIN"})
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("cannot read runtime API workload identity configuration: %v", err), Remedy: "check the runtime API Deployment and its declared ConfigMap sources"}
	}

	enabled, err := parseAdapterCertificatesEnabled(operator["MCP_ADAPTER_CERTIFICATES"])
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: "operator MCP_ADAPTER_CERTIFICATES value is invalid", Remedy: "set MCP_ADAPTER_CERTIFICATES to true or false and rerun setup"}
	}
	configDetail := fmt.Sprintf("operator: adapter certificates %s, issuer %q, trust domain %q; runtime API: issuer %q, trust domain %q", onOff(enabled), displayUnset(operator["MCP_MTLS_CLUSTER_ISSUER"]), displayUnset(operator["MCP_TRUST_DOMAIN"]), displayUnset(api["MCP_MTLS_CLUSTER_ISSUER"]), displayUnset(api["MCP_TRUST_DOMAIN"]))
	if operator["MCP_MTLS_CLUSTER_ISSUER"] != api["MCP_MTLS_CLUSTER_ISSUER"] || operator["MCP_TRUST_DOMAIN"] != api["MCP_TRUST_DOMAIN"] {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; operator/runtime API configuration drift detected", Remedy: "align the workload issuer and trust domain in MCP_MTLS_CLUSTER_ISSUER and MCP_TRUST_DOMAIN, then restart the runtime API"}
	}
	if !enabled {
		return DoctorCheck{Name: name, OK: true, Detail: configDetail + "; client certificates are off by operator choice"}
	}
	if operator["MCP_MTLS_CLUSTER_ISSUER"] == "" || operator["MCP_TRUST_DOMAIN"] == "" {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; adapter certificates are enabled but the workload issuer or trust domain is missing", Remedy: "configure MCP_MTLS_CLUSTER_ISSUER and MCP_TRUST_DOMAIN, then update the operator"}
	}

	issuer := operator["MCP_MTLS_CLUSTER_ISSUER"]
	issuerReady, err := readKubectlOutput(kubectl, []string{"get", "clusterissuer", issuer, "-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"})
	if err != nil || strings.TrimSpace(issuerReady) != "True" {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; workload ClusterIssuer %q is not Ready", issuer), Remedy: "inspect `kubectl get clusterissuer " + issuer + " -o yaml` and repair its cert-manager configuration"}
	}

	// cert-manager's ca.crt contains the public signing chain for this server's
	// gateway certificate. Request that public field alone; never fetch tls.key.
	caPEM, err := readSecretField(kubectl, namespace, server+"-gateway-mtls", "ca.crt")
	if err != nil || strings.TrimSpace(caPEM) == "" {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; the selected server's workload CA certificate is not available yet", Remedy: "check the server Certificate readiness and cert-manager events"}
	}
	caCerts, err := parsePublicCertificates(caPEM)
	if err != nil || len(caCerts) == 0 {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; the selected server's public CA certificate could not be parsed", Remedy: "check the selected server's gateway Certificate and ca.crt"}
	}
	caDescription := describePublicCertificates(caCerts)
	if !time.Now().Before(earliestExpiry(caCerts).Add(-workloadIdentityExpiryWarning)) {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; workload CA expires within 30 days: " + caDescription, Remedy: "renew or replace the workload CA and update the trusted bundle before expiry"}
	}

	optionNamespace := strings.TrimSpace(operator["MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE"])
	bundleNamespace := optionNamespace
	bundleName := "mcp-adapter-client-ca"
	if optionNamespace == "" {
		optionNamespace = namespace
		bundleNamespace = namespace
		bundleName = server + "-mtls-ca"
	}
	route, err := readIdentityObject(kubectl, "ingressroute.traefik.io", namespace, server)
	if err != nil || route.GetName() != server {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; selected server route %s/%s is not an active Traefik IngressRoute", namespace, server), Remedy: "check MCPServer reconciliation and its ingress route"}
	}
	optionNamespace, optionName, err := workloadidentity.SelectedTLSOption(route, operator["MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE"])
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; " + err.Error(), Remedy: "check the selected IngressRoute TLS configuration"}
	}
	option, err := readIdentityObject(kubectl, "tlsoption.traefik.io", optionNamespace, optionName)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; route TLSOption %s/%s is unreadable", optionNamespace, optionName), Remedy: "check the selected route TLSOption and read access"}
	}
	trustedNames, err := workloadidentity.ClientAuthCASecrets(option)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; route TLSOption %s/%s does not request a verifiable client certificate", optionNamespace, optionName), Remedy: "check the operator's adapter-certificate reconciliation and Traefik TLSOption"}
	}
	if optionNamespace != bundleNamespace || !containsName(strings.Join(trustedNames, " "), bundleName) {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; route TLSOption %s/%s does not trust the expected workload bundle %s/%s", optionNamespace, optionName, bundleNamespace, bundleName), Remedy: "reconcile the workload CA bundle and ensure the route TLSOption references it"}
	}
	trustedPEM, err := readSecretField(kubectl, bundleNamespace, bundleName, "tls.ca")
	if err != nil || strings.TrimSpace(trustedPEM) == "" {
		trustedPEM, err = readSecretField(kubectl, bundleNamespace, bundleName, "ca.crt")
	}
	if err != nil || strings.TrimSpace(trustedPEM) == "" {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; trusted workload bundle %s/%s has no readable public CA certificate", bundleNamespace, bundleName), Remedy: "check gateway certificate issuance and workload bundle reconciliation"}
	}
	trustedCerts, err := parsePublicCertificates(trustedPEM)
	if err != nil || len(trustedCerts) == 0 {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; trusted workload bundle contains no parseable public certificate", Remedy: "reconcile the workload CA bundle from the gateway certificate ca.crt"}
	}
	if !containsAllHashes(certificateHashes(caCerts), certificateHashes(trustedCerts)) {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + "; workload CA and selected route trust bundle fingerprints differ (CA: " + fingerprintList(caCerts) + "; bundle: " + fingerprintList(trustedCerts) + ")", Remedy: "reconcile the selected server's trusted CA bundle and verify the platform TLSOption references the same workload CA"}
	}

	return DoctorCheck{Name: name, OK: true, Detail: configDetail + fmt.Sprintf("; issuer %q Ready; route %s/%s verifies client certificates; workload CA: %s; trusted bundle SHA-256: %s", issuer, optionNamespace, optionName, caDescription, bundleFingerprint(trustedPEM))}
}

func readIdentityObject(kubectl core.KubectlRunner, kind, namespace, name string) (*unstructured.Unstructured, error) {
	raw, err := readKubectlOutput(kubectl, []string{"get", kind, name, "-n", namespace, "-o", "json"})
	if err != nil {
		return nil, err
	}
	var obj unstructured.Unstructured
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("cannot decode %s %s/%s", kind, namespace, name)
	}
	return &obj, nil
}

func readIdentityDeploymentConfig(kubectl core.KubectlRunner, namespace, deploymentName, container string, keys []string) (map[string]string, error) {
	raw, err := readKubectlOutput(kubectl, []string{"get", "deployment", deploymentName, "-n", namespace, "-o", "json"})
	if err != nil {
		return nil, err
	}
	var deployment appsv1.Deployment
	if err := json.Unmarshal([]byte(raw), &deployment); err != nil {
		return nil, fmt.Errorf("cannot decode Deployment %s/%s", namespace, deploymentName)
	}
	if deployment.Generation > deployment.Status.ObservedGeneration {
		return nil, fmt.Errorf("deployment configuration is not yet observed")
	}
	return workloadidentity.DeploymentConfig(context.Background(), &deployment, container, keys, func(_ context.Context, namespace, name string) (*corev1.ConfigMap, error) {
		raw, err := readKubectlOutput(kubectl, []string{"get", "configmap", name, "-n", namespace, "--ignore-not-found", "-o", "json"})
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(raw) == "" {
			return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
		}
		var cm corev1.ConfigMap
		if err := json.Unmarshal([]byte(raw), &cm); err != nil {
			return nil, fmt.Errorf("cannot decode ConfigMap %s/%s", namespace, name)
		}
		return &cm, nil
	})
}

func readSecretField(kubectl core.KubectlRunner, namespace, name, key string) (string, error) {
	field := strings.ReplaceAll(key, ".", `\.`)
	encoded, err := readKubectlOutput(kubectl, []string{"get", "secret", name, "-n", namespace, "-o", "jsonpath={.data." + field + "}"})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(encoded) == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", fmt.Errorf("decode public certificate field: %w", err)
	}
	return string(decoded), nil
}

func parseAdapterCertificatesEnabled(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, fmt.Errorf("invalid boolean")
	}
}

func onOff(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func displayUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not configured"
	}
	return value
}

func containsName(raw, target string) bool {
	for _, name := range strings.Fields(raw) {
		if name == target {
			return true
		}
	}
	return false
}

func parsePublicCertificates(raw string) ([]publicCertificate, error) {
	var certs []publicCertificate
	remaining := []byte(raw)
	for len(strings.TrimSpace(string(remaining))) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			return nil, fmt.Errorf("invalid PEM certificate bundle")
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public CA certificate: %w", err)
		}
		digest := sha256.Sum256(cert.Raw)
		certs = append(certs, publicCertificate{Subject: cert.Subject.String(), Issuer: cert.Issuer.String(), Expires: cert.NotAfter, Hash: hex.EncodeToString(digest[:])})
	}
	return certs, nil
}

func describePublicCertificates(certs []publicCertificate) string {
	if len(certs) == 0 {
		return "unavailable"
	}
	first := certs[0]
	return fmt.Sprintf("subject=%q issuer=%q SHA-256=%s expires=%s", first.Subject, first.Issuer, first.Hash, earliestExpiry(certs).UTC().Format(time.RFC3339))
}

func earliestExpiry(certs []publicCertificate) time.Time {
	earliest := certs[0].Expires
	for _, cert := range certs[1:] {
		if cert.Expires.Before(earliest) {
			earliest = cert.Expires
		}
	}
	return earliest
}

func certificateHashes(certs []publicCertificate) []string {
	hashes := make([]string, 0, len(certs))
	for _, cert := range certs {
		hashes = append(hashes, cert.Hash)
	}
	sort.Strings(hashes)
	return hashes
}

func containsAllHashes(expected, actual []string) bool {
	actualSet := make(map[string]bool, len(actual))
	for _, hash := range actual {
		actualSet[hash] = true
	}
	for _, hash := range expected {
		if !actualSet[hash] {
			return false
		}
	}
	return len(expected) > 0
}

func fingerprintList(certs []publicCertificate) string {
	return strings.Join(certificateHashes(certs), ",")
}

func bundleFingerprint(pemBundle string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(pemBundle)))
	return hex.EncodeToString(hash[:])
}
