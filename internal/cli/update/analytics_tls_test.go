package update

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func analyticsTLSFixture() []runtime.Object {
	const ns = "mcp-observability"
	volume := func(name string, secret bool) corev1.Volume {
		v := corev1.Volume{Name: name}
		if secret {
			v.Secret = &corev1.SecretVolumeSource{SecretName: name}
		} else {
			v.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}
		}
		return v
	}
	kafka := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: ns}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kafka", Env: []corev1.EnvVar{
		{Name: "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", Value: "CONTROLLER:SSL,SSL:SSL"}, {Name: "KAFKA_SSL_CLIENT_AUTH", Value: "required"}, {Name: "KAFKA_INTER_BROKER_LISTENER_NAME", Value: "SSL"}, {Name: "KAFKA_SSL_ENDPOINT_IDENTIFICATION_ALGORITHM", Value: "https"},
	}}}, Volumes: []corev1.Volume{volume("mcp-analytics-kafka", true)}}}}}
	ch := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "clickhouse", Namespace: ns}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{volume("mcp-analytics-clickhouse", true), volume("mcp-analytics-ca", false)}}}}}
	objects := []runtime.Object{kafka, ch, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "clickhouse", Namespace: ns}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9440}, {Port: 8443}}}}}
	for _, namespace := range []string{ns, "mcp-platform"} {
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: namespace}, Data: map[string]string{"KAFKA_TLS_ENABLED": "true", "CLICKHOUSE_TLS_ENABLED": "true", "CLICKHOUSE_ADDR": "clickhouse.mcp-observability.svc.cluster.local:9440", "MCP_ANALYTICS_INGEST_URL": "https://mcp-ingest.mcp-observability.svc.cluster.local:8081/events"}})
	}
	for _, component := range []struct{ namespace, name, secret string }{{ns, "mcp-ingest", "mcp-analytics-ingest-kafka"}, {ns, "mcp-processor", "mcp-analytics-processor-kafka"}, {ns, "mcp-analytics-api", ""}, {"mcp-platform", "mcp-runtime-api", ""}} {
		pod := corev1.PodSpec{Volumes: []corev1.Volume{volume("mcp-analytics-ca", false)}}
		if component.secret != "" {
			pod.Volumes = append(pod.Volumes, volume(component.secret, true))
		}
		if component.name == "mcp-ingest" {
			pod.Containers = []corev1.Container{{Name: "ingest", Env: []corev1.EnvVar{{Name: "HTTP_TLS_CERT_FILE", Value: "/etc/mcp-analytics/server/tls.crt"}, {Name: "HTTP_TLS_KEY_FILE", Value: "/etc/mcp-analytics/server/tls.key"}}}}
		}
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: component.name, Namespace: component.namespace}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: pod}}})
	}
	return append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-operator-controller-manager", Namespace: "mcp-runtime"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{{Name: "MCP_ANALYTICS_CA_CONFIGMAP", Value: "mcp-analytics-ca"}}}}}}}})
}

func TestAnalyticsTLSUpdatePreflightNeverWritesOrTrustsMarkerAlone(t *testing.T) {
	for _, name := range []string{"secure", "not-installed", "legacy", "test-mode", "missing-client-key", "plaintext-port", "missing-operator-trust", "config-flag-only"} {
		t.Run(name, func(t *testing.T) {
			objects := analyticsTLSFixture()
			if name == "not-installed" {
				objects = nil
			}
			if name == "config-flag-only" {
				objects = []runtime.Object{objects[0], objects[3]}
			}
			for _, object := range objects {
				switch o := object.(type) {
				case *appsv1.StatefulSet:
					if o.Name == "kafka" && (name == "legacy" || name == "test-mode" || name == "config-flag-only") {
						o.Spec.Template.Spec.Containers[0].Env = nil
					}
				case *corev1.ConfigMap:
					if name == "test-mode" {
						o.Data["MCP_RUNTIME_TEST_MODE"] = "1"
					}
				case *corev1.Service:
					if name == "plaintext-port" {
						o.Spec.Ports = append(o.Spec.Ports, corev1.ServicePort{Port: 9000})
					}
				case *appsv1.Deployment:
					if name == "missing-client-key" && o.Name == "mcp-ingest" {
						o.Spec.Template.Spec.Volumes = o.Spec.Template.Spec.Volumes[:1]
					}
					if name == "missing-operator-trust" && o.Namespace == "mcp-runtime" {
						o.Spec.Template.Spec.Containers[0].Env = nil
					}
				}
			}
			cs := fake.NewSimpleClientset(objects...)
			err := requireAnalyticsTLSMigration(context.Background(), cs)
			allowed := name == "secure" || name == "not-installed" || name == "test-mode"
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v error=%v", allowed, err)
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("preflight mutated cluster: %v", action)
				}
			}
		})
	}
}
