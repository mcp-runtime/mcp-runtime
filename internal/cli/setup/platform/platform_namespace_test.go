package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
)

func TestPlatformNamespaceUsesRestrictedAdmission(t *testing.T) {
	for _, existingPolicy := range []string{"", "privileged", "restricted"} {
		t.Run(existingPolicy, func(t *testing.T) {
			labels := map[string]string{"existing": "keep"}
			if existingPolicy != "" {
				labels["pod-security.kubernetes.io/enforce"] = existingPolicy
			}
			clients := newPlatformKubernetesTestClients([]runtime.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: core.ComponentNamespace("platform-api"), Labels: labels}},
			}, nil)
			swapKubernetesClientsForTest(t, clients)
			if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
				t.Fatal(err)
			}
			ns, err := clients.Clientset.CoreV1().Namespaces().Get(context.Background(), core.ComponentNamespace("platform-api"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" ||
				ns.Labels["pod-security.kubernetes.io/audit"] != "restricted" ||
				ns.Labels["pod-security.kubernetes.io/warn"] != "restricted" ||
				ns.Labels["existing"] != "keep" {
				t.Fatalf("unexpected namespace labels: %v", ns.Labels)
			}
		})
	}
}

func TestSharedConfigAddressesObservabilityAcrossNamespaces(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "k8s", "01-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	for key, service := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "otel-collector",
		"PROMETHEUS_API_URL":          "prometheus",
	} {
		wantHost := service + "." + core.ComponentNamespace(service) + ".svc:"
		if !strings.Contains(config.Data[key], wantHost) {
			t.Errorf("%s = %q, want service host %q", key, config.Data[key], wantHost)
		}
	}
}

func TestStuckPostgresPreflightUsesPlatformNamespace(t *testing.T) {
	namespace := core.ComponentNamespace("postgres")
	clients := newPlatformKubernetesTestClients([]runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-postgres-0", Namespace: namespace, Labels: map[string]string{"app": "mcp-postgres"}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "postgres", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}}},
		},
	}, nil)
	issues := checkStuckAnalyticsStatefulSets(context.Background(), clients)
	if len(issues) != 1 || !strings.Contains(issues[0].message, "mcp-postgres") ||
		!strings.Contains(issues[0].cleanup[0], "-n "+namespace+" ") {
		t.Fatalf("Postgres preflight issues = %+v", issues)
	}
}

func TestTerminatingObservabilityAndCollectorNamespacesArePreflightFailures(t *testing.T) {
	now := metav1.Now()
	clients := newPlatformKubernetesTestClients([]runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: core.ComponentNamespace("analytics-api"), DeletionTimestamp: &now}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: core.ComponentNamespace("promtail"), DeletionTimestamp: &now}},
	}, nil)
	issues := checkTerminatingNamespaces(context.Background(), clients, true)
	if len(issues) != 1 || !issues[0].fatal ||
		!strings.Contains(issues[0].message, core.ComponentNamespace("analytics-api")) ||
		!strings.Contains(issues[0].message, core.ComponentNamespace("promtail")) {
		t.Fatalf("terminating namespace issues = %+v", issues)
	}
}

func TestPlatformNamespaceCreatedRestricted(t *testing.T) {
	clients := newPlatformKubernetesTestClients(nil, nil)
	swapKubernetesClientsForTest(t, clients)
	if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
		t.Fatal(err)
	}
	ns, err := clients.Clientset.CoreV1().Namespaces().Get(context.Background(), core.ComponentNamespace("platform-api"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("platform namespace is not restricted: %v", ns.Labels)
	}
}

func TestSetupCreatesIngressRoleNamespacesBeforeInstallingIngress(t *testing.T) {
	rec := &callRecorder{}
	deps := SetupDeps{
		ClusterManager: &fakeClusterManager{rec: rec},
		EnsureNamespace: func(namespace string) error {
			rec.add("ensure-" + namespace)
			return nil
		},
	}
	if err := setupClusterSteps(zap.NewNop(), "", "", cluster.IngressOptions{}, deps); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cluster-init",
		"ensure-" + core.ComponentNamespace("platform-api"),
		"ensure-" + core.ComponentNamespace("analytics-api"),
		"cluster-config",
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("setup calls = %v, want %v", rec.calls, want)
	}
	for i, call := range want {
		if rec.calls[i] != call {
			t.Fatalf("setup calls = %v, want %v", rec.calls, want)
		}
	}
}
