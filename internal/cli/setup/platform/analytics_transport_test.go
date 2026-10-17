package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/pkg/k8sclient"
)

func transportManifest(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func transportObjects(t *testing.T, manifest string) map[string]*unstructured.Unstructured {
	t.Helper()
	result := map[string]*unstructured.Unstructured{}
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		var data json.RawMessage
		err := decoder.Decode(&data)
		if err == io.EOF {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			continue
		}
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(data); err != nil {
			t.Fatal(err)
		}
		if len(object.Object) > 0 {
			result[object.GetKind()+"/"+object.GetName()] = object
		}
	}
}
func transportContainer(t *testing.T, object *unstructured.Unstructured) map[string]any {
	t.Helper()
	containers, _, _ := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		t.Fatal("workload has no container")
	}
	return containers[0].(map[string]any)
}
func transportEnv(container map[string]any) map[string]string {
	result := map[string]string{}
	values, _, _ := unstructured.NestedSlice(container, "env")
	for _, raw := range values {
		variable := raw.(map[string]any)
		if value, ok := variable["value"].(string); ok {
			result[variable["name"].(string)] = value
		}
	}
	return result
}

func TestAnalyticsTLSKafkaBrokerAndControllerStayEncryptedWithoutResizing(t *testing.T) {
	for _, name := range []string{"k8s/05-kafka.yaml", "k8s/05-kafka-hostpath.yaml"} {
		t.Run(name, func(t *testing.T) {
			base := transportManifest(t, name)
			rendered, err := renderAnalyticsTransportManifest(base)
			if err != nil {
				t.Fatal(err)
			}
			stateful := transportObjects(t, rendered)["StatefulSet/kafka"]
			container := transportContainer(t, stateful)
			env := transportEnv(container)
			if env["KAFKA_LISTENER_SECURITY_PROTOCOL_MAP"] != "CONTROLLER:SSL,SSL:SSL" || env["KAFKA_SSL_CLIENT_AUTH"] != "required" || env["KAFKA_INTER_BROKER_LISTENER_NAME"] != "SSL" || env["KAFKA_SSL_KEYSTORE_TYPE"] != "PKCS12" {
				t.Fatalf("Kafka has an unencrypted/unauthenticated listener: %v", env)
			}
			command, _, _ := unstructured.NestedStringSlice(container, "command")
			if strings.Contains(strings.Join(command, "\n"), "PLAINTEXT") {
				t.Fatal("broker advertises plaintext")
			}
			replicas, _, _ := unstructured.NestedInt64(stateful.Object, "spec", "replicas")
			if replicas != 3 || env["KAFKA_MIN_INSYNC_REPLICAS"] != "2" || env["CLUSTER_ID"] != "MkU3OEVBNTcwNTJENDM2Qk" {
				t.Fatal("TLS altered quorum, durability or cluster identity")
			}
			second, err := renderAnalyticsTransportManifest(rendered)
			if err != nil || second != rendered {
				t.Fatalf("TLS rendering is not repeatable: %v", err)
			}
		})
	}
}

func TestAnalyticsTLSUsesVerifiedClientsAndSecureClickHousePorts(t *testing.T) {
	for _, name := range []string{"k8s/03-clickhouse.yaml", "k8s/03-clickhouse-hostpath.yaml"} {
		rendered, err := renderAnalyticsTransportManifest(transportManifest(t, name))
		if err != nil {
			t.Fatal(err)
		}
		objects := transportObjects(t, rendered)
		container := transportContainer(t, objects["StatefulSet/clickhouse"])
		get, _, _ := unstructured.NestedMap(container, "readinessProbe", "httpGet")
		if get["scheme"] != "HTTPS" || get["port"] != int64(8443) {
			t.Fatalf("ClickHouse probe did not use TLS: %v", get)
		}
		ports, _, _ := unstructured.NestedSlice(objects["Service/clickhouse"].Object, "spec", "ports")
		for _, raw := range ports {
			port := raw.(map[string]any)
			if port["port"] == int64(9000) || port["port"] == int64(8123) {
				t.Fatal("plaintext ClickHouse port remains exposed")
			}
		}
	}
	for _, name := range []string{"k8s/04-clickhouse-init.yaml", "k8s/05-kafka-topic-init.yaml"} {
		rendered, err := renderAnalyticsTransportManifest(transportManifest(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(name, "clickhouse") && (!strings.Contains(rendered, "--secure --port=9440") || strings.Contains(rendered, "clickhouse-client --host=clickhouse")) {
			t.Fatal("schema initialization bypasses verified TLS")
		}
		if strings.Contains(name, "kafka") && (!strings.Contains(rendered, "--command-config /tmp/kafka-client.properties") || !strings.Contains(rendered, "ssl.endpoint.identification.algorithm=https")) {
			t.Fatal("topic initialization bypasses authenticated TLS")
		}
		second, err := renderAnalyticsTransportManifest(rendered)
		if err != nil || second != rendered {
			t.Fatalf("job TLS rendering is not repeatable: %v", err)
		}
	}
	config, err := renderAnalyticsTransportManifest(transportManifest(t, "k8s/01-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	data, _, _ := unstructured.NestedStringMap(transportObjects(t, config)["ConfigMap/mcp-shared-config"].Object, "data")
	if data["CLICKHOUSE_TLS_ENABLED"] != "true" || !strings.HasSuffix(data["CLICKHOUSE_ADDR"], ":9440") || !strings.HasPrefix(data["MCP_ANALYTICS_INGEST_URL"], "https://") {
		t.Fatalf("clients lost secure transport settings: %v", data)
	}
}

func fakeAnalyticsPKI(t *testing.T, root *corev1.Secret) (*kubernetesfake.Clientset, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	objects := []runtime.Object{}
	if root != nil {
		objects = append(objects, root)
	}
	cs := kubernetesfake.NewSimpleClientset(objects...)
	issuer := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": "mcp-runtime-ca"}, "spec": map[string]any{"ca": map[string]any{"secretName": "mcp-runtime-ca"}}}}
	dc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), issuer)
	dc.PrependReactor("create", "certificates", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		object.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
		return false, nil, nil
	})
	oldConfig, oldClients := platformSetupKubeconfig, newKubernetesClients
	platformSetupKubeconfig = ""
	newKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: cs, Dynamic: dc}, nil }
	t.Cleanup(func() { platformSetupKubeconfig, newKubernetesClients = oldConfig, oldClients })
	return cs, dc
}

func TestAnalyticsPKIPreservesSigningRootAndPublishesOnlyPublicMaterial(t *testing.T) {
	cert, key := caPEM(t, time.Now().AddDate(5, 0, 0))
	root := caSecret(append(append([]byte{}, cert...), key...), key)
	cs, dc := fakeAnalyticsPKI(t, root)
	if err := prepareAnalyticsTransportPKI(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, err := cs.CoreV1().Secrets("cert-manager").Get(context.Background(), "mcp-runtime-ca", metav1.GetOptions{})
	if err != nil || !bytes.Equal(current.Data["tls.key"], key) {
		t.Fatal("infrastructure root was replaced")
	}
	for _, namespace := range []string{"mcp-platform", "mcp-observability", "mcp-runtime"} {
		config, err := cs.CoreV1().ConfigMaps(namespace).Get(context.Background(), analyticsCAConfigMap, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if config.Data["ca.crt"] != string(cert) || strings.Contains(config.Data["ca.crt"], "PRIVATE KEY") || config.Data["clickhouse-client.xml"] == "" {
			t.Fatal("public trust leaked key material or lost client configuration")
		}
	}
	for _, expected := range analyticsTransportCertificates() {
		object, err := dc.Resource(analyticsCertificateGVR).Namespace("mcp-observability").Get(context.Background(), expected.name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if cn, _, _ := unstructured.NestedString(object.Object, "spec", "commonName"); cn != "" {
			t.Fatal("certificate added an unapproved commonName")
		}
		if isCA, _, _ := unstructured.NestedBool(object.Object, "spec", "isCA"); isCA {
			t.Fatal("service leaf requested CA signing authority")
		}
	}
}

func TestAnalyticsPKICannotReplaceMissingOrUnhealthyExistingRoot(t *testing.T) {
	for _, name := range []string{"missing", "expired", "near-expiry", "mismatched-key"} {
		t.Run(name, func(t *testing.T) {
			var root *corev1.Secret
			if name != "missing" {
				expiry := time.Now().AddDate(5, 0, 0)
				if name == "expired" {
					expiry = time.Now().Add(-time.Hour)
				}
				if name == "near-expiry" {
					expiry = time.Now().Add(24 * time.Hour)
				}
				cert, key := caPEM(t, expiry)
				if name == "mismatched-key" {
					_, key = caPEM(t, expiry)
				}
				root = caSecret(cert, key)
			}
			cs, dc := fakeAnalyticsPKI(t, root)
			if err := prepareAnalyticsTransportPKI(context.Background()); err == nil {
				t.Fatal("existing unhealthy infrastructure root accepted")
			}
			for _, action := range append(cs.Actions(), dc.Actions()...) {
				if action.GetVerb() != "get" {
					t.Fatalf("bad root caused a write: %v", action)
				}
			}
		})
	}
}

func TestAnalyticsPKIRejectsPlaintextQuorumBeforeAnyWrites(t *testing.T) {
	cert, key := caPEM(t, time.Now().AddDate(5, 0, 0))
	cs, dc := fakeAnalyticsPKI(t, caSecret(cert, key))
	_, err := cs.AppsV1().StatefulSets("mcp-observability").Create(context.Background(), &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "mcp-observability"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kafka", Env: []corev1.EnvVar{{Name: "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", Value: "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT"}}}}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cs.ClearActions()
	if err := prepareAnalyticsTransportPKI(context.Background()); err == nil || !strings.Contains(err.Error(), "fresh TLS installation") {
		t.Fatalf("plaintext quorum accepted: %v", err)
	}
	for _, action := range append(cs.Actions(), dc.Actions()...) {
		if action.GetVerb() != "get" {
			t.Fatalf("plaintext quorum caused a write: %v", action)
		}
	}
}
