package doctor

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"mcp-runtime/internal/cli/core"
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
	apiNS := componentNamespace("platform-api")
	operator := map[string]string{}
	for _, key := range []string{"MCP_ADAPTER_CERTIFICATES", "MCP_MTLS_CLUSTER_ISSUER", "MCP_TRUST_DOMAIN", "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE"} {
		value, err := readKubectlOutput(kubectl, []string{"get", "deployment", operatorDeployment, "-n", operatorNS, "-o", "jsonpath={range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.value}{end}"})
		if err != nil {
			return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("cannot read the operator's %s configuration: %v", key, err), Remedy: "check read access to the mcp-runtime-operator-controller-manager Deployment"}
		}
		operator[key] = strings.TrimSpace(value)
	}

	api := map[string]string{}
	for _, key := range []string{"MCP_MTLS_CLUSTER_ISSUER", "MCP_TRUST_DOMAIN"} {
		value, err := readRuntimeAPIConfig(kubectl, apiNS, key)
		if err != nil {
			return DoctorCheck{Name: name, OK: false, Detail: fmt.Sprintf("cannot read runtime API %s configuration: %v", key, err), Remedy: "check read access to mcp-platform/configmap/mcp-shared-config"}
		}
		api[key] = strings.TrimSpace(value)
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
	optionName := "default"
	bundleNamespace := optionNamespace
	bundleName := "mcp-adapter-client-ca"
	if optionNamespace == "" {
		optionNamespace = namespace
		optionName = server + "-mtls"
		bundleNamespace = namespace
		bundleName = server + "-mtls-ca"
	}
	routeName, err := readKubectlOutput(kubectl, []string{"get", "ingressroute.traefik.io", server, "-n", namespace, "-o", "jsonpath={.metadata.name}"})
	if err != nil || strings.TrimSpace(routeName) != server {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; selected server route %s/%s is not an active Traefik IngressRoute", namespace, server), Remedy: "check MCPServer reconciliation and its ingress route"}
	}
	clientAuthType, err := readKubectlOutput(kubectl, []string{"get", "tlsoption.traefik.io", optionName, "-n", optionNamespace, "-o", "jsonpath={.spec.clientAuth.clientAuthType}"})
	if err != nil || !strings.Contains(clientAuthType, "VerifyClientCert") {
		return DoctorCheck{Name: name, OK: false, Detail: configDetail + fmt.Sprintf("; route TLSOption %s/%s does not request a verifiable client certificate", optionNamespace, optionName), Remedy: "check the operator's adapter-certificate reconciliation and Traefik TLSOption"}
	}
	trustedNames, err := readKubectlOutput(kubectl, []string{"get", "tlsoption.traefik.io", optionName, "-n", optionNamespace, "-o", "jsonpath={.spec.clientAuth.secretNames[*]}"})
	if err != nil || !containsName(trustedNames, bundleName) {
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

	return DoctorCheck{Name: name, OK: true, Detail: configDetail + fmt.Sprintf("; issuer %q Ready; route %s/%s requests optional client certificates; workload CA: %s; trusted bundle SHA-256: %s", issuer, optionNamespace, optionName, caDescription, bundleFingerprint(trustedPEM))}
}

// readRuntimeAPIConfig applies the same precedence as the container
// environment: explicit env entries override envFrom ConfigMaps. It reads only
// the named key from the selected ConfigMap and refuses Secret-backed values.
func readRuntimeAPIConfig(kubectl core.KubectlRunner, namespace, key string) (string, error) {
	base := []string{"get", "deployment", "mcp-runtime-api", "-n", namespace, "-o"}
	query := func(path string) (string, error) {
		args := append(append([]string(nil), base...), "jsonpath="+path)
		return readKubectlOutput(kubectl, args)
	}

	name, err := query("{range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.name}{end}")
	if err != nil {
		return "", fmt.Errorf("read runtime API environment declaration: %w", err)
	}
	if strings.TrimSpace(name) != "" {
		value, err := query("{range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.value}{end}")
		if err != nil {
			return "", fmt.Errorf("read runtime API environment value: %w", err)
		}
		secretRef, err := query("{range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.valueFrom.secretKeyRef.name}{end}")
		if err != nil {
			return "", fmt.Errorf("inspect runtime API environment source: %w", err)
		}
		if strings.TrimSpace(secretRef) != "" {
			return "", fmt.Errorf("%s is Secret-backed; refusing to inspect it", key)
		}
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		configMap, err := query("{range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.valueFrom.configMapKeyRef.name}{end}")
		if err != nil {
			return "", fmt.Errorf("inspect runtime API ConfigMap source: %w", err)
		}
		configMapKey, err := query("{range .spec.template.spec.containers[*].env[?(@.name==\"" + key + "\")]}{.valueFrom.configMapKeyRef.key}{end}")
		if err != nil {
			return "", fmt.Errorf("inspect runtime API ConfigMap key: %w", err)
		}
		if strings.TrimSpace(configMap) != "" && strings.TrimSpace(configMapKey) != "" {
			value, err = readKubectlOutput(kubectl, []string{"get", "configmap", strings.TrimSpace(configMap), "-n", namespace, "-o", "jsonpath={.data." + strings.TrimSpace(configMapKey) + "}"})
			return strings.TrimSpace(value), err
		}
		// A declared literal empty value intentionally overrides envFrom.
		return "", nil
	}

	// Current manifests supply these keys through mcp-shared-config's envFrom.
	// RuntimeAPI containers are non-shell distroless images, so the deployment
	// template and the referenced ConfigMap are the inspectable effective source.
	value, err := readKubectlOutput(kubectl, []string{"get", "configmap", "mcp-shared-config", "-n", namespace, "-o", "jsonpath={.data." + key + "}"})
	return strings.TrimSpace(value), err
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
