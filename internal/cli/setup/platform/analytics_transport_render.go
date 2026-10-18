package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const analyticsTLSMount = "/etc/mcp-analytics"

// This transform is selected only by production-style TLS setup. Base manifests
// retain the explicit local test-mode path; no resource or request can opt a
// production deployment out after the setup plan selects TLS.
func renderAnalyticsTransportManifest(content string) (string, error) {
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096)
	var result bytes.Buffer
	ingestNamespace, hasIngestTransport := "", false
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(raw); err != nil {
			return "", err
		}
		object := u.Object
		if len(object) == 0 {
			continue
		}
		if u.GetKind() == "ServersTransport" && u.GetName() == "mcp-analytics-ingest" {
			hasIngestTransport = true
		}
		if u.GetKind() == "Service" && u.GetName() == "mcp-ingest" {
			ingestNamespace = u.GetNamespace()
			annotations := u.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["traefik.ingress.kubernetes.io/service.serversscheme"] = "https"
			annotations["traefik.ingress.kubernetes.io/service.serverstransport"] = ingestNamespace + "-mcp-analytics-ingest@kubernetescrd"
			u.SetAnnotations(annotations)
		}
		if u.GetKind() == "ConfigMap" && u.GetName() == "mcp-shared-config" {
			data, _, _ := unstructured.NestedStringMap(object, "data")
			if data == nil {
				data = map[string]string{}
			}
			data["CLICKHOUSE_ADDR"] = strings.ReplaceAll(data["CLICKHOUSE_ADDR"], ":9000", ":9440")
			data["CLICKHOUSE_TLS_ENABLED"], data["CLICKHOUSE_TLS_CA_FILE"] = "true", analyticsTLSMount+"/ca/ca.crt"
			data["KAFKA_TLS_ENABLED"], data["KAFKA_TLS_CA_FILE"] = "true", analyticsTLSMount+"/ca/ca.crt"
			data["KAFKA_TLS_CERT_FILE"], data["KAFKA_TLS_KEY_FILE"] = analyticsTLSMount+"/kafka/tls.crt", analyticsTLSMount+"/kafka/tls.key"
			data["MCP_ANALYTICS_INGEST_URL"] = strings.Replace(data["MCP_ANALYTICS_INGEST_URL"], "http://", "https://", 1)
			data["INTERNAL_ANALYTICS_TLS"] = "true"
			if err := unstructured.SetNestedStringMap(object, data, "data"); err != nil {
				return "", err
			}
		}
		if u.GetKind() == "Service" && u.GetName() == "clickhouse" {
			ports, _, _ := unstructured.NestedSlice(object, "spec", "ports")
			for _, raw := range ports {
				port := raw.(map[string]any)
				remapAnalyticsPort(port, "port")
				remapAnalyticsPort(port, "targetPort")
			}
			_ = unstructured.SetNestedSlice(object, ports, "spec", "ports")
		}
		if u.GetKind() == "NetworkPolicy" {
			remapAnalyticsPolicyPorts(object)
		}
		path := []string{"spec", "template", "spec"}
		if u.GetKind() == "Deployment" || u.GetKind() == "StatefulSet" || u.GetKind() == "Job" {
			pod, found, err := unstructured.NestedMap(object, path...)
			if err != nil {
				return "", err
			}
			if found {
				if err := wireAnalyticsPodTLS(pod, u.GetName()); err != nil {
					return "", err
				}
				if err := unstructured.SetNestedMap(object, pod, path...); err != nil {
					return "", err
				}
			}
		}
		data, err := json.Marshal(object)
		if err != nil {
			return "", err
		}
		if result.Len() > 0 {
			result.WriteString("\n---\n")
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	if ingestNamespace != "" && !hasIngestTransport {
		transport := map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "ServersTransport", "metadata": map[string]any{"name": "mcp-analytics-ingest", "namespace": ingestNamespace}, "spec": map[string]any{"serverName": clusterServiceDNS("mcp-ingest", ingestNamespace), "rootCAsSecrets": []string{"mcp-analytics-ingest"}}}
		data, err := json.Marshal(transport)
		if err != nil {
			return "", err
		}
		result.WriteString("\n---\n")
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.String(), nil
}

func wireAnalyticsPodTLS(pod map[string]any, name string) error {
	containers, _, _ := unstructured.NestedSlice(pod, "containers")
	usesCA := name == "clickhouse" || name == "clickhouse-init" || name == "mcp-ingest" || name == "mcp-processor" || name == "mcp-runtime-api" || name == "mcp-analytics-api" || name == "kafka-topic-init"
	if usesCA {
		addAnalyticsVolume(pod, map[string]any{"name": "analytics-ca", "configMap": map[string]any{"name": analyticsCAConfigMap}})
	}
	for _, raw := range containers {
		container := raw.(map[string]any)
		if usesCA {
			addAnalyticsMount(container, "analytics-ca", analyticsTLSMount+"/ca")
		}
		secureAnalyticsClickHouseEnv(container)
		switch name {
		case "kafka":
			setAnalyticsEnv(container, map[string]string{"KAFKA_LISTENERS": "SSL://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:SSL,SSL:SSL", "KAFKA_INTER_BROKER_LISTENER_NAME": "SSL", "KAFKA_SSL_CLIENT_AUTH": "required", "KAFKA_SSL_ENDPOINT_IDENTIFICATION_ALGORITHM": "https", "KAFKA_SSL_KEYSTORE_TYPE": "PKCS12", "KAFKA_SSL_TRUSTSTORE_TYPE": "PKCS12", "KAFKA_SSL_KEYSTORE_FILENAME": "keystore.p12", "KAFKA_SSL_TRUSTSTORE_FILENAME": "truststore.p12", "KAFKA_SSL_KEY_CREDENTIALS": "password", "KAFKA_SSL_KEYSTORE_CREDENTIALS": "password", "KAFKA_SSL_TRUSTSTORE_CREDENTIALS": "password"})
			rewriteAnalyticsCommand(container, "PLAINTEXT://${HOSTNAME}", "SSL://${HOSTNAME}")
			addAnalyticsVolume(pod, analyticsKeystoreVolume("mcp-analytics-kafka"))
			addAnalyticsMount(container, "analytics-keystore", "/etc/kafka/secrets")
		case "mcp-ingest", "mcp-processor":
			// #nosec G101 -- Kubernetes Secret object name, not credential material.
			secret := "mcp-analytics-ingest-kafka"
			if name == "mcp-processor" {
				// #nosec G101 -- Kubernetes Secret object name, not credential material.
				secret = "mcp-analytics-processor-kafka"
			}
			addAnalyticsVolume(pod, map[string]any{"name": "analytics-kafka-client", "secret": map[string]any{"secretName": secret, "defaultMode": int64(0440)}})
			addAnalyticsMount(container, "analytics-kafka-client", analyticsTLSMount+"/kafka")
			if name == "mcp-ingest" {
				setAnalyticsEnv(container, map[string]string{"HTTP_TLS_CERT_FILE": analyticsTLSMount + "/server/tls.crt", "HTTP_TLS_KEY_FILE": analyticsTLSMount + "/server/tls.key"})
				// #nosec G101 -- Kubernetes Secret object name, not credential material.
				addAnalyticsVolume(pod, map[string]any{"name": "analytics-server-tls", "secret": map[string]any{"secretName": "mcp-analytics-ingest", "defaultMode": int64(0440)}})
				addAnalyticsMount(container, "analytics-server-tls", analyticsTLSMount+"/server")
				setAnalyticsHTTPSProbes(container)
			}
		case "kafka-topic-init":
			addAnalyticsVolume(pod, analyticsKeystoreVolume("mcp-analytics-topic-init"))
			addAnalyticsMount(container, "analytics-keystore", "/etc/kafka/secrets")
			command, _, _ := unstructured.NestedStringSlice(container, "command")
			if len(command) == 0 {
				return fmt.Errorf("kafka topic initialization command is missing")
			}
			if !strings.Contains(command[len(command)-1], "/tmp/kafka-client.properties") {
				rewriteAnalyticsCommand(container, "kafka-topics ", "kafka-topics --command-config /tmp/kafka-client.properties ")
				command, _, _ = unstructured.NestedStringSlice(container, "command")
				command[len(command)-1] = analyticsKafkaClientProperties + command[len(command)-1]
			}
			_ = unstructured.SetNestedStringSlice(container, command, "command")
		case "clickhouse":
			// #nosec G101 -- Kubernetes Secret object name, not credential material.
			addAnalyticsVolume(pod, map[string]any{"name": "analytics-server-tls", "secret": map[string]any{"secretName": "mcp-analytics-clickhouse", "defaultMode": int64(0440)}})
			addAnalyticsMount(container, "analytics-server-tls", analyticsTLSMount+"/server")
			mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
			found := false
			for _, raw := range mounts {
				if raw.(map[string]any)["mountPath"] == "/etc/clickhouse-server/config.d/analytics-tls.xml" {
					found = true
				}
			}
			if !found {
				mounts = append(mounts, map[string]any{"name": "analytics-ca", "mountPath": "/etc/clickhouse-server/config.d/analytics-tls.xml", "subPath": "clickhouse-server.xml", "readOnly": true})
			}
			_ = unstructured.SetNestedSlice(container, mounts, "volumeMounts")
			ports, _, _ := unstructured.NestedSlice(container, "ports")
			for _, raw := range ports {
				remapAnalyticsPort(raw.(map[string]any), "containerPort")
			}
			_ = unstructured.SetNestedSlice(container, ports, "ports")
			setAnalyticsHTTPSProbes(container)
		case "clickhouse-init":
			rewriteAnalyticsCommand(container, "clickhouse-client --host=clickhouse", "clickhouse-client --config-file="+analyticsTLSMount+"/ca/clickhouse-client.xml --secure --port=9440 --host=clickhouse")
		}
	}
	return unstructured.SetNestedSlice(pod, containers, "containers")
}

func analyticsKeystoreVolume(secret string) map[string]any {
	return map[string]any{"name": "analytics-keystore", "projected": map[string]any{"defaultMode": int64(0440), "sources": []any{map[string]any{"secret": map[string]any{"name": secret, "items": []any{map[string]any{"key": "keystore.p12", "path": "keystore.p12"}, map[string]any{"key": "truststore.p12", "path": "truststore.p12"}}}}, map[string]any{"secret": map[string]any{"name": analyticsKeystoreSecret, "items": []any{map[string]any{"key": "password", "path": "password"}}}}}}}
}
func addAnalyticsVolume(pod, volume map[string]any) {
	volumes, _, _ := unstructured.NestedSlice(pod, "volumes")
	for _, existing := range volumes {
		if existing.(map[string]any)["name"] == volume["name"] {
			return
		}
	}
	_ = unstructured.SetNestedSlice(pod, append(volumes, volume), "volumes")
}
func addAnalyticsMount(container map[string]any, name, path string) {
	mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
	for _, existing := range mounts {
		if existing.(map[string]any)["name"] == name && existing.(map[string]any)["mountPath"] == path {
			return
		}
	}
	_ = unstructured.SetNestedSlice(container, append(mounts, map[string]any{"name": name, "mountPath": path, "readOnly": true}), "volumeMounts")
}

// secureAnalyticsClickHouseEnv moves inline CLICKHOUSE_ADDR overrides to the
// secure native port. Inline env takes precedence over envFrom, so the shared
// ConfigMap rewrite alone leaves such workloads dialing the removed :9000.
func secureAnalyticsClickHouseEnv(container map[string]any) {
	variables, _, _ := unstructured.NestedSlice(container, "env")
	changed := false
	for _, raw := range variables {
		variable := raw.(map[string]any)
		if value, ok := variable["value"].(string); ok && variable["name"] == "CLICKHOUSE_ADDR" && strings.HasSuffix(value, ":9000") {
			variable["value"] = strings.TrimSuffix(value, ":9000") + ":9440"
			changed = true
		}
	}
	if changed {
		_ = unstructured.SetNestedSlice(container, variables, "env")
	}
}

func setAnalyticsEnv(container map[string]any, values map[string]string) {
	variables, _, _ := unstructured.NestedSlice(container, "env")
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := values[name]
		found := false
		for i, existing := range variables {
			if existing.(map[string]any)["name"] == name {
				variables[i] = map[string]any{"name": name, "value": value}
				found = true
				break
			}
		}
		if !found {
			variables = append(variables, map[string]any{"name": name, "value": value})
		}
	}
	_ = unstructured.SetNestedSlice(container, variables, "env")
}
func rewriteAnalyticsCommand(container map[string]any, old, replacement string) {
	for _, field := range []string{"command", "args"} {
		values, _, _ := unstructured.NestedStringSlice(container, field)
		for i := range values {
			values[i] = strings.ReplaceAll(values[i], old, replacement)
		}
		if len(values) > 0 {
			_ = unstructured.SetNestedStringSlice(container, values, field)
		}
	}
}
func setAnalyticsHTTPSProbes(container map[string]any) {
	for _, probe := range []string{"startupProbe", "readinessProbe", "livenessProbe"} {
		if get, exists, _ := unstructured.NestedMap(container, probe, "httpGet"); exists {
			get["scheme"] = "HTTPS"
			remapAnalyticsPort(get, "port")
			_ = unstructured.SetNestedMap(container, get, probe, "httpGet")
		}
	}
}
func remapAnalyticsPort(object map[string]any, field string) {
	if value, ok := object[field].(int64); ok {
		if value == 9000 {
			object[field] = int64(9440)
		}
		if value == 8123 {
			object[field] = int64(8443)
		}
	}
}
func remapAnalyticsPolicyPorts(value any) {
	switch object := value.(type) {
	case map[string]any:
		remapAnalyticsPort(object, "port")
		for _, child := range object {
			remapAnalyticsPolicyPorts(child)
		}
	case []any:
		for _, child := range object {
			remapAnalyticsPolicyPorts(child)
		}
	}
}

const analyticsKafkaClientProperties = `umask 077
{
  printf 'security.protocol=SSL\nssl.keystore.type=PKCS12\nssl.truststore.type=PKCS12\nssl.endpoint.identification.algorithm=https\nssl.keystore.location=/etc/kafka/secrets/keystore.p12\nssl.truststore.location=/etc/kafka/secrets/truststore.p12\nssl.keystore.password='
  cat /etc/kafka/secrets/password
  printf '\nssl.key.password='
  cat /etc/kafka/secrets/password
  printf '\nssl.truststore.password='
  cat /etc/kafka/secrets/password
  printf '\n'
} > /tmp/kafka-client.properties
`
