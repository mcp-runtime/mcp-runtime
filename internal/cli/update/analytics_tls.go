package update

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/cli/core"
)

func analyticsMigrationRequired(detail string) error {
	return fmt.Errorf("analytics TLS setup migration required (%s); no update changes were made: preserve encrypted CA/certificate backups and data, install a fresh TLS analytics deployment and restore; see docs/platform-services.md#analytics-transport-security", detail)
}

// Read the actual installed wiring before making any update plan. A ConfigMap
// marker alone cannot certify that brokers, clients and serving ports use TLS.
func requireAnalyticsTLSMigration(ctx context.Context, cs kubernetes.Interface) error {
	ns := core.ComponentNamespace("kafka")
	kafka, err := cs.AppsV1().StatefulSets(ns).Get(ctx, "kafka", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Analytics is optional, but a partial installation is not an opt-out.
		if _, chErr := cs.AppsV1().StatefulSets(ns).Get(ctx, "clickhouse", metav1.GetOptions{}); apierrors.IsNotFound(chErr) {
			return nil
		}
		return analyticsMigrationRequired("analytics installation is incomplete")
	}
	if err != nil {
		return analyticsMigrationRequired("Kafka transport cannot be inspected")
	}
	config, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, "mcp-shared-config", metav1.GetOptions{})
	if err != nil {
		return analyticsMigrationRequired("analytics configuration cannot be inspected")
	}
	if config.Data["MCP_RUNTIME_TEST_MODE"] == "1" {
		return nil
	}
	if !transportEnvMatches(kafka.Spec.Template.Spec, "kafka", map[string]string{
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":        "CONTROLLER:SSL,SSL:SSL",
		"KAFKA_SSL_CLIENT_AUTH":                       "required",
		"KAFKA_INTER_BROKER_LISTENER_NAME":            "SSL",
		"KAFKA_SSL_ENDPOINT_IDENTIFICATION_ALGORITHM": "https",
	}) || !hasTransportVolume(kafka.Spec.Template.Spec, "mcp-analytics-kafka", true) {
		return analyticsMigrationRequired("broker/controller mutual TLS is missing")
	}
	for _, namespace := range []string{ns, core.ComponentNamespace("platform-api")} {
		shared, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, "mcp-shared-config", metav1.GetOptions{})
		if err != nil || shared.Data["CLICKHOUSE_TLS_ENABLED"] != "true" || shared.Data["KAFKA_TLS_ENABLED"] != "true" || !strings.HasSuffix(shared.Data["CLICKHOUSE_ADDR"], ":9440") || !strings.HasPrefix(shared.Data["MCP_ANALYTICS_INGEST_URL"], "https://") {
			return analyticsMigrationRequired("analytics clients do not use verified TLS endpoints")
		}
	}
	clickhouse, err := cs.AppsV1().StatefulSets(ns).Get(ctx, "clickhouse", metav1.GetOptions{})
	if err != nil || !hasTransportVolume(clickhouse.Spec.Template.Spec, "mcp-analytics-clickhouse", false) || !hasTransportVolume(clickhouse.Spec.Template.Spec, "mcp-analytics-ca", false) {
		return analyticsMigrationRequired("ClickHouse serving trust is missing")
	}
	service, err := cs.CoreV1().Services(ns).Get(ctx, "clickhouse", metav1.GetOptions{})
	if err != nil {
		return analyticsMigrationRequired("ClickHouse secure service cannot be inspected")
	}
	secureNative := false
	for _, port := range service.Spec.Ports {
		if port.Port == 9000 || port.Port == 8123 {
			return analyticsMigrationRequired("ClickHouse still exposes plaintext service ports")
		}
		secureNative = secureNative || port.Port == 9440
	}
	if !secureNative {
		return analyticsMigrationRequired("ClickHouse secure native port is missing")
	}
	for _, component := range []struct{ namespace, name, secret string }{
		{ns, "mcp-ingest", "mcp-analytics-ingest-kafka"},
		{ns, "mcp-processor", "mcp-analytics-processor-kafka"},
		{ns, "mcp-analytics-api", ""},
		{core.ComponentNamespace("platform-api"), "mcp-runtime-api", ""},
	} {
		deployment, err := cs.AppsV1().Deployments(component.namespace).Get(ctx, component.name, metav1.GetOptions{})
		if err != nil || !hasTransportVolume(deploymentPod(deployment), "mcp-analytics-ca", false) || component.secret != "" && !hasTransportVolume(deploymentPod(deployment), component.secret, false) {
			return analyticsMigrationRequired("an analytics client lacks mounted transport credentials")
		}
		if component.name == "mcp-ingest" && !transportEnvMatches(deployment.Spec.Template.Spec, "ingest", map[string]string{"HTTP_TLS_CERT_FILE": "/etc/mcp-analytics/server/tls.crt", "HTTP_TLS_KEY_FILE": "/etc/mcp-analytics/server/tls.key"}) {
			return analyticsMigrationRequired("ingest HTTPS serving configuration is missing")
		}
	}
	operator, err := cs.AppsV1().Deployments(core.NamespaceMCPRuntime).Get(ctx, "mcp-runtime-operator-controller-manager", metav1.GetOptions{})
	if err != nil || !transportEnvMatches(deploymentPod(operator), "manager", map[string]string{"MCP_ANALYTICS_CA_CONFIGMAP": "mcp-analytics-ca"}) {
		return analyticsMigrationRequired("gateway analytics trust is not configured")
	}
	return nil
}

func deploymentPod(deployment *appsv1.Deployment) corev1.PodSpec {
	if deployment == nil {
		return corev1.PodSpec{}
	}
	return deployment.Spec.Template.Spec
}

func transportEnvMatches(pod corev1.PodSpec, name string, expected map[string]string) bool {
	for _, container := range pod.Containers {
		if container.Name != name {
			continue
		}
		actual := map[string]string{}
		for _, variable := range container.Env {
			actual[variable.Name] = variable.Value
		}
		for key, value := range expected {
			if actual[key] != value {
				return false
			}
		}
		return true
	}
	return false
}

func hasTransportVolume(pod corev1.PodSpec, object string, projected bool) bool {
	for _, volume := range pod.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == object || volume.ConfigMap != nil && volume.ConfigMap.Name == object {
			return true
		}
		if projected && volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == object {
					return true
				}
			}
		}
	}
	return false
}
