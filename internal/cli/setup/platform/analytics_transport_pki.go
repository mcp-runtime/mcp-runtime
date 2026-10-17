package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

const analyticsCAConfigMap = "mcp-analytics-ca"
const analyticsKeystoreSecret = "mcp-analytics-keystore" // #nosec G101 -- Kubernetes object name.

var analyticsCertificateGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
var analyticsIssuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}

func setupAnalyticsTransportTLS(logger *zap.Logger, plan setupplan.Plan) error {
	if plan.TestMode || !plan.DeployAnalytics {
		return nil
	}
	if plan.InstallCertManager {
		if err := ensureCertManagerInstalledClientGo(logger); err != nil {
			return err
		}
	} else if err := checkCertManagerInstalledClientGo(); err != nil {
		return err
	}
	return prepareAnalyticsTransportPKI(context.Background())
}

// Infrastructure TLS uses the existing internal-registry CA, never the
// dedicated production workload-identity issuer. Existing roots are preserved.
func prepareAnalyticsTransportPKI(ctx context.Context) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if existing, err := clients.Clientset.AppsV1().StatefulSets(core.ComponentNamespace("kafka")).Get(ctx, "kafka", metav1.GetOptions{}); err == nil {
		secure := false
		for _, container := range existing.Spec.Template.Spec.Containers {
			if container.Name == "kafka" {
				for _, variable := range container.Env {
					if variable.Name == "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP" && variable.Value == "CONTROLLER:SSL,SSL:SSL" {
						secure = true
					}
				}
			}
		}
		if !secure {
			return fmt.Errorf("existing plaintext Kafka requires backup, a fresh TLS installation and restore; setup will not rewrite a running quorum's transport. Preserve CA/certificates and data; see docs/platform-services.md#analytics-transport-security")
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("inspect existing Kafka transport before PKI changes: %w", err)
	}
	issuer, issuerErr := clients.Dynamic.Resource(analyticsIssuerGVR).Get(ctx, certmanager.CertClusterIssuerName, metav1.GetOptions{})
	if issuerErr != nil && !apierrors.IsNotFound(issuerErr) {
		return fmt.Errorf("read infrastructure issuer: %w", issuerErr)
	}
	root, err := clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Get(ctx, certmanager.CertCASecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if issuerErr == nil {
			return fmt.Errorf("existing infrastructure issuer has lost its CA Secret; restore its encrypted backup instead of generating a new root")
		}
		manifest, generateErr := certmanager.RenderGeneratedCASecretManifest(time.Now())
		if generateErr != nil {
			return generateErr
		}
		var generated struct {
			Metadata   metav1.ObjectMeta `json:"metadata"`
			StringData map[string]string `json:"stringData"`
		}
		if err := json.Unmarshal([]byte(manifest), &generated); err != nil {
			return err
		}
		root = &corev1.Secret{ObjectMeta: generated.Metadata, Type: corev1.SecretTypeTLS, Data: map[string][]byte{}}
		for name, value := range generated.StringData {
			root.Data[name] = []byte(value)
		}
		root, err = clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Create(ctx, root, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("read or initialize infrastructure CA: %w", err)
	}
	health, err := certmanager.ValidateCAKeyPair(root.Data["tls.crt"], root.Data["tls.key"], time.Now())
	if err != nil || health.NearExpiry {
		return fmt.Errorf("infrastructure CA must be valid with at least 180 days remaining; restore or rotate it under the documented trust procedure")
	}
	publicCA, err := publicAnalyticsCA(root.Data["tls.crt"], root.Data["tls.key"])
	if err != nil {
		return err
	}
	if issuerErr == nil {
		name, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName")
		if name != certmanager.CertCASecretName {
			return fmt.Errorf("existing infrastructure issuer references another signer; refusing to overwrite it")
		}
	} else {
		issuer = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": certmanager.CertClusterIssuerName}, "spec": map[string]any{"ca": map[string]any{"secretName": certmanager.CertCASecretName}}}}
		if _, err := clients.Dynamic.Resource(analyticsIssuerGVR).Create(ctx, issuer, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	for _, component := range []string{"kafka", "platform-api", "operator"} {
		ns := core.ComponentNamespace(component)
		if component == "operator" {
			ns = core.NamespaceMCPRuntime
		}
		if err := ensureNamespaceWithLabels(ns, nil); err != nil {
			return err
		}
		if err := publishAnalyticsPublicCA(ctx, clients, ns, publicCA); err != nil {
			return err
		}
	}
	ns := core.ComponentNamespace("kafka")
	if err := ensureAnalyticsKeystorePassword(ctx, clients, ns); err != nil {
		return err
	}
	for _, spec := range analyticsTransportCertificates() {
		if err := ensureAnalyticsCertificate(ctx, clients, ns, spec); err != nil {
			return err
		}
	}
	for _, spec := range analyticsTransportCertificates() {
		if err := k8sclient.WaitForCertificateReady(ctx, clients, ns, spec.name, 5*time.Minute); err != nil {
			return fmt.Errorf("analytics TLS certificate %s is not Ready; check issuer approval policy and signer health: %w", spec.name, err)
		}
	}
	return nil
}

// Re-encode only parsed certificate DER. A malformed tls.crt containing a
// private PEM block must never copy that block into a public ConfigMap.
func publicAnalyticsCA(cert, key []byte) ([]byte, error) {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) == 0 || len(pair.Certificate) > 16 {
		return nil, fmt.Errorf("infrastructure CA certificate chain is invalid")
	}
	var public bytes.Buffer
	for _, der := range pair.Certificate {
		parsed, err := x509.ParseCertificate(der)
		if err != nil || !parsed.IsCA || !parsed.BasicConstraintsValid || parsed.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("infrastructure CA chain includes a non-signing certificate")
		}
		if err := pem.Encode(&public, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, err
		}
	}
	return public.Bytes(), nil
}

type analyticsCertificate struct {
	name   string
	dns    []string
	client bool
	server bool
	pkcs12 bool
}

func analyticsTransportCertificates() []analyticsCertificate {
	ns := core.ComponentNamespace("kafka")
	return []analyticsCertificate{
		{name: "mcp-analytics-kafka", dns: []string{"kafka", "kafka." + ns, "kafka." + ns + ".svc", clusterServiceDNS("kafka", ns), "*.kafka-headless." + ns + ".svc", "*." + clusterServiceDNS("kafka-headless", ns)}, client: true, server: true, pkcs12: true},
		{name: "mcp-analytics-clickhouse", dns: []string{"clickhouse", "clickhouse." + ns, "clickhouse." + ns + ".svc", clusterServiceDNS("clickhouse", ns)}, server: true},
		{name: "mcp-analytics-ingest", dns: []string{"mcp-ingest", "mcp-ingest." + ns, "mcp-ingest." + ns + ".svc", clusterServiceDNS("mcp-ingest", ns)}, server: true},
		// An empty subject needs a SAN for Java's X.509 parser. Keep commonName
		// absent, as required by the infrastructure approval policy.
		{name: "mcp-analytics-ingest-kafka", dns: []string{clusterServiceDNS("mcp-ingest", ns)}, client: true},
		{name: "mcp-analytics-processor-kafka", dns: []string{clusterServiceDNS("mcp-processor", ns)}, client: true},
		{name: "mcp-analytics-topic-init", dns: []string{clusterServiceDNS("kafka-topic-init", ns)}, client: true, pkcs12: true},
	}
}

func publishAnalyticsPublicCA(ctx context.Context, clients *k8sclient.Clients, namespace string, data []byte) error {
	cm, err := clients.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, analyticsCAConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = clients.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: analyticsCAConfigMap, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}, Data: map[string]string{"ca.crt": string(data), "clickhouse-server.xml": analyticsClickHouseServerXML, "clickhouse-client.xml": analyticsClickHouseClientXML}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		return fmt.Errorf("analytics CA ConfigMap is not managed by MCP Runtime")
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["ca.crt"] = string(data)
	cm.Data["clickhouse-server.xml"], cm.Data["clickhouse-client.xml"] = analyticsClickHouseServerXML, analyticsClickHouseClientXML
	_, err = clients.Clientset.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

const analyticsClickHouseServerXML = `<clickhouse>
  <tcp_port remove="remove"/><http_port remove="remove"/>
  <tcp_port_secure>9440</tcp_port_secure><https_port>8443</https_port>
  <openSSL><server>
    <certificateFile>/etc/mcp-analytics/server/tls.crt</certificateFile>
    <privateKeyFile>/etc/mcp-analytics/server/tls.key</privateKeyFile>
    <caConfig>/etc/mcp-analytics/ca/ca.crt</caConfig>
    <verificationMode>none</verificationMode>
    <disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols>
  </server></openSSL>
</clickhouse>`

const analyticsClickHouseClientXML = `<clickhouse><openSSL><client>
  <caConfig>/etc/mcp-analytics/ca/ca.crt</caConfig>
  <verificationMode>strict</verificationMode>
  <invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler>
  <disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols>
</client></openSSL></clickhouse>`

func ensureAnalyticsKeystorePassword(ctx context.Context, clients *k8sclient.Clients, namespace string) error {
	secret, err := clients.Clientset.CoreV1().Secrets(namespace).Get(ctx, analyticsKeystoreSecret, metav1.GetOptions{})
	if err == nil {
		if len(secret.Data["password"]) < 32 {
			return fmt.Errorf("analytics keystore password is missing or too short; restore the existing managed Secret")
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	_, err = clients.Clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: analyticsKeystoreSecret, Namespace: namespace}, Data: map[string][]byte{"password": []byte(base64.RawStdEncoding.EncodeToString(random[:]))}}, metav1.CreateOptions{})
	return err
}

func ensureAnalyticsCertificate(ctx context.Context, clients *k8sclient.Clients, namespace string, certificate analyticsCertificate) error {
	usages := []any{"digital signature"}
	if certificate.client {
		usages = append(usages, "client auth")
	}
	if certificate.server {
		usages = append(usages, "server auth")
	}
	spec := map[string]any{"secretName": certificate.name, "duration": "2160h", "renewBefore": "360h", "usages": usages, "issuerRef": map[string]any{"name": certmanager.CertClusterIssuerName, "kind": "ClusterIssuer", "group": "cert-manager.io"}, "privateKey": map[string]any{"algorithm": "ECDSA", "size": int64(256), "encoding": "PKCS8", "rotationPolicy": "Always"}}
	if len(certificate.dns) > 0 {
		names := make([]any, len(certificate.dns))
		for i, name := range certificate.dns {
			names[i] = name
		}
		spec["dnsNames"] = names
	}
	if certificate.pkcs12 {
		spec["keystores"] = map[string]any{"pkcs12": map[string]any{"create": true, "passwordSecretRef": map[string]any{"name": analyticsKeystoreSecret, "key": "password"}}}
	}
	resource := clients.Dynamic.Resource(analyticsCertificateGVR).Namespace(namespace)
	current, err := resource.Get(ctx, certificate.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": certificate.name, "namespace": namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "mcp-runtime"}}, "spec": spec}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if current.GetLabels()["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		return fmt.Errorf("analytics Certificate %s is not managed by MCP Runtime", certificate.name)
	}
	current.Object["spec"] = spec
	_, err = resource.Update(ctx, current, metav1.UpdateOptions{})
	return err
}
