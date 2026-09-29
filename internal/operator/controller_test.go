package operator

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
)

func TestRewriteRegistry(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		registry string
		want     string
	}{
		{
			name:     "test-image",
			image:    "test-image",
			registry: "registry.local",
			want:     "registry.local/test-image",
		},
	}
	for _, test := range tests {
		got := rewriteRegistry(test.image, test.registry)
		if got != test.want {
			t.Errorf("rewriteRegistry(%q, %q) = %q, want %q", test.image, test.registry, got, test.want)
		}
	}
}

func TestApplyContainerResources(t *testing.T) {
	t.Run("fills all defaults when no overrides", func(t *testing.T) {
		var container corev1.Container
		err := applyContainerResources(&container, mcpv1alpha1.ResourceRequirements{})
		if err != nil {
			t.Fatalf("applyContainerResources() error = %v", err)
		}

		if got := container.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse(defaultRequestCPU)) != 0 {
			t.Fatalf("requests.cpu = %q, want %q", got.String(), defaultRequestCPU)
		}
		if got := container.Resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse(defaultRequestMemory)) != 0 {
			t.Fatalf("requests.memory = %q, want %q", got.String(), defaultRequestMemory)
		}
		if got := container.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse(defaultLimitCPU)) != 0 {
			t.Fatalf("limits.cpu = %q, want %q", got.String(), defaultLimitCPU)
		}
		if got := container.Resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse(defaultLimitMemory)) != 0 {
			t.Fatalf("limits.memory = %q, want %q", got.String(), defaultLimitMemory)
		}
	})

	t.Run("overrides specific fields while keeping defaults for others", func(t *testing.T) {
		var container corev1.Container
		resources := mcpv1alpha1.ResourceRequirements{
			Requests: &mcpv1alpha1.ResourceList{
				CPU: "250m",
			},
			Limits: &mcpv1alpha1.ResourceList{
				Memory: "1Gi",
			},
		}

		err := applyContainerResources(&container, resources)
		if err != nil {
			t.Fatalf("applyContainerResources() error = %v", err)
		}

		if got := container.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("250m")) != 0 {
			t.Fatalf("requests.cpu = %q, want %q", got.String(), "250m")
		}
		if got := container.Resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse(defaultRequestMemory)) != 0 {
			t.Fatalf("requests.memory = %q, want %q", got.String(), defaultRequestMemory)
		}
		if got := container.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse(defaultLimitCPU)) != 0 {
			t.Fatalf("limits.cpu = %q, want %q", got.String(), defaultLimitCPU)
		}
		if got := container.Resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse("1Gi")) != 0 {
			t.Fatalf("limits.memory = %q, want %q", got.String(), "1Gi")
		}
	})

	t.Run("returns error for invalid CPU value", func(t *testing.T) {
		var container corev1.Container
		resources := mcpv1alpha1.ResourceRequirements{
			Requests: &mcpv1alpha1.ResourceList{
				CPU: "invalid",
			},
		}

		err := applyContainerResources(&container, resources)
		if err == nil {
			t.Fatal("expected error for invalid CPU value")
		}
	})

	t.Run("returns error for invalid memory value", func(t *testing.T) {
		var container corev1.Container
		resources := mcpv1alpha1.ResourceRequirements{
			Limits: &mcpv1alpha1.ResourceList{
				Memory: "invalid",
			},
		}

		err := applyContainerResources(&container, resources)
		if err == nil {
			t.Fatal("expected error for invalid memory value")
		}
	})
}

func TestBuildGatewayContainerAppliesDefaultResources(t *testing.T) {
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Gateway: &mcpv1alpha1.GatewayConfig{
				Enabled:     mcpv1alpha1.BoolPtr(true),
				Port:        defaultGatewayPort,
				UpstreamURL: "http://127.0.0.1:8088",
			},
		},
	}

	r := MCPServerReconciler{GatewayProxyImage: "example.com/mcp-gateway:latest"}
	container, err := r.buildGatewayContainer(mcpServer)
	if err != nil {
		t.Fatalf("buildGatewayContainer() error = %v", err)
	}

	if got := container.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse(defaultRequestCPU)) != 0 {
		t.Fatalf("gateway requests.cpu = %q, want %q", got.String(), defaultRequestCPU)
	}
	if got := container.Resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse(defaultRequestMemory)) != 0 {
		t.Fatalf("gateway requests.memory = %q, want %q", got.String(), defaultRequestMemory)
	}
	if got := container.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse(defaultLimitCPU)) != 0 {
		t.Fatalf("gateway limits.cpu = %q, want %q", got.String(), defaultLimitCPU)
	}
	if got := container.Resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse(defaultLimitMemory)) != 0 {
		t.Fatalf("gateway limits.memory = %q, want %q", got.String(), defaultLimitMemory)
	}
}

func TestBuildGatewayContainerAppliesConfiguredResources(t *testing.T) {
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Gateway: &mcpv1alpha1.GatewayConfig{
				Enabled:     mcpv1alpha1.BoolPtr(true),
				Port:        defaultGatewayPort,
				UpstreamURL: "http://127.0.0.1:8088",
				Resources: &mcpv1alpha1.ResourceRequirements{
					Requests: &mcpv1alpha1.ResourceList{CPU: "5m", Memory: "32Mi"},
					Limits:   &mcpv1alpha1.ResourceList{CPU: "100m", Memory: "128Mi"},
				},
			},
		},
	}

	r := MCPServerReconciler{GatewayProxyImage: "example.com/mcp-gateway:latest"}
	container, err := r.buildGatewayContainer(mcpServer)
	if err != nil {
		t.Fatalf("buildGatewayContainer() error = %v", err)
	}

	if got := container.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("5m")) != 0 {
		t.Fatalf("gateway requests.cpu = %q, want %q", got.String(), "5m")
	}
	if got := container.Resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse("32Mi")) != 0 {
		t.Fatalf("gateway requests.memory = %q, want %q", got.String(), "32Mi")
	}
	if got := container.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse("100m")) != 0 {
		t.Fatalf("gateway limits.cpu = %q, want %q", got.String(), "100m")
	}
	if got := container.Resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse("128Mi")) != 0 {
		t.Fatalf("gateway limits.memory = %q, want %q", got.String(), "128Mi")
	}
}

func TestDefaultedMCPServerForReconcile(t *testing.T) {
	t.Run("fills all defaults when unset", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-server",
				Namespace: "default",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertReplicas(t, mcpServer.Spec.Replicas, 1)
		assertEqual(t, "port", mcpServer.Spec.Port, int32(8088))
		assertEqual(t, "servicePort", mcpServer.Spec.ServicePort, int32(80))
		assertEqual(t, "imageTag", mcpServer.Spec.ImageTag, "latest")
		assertEqual(t, "ingressPath", mcpServer.Spec.IngressPath, "/test-server/mcp")
		assertEqual(t, "publicPathPrefix", mcpServer.Spec.PublicPathPrefix, "test-server")
		assertEqual(t, "ingressClass", mcpServer.Spec.IngressClass, "traefik")
	})

	t.Run("derives publicPathPrefix from server name", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
		}
		r := MCPServerReconciler{
			GatewayProxyImage:  "example.com/mcp-gateway:test",
			Scheme:             runtime.NewScheme(),
			DefaultIngressHost: "example.com",
		}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)
		assertEqual(t, "publicPathPrefix", mcpServer.Spec.PublicPathPrefix, "test-server")
		assertEqual(t, "ingressHost", mcpServer.Spec.IngressHost, "example.com")
	})

	t.Run("preserves explicit publicPathPrefix", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
			Spec: mcpv1alpha1.MCPServerSpec{
				PublicPathPrefix: "custom-prefix",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage:  "example.com/mcp-gateway:test",
			Scheme:             runtime.NewScheme(),
			DefaultIngressHost: "example.com",
		}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)
		assertEqual(t, "publicPathPrefix", mcpServer.Spec.PublicPathPrefix, "custom-prefix")
		assertEqual(t, "ingressHost", mcpServer.Spec.IngressHost, "")
	})

	t.Run("preserves existing values", func(t *testing.T) {
		replicas := int32(5)
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "my-server"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Replicas:     &replicas,
				Port:         9000,
				ServicePort:  8080,
				IngressPath:  "/custom/path",
				IngressClass: "nginx",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertReplicas(t, mcpServer.Spec.Replicas, 5)
		assertEqual(t, "port", mcpServer.Spec.Port, int32(9000))
		assertEqual(t, "servicePort", mcpServer.Spec.ServicePort, int32(8080))
		assertEqual(t, "ingressPath", mcpServer.Spec.IngressPath, "/custom/path")
		assertEqual(t, "ingressClass", mcpServer.Spec.IngressClass, "nginx")
		assertEqual(t, "imageTag", mcpServer.Spec.ImageTag, "latest")
	})

	t.Run("skips imageTag if image has tag", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "nginx:1.19", // Already has tag
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertEqual(t, "imageTag", mcpServer.Spec.ImageTag, "")
	})

	t.Run("sets imageTag for hostport image without tag", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "10.43.109.51:5000/example-python-2025-11-25",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertEqual(t, "imageTag", mcpServer.Spec.ImageTag, "latest")
	})

	t.Run("skips imageTag when hostport image already has tag", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "10.43.109.51:5000/example-python-2025-11-25:52c916f",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertEqual(t, "imageTag", mcpServer.Spec.ImageTag, "")
	})

	t.Run("skips ingressPath if name is empty", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{} // No name set
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		assertEqual(t, "ingressPath", mcpServer.Spec.IngressPath, "")
	})

	t.Run("applies gateway and analytics defaults", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-server"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "example.com/gateway-server",
				Gateway: &mcpv1alpha1.GatewayConfig{
					Enabled: mcpv1alpha1.BoolPtr(true),
				},
				Analytics: &mcpv1alpha1.AnalyticsConfig{
					IngestURL: "http://analytics.default.svc/api/events",
				},
			},
		}

		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Scheme: runtime.NewScheme()}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		if mcpServer.Spec.Gateway == nil {
			t.Fatal("expected gateway defaults to be applied")
		}
		assertEqual(t, "gatewayPort", mcpServer.Spec.Gateway.Port, int32(defaultGatewayPort))
		assertEqual(t, "gatewayUpstreamURL", mcpServer.Spec.Gateway.UpstreamURL, "http://127.0.0.1:8088")
		if mcpServer.Spec.Analytics == nil {
			t.Fatal("expected analytics defaults to be applied")
		}
		assertEqual(t, "analyticsSource", mcpServer.Spec.Analytics.Source, "gateway-server-gateway")
		assertEqual(t, "analyticsEventType", mcpServer.Spec.Analytics.EventType, "mcp.request")
	})

	t.Run("applies default analytics ingest url from reconciler config", func(t *testing.T) {
		mcpServer := mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-server"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "example.com/gateway-server",
				Gateway: &mcpv1alpha1.GatewayConfig{
					Enabled: mcpv1alpha1.BoolPtr(true),
				},
				Analytics: &mcpv1alpha1.AnalyticsConfig{},
			},
		}

		r := MCPServerReconciler{
			GatewayProxyImage:         "example.com/mcp-gateway:test",
			Scheme:                    runtime.NewScheme(),
			DefaultAnalyticsIngestURL: "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events",
		}
		mcpServer = *r.defaultedMCPServerForReconcile(&mcpServer)

		if mcpServer.Spec.Analytics == nil {
			t.Fatal("expected analytics defaults to be applied")
		}
		assertEqual(t, "analyticsIngestURL", mcpServer.Spec.Analytics.IngestURL, "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events")
	})
}

func TestReconcileDeploymentLabels(t *testing.T) {
	replicas := int32(1)
	mcpServer := mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example.com/test-server",
			ImageTag:    "latest",
			Port:        8088,
			ServicePort: 80,
			Replicas:    &replicas,
		},
	}

	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&mcpServer).Build()
	reconciler := MCPServerReconciler{
		Client:              client,
		Scheme:              scheme,
		GatewayProxyImage:   "example.com/mcp-gateway:latest",
		GatewayOTLPEndpoint: "http://otel-collector.mcp-sentinel.svc.cluster.local:4318",
	}

	if err := reconciler.reconcileDeployment(context.Background(), &mcpServer); err != nil {
		t.Fatalf("reconcileDeployment() error = %v", err)
	}

	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &deployment); err != nil {
		t.Fatalf("failed to fetch deployment: %v", err)
	}

	if deployment.Labels["app"] != mcpServer.Name {
		t.Fatalf("deployment label app = %q, want %q", deployment.Labels["app"], mcpServer.Name)
	}
	if deployment.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		t.Fatalf("deployment label managed-by = %q, want %q", deployment.Labels["app.kubernetes.io/managed-by"], "mcp-runtime")
	}

	if deployment.Spec.Template.Labels["app"] != mcpServer.Name {
		t.Fatalf("pod template label app = %q, want %q", deployment.Spec.Template.Labels["app"], mcpServer.Name)
	}
	if deployment.Spec.Template.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		t.Fatalf("pod template label managed-by = %q, want %q", deployment.Spec.Template.Labels["app.kubernetes.io/managed-by"], "mcp-runtime")
	}
	if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("expected MCPServer pods to disable service account token automount")
	}
	if deployment.Spec.Template.Spec.ServiceAccountName != defaultWorkloadServiceAccount {
		t.Fatalf("serviceAccountName = %q, want %q", deployment.Spec.Template.Spec.ServiceAccountName, defaultWorkloadServiceAccount)
	}
	if deployment.Spec.Template.Spec.SecurityContext == nil || deployment.Spec.Template.Spec.SecurityContext.SeccompProfile == nil {
		t.Fatal("expected MCPServer pod security context with seccomp profile")
	}
	if deployment.Spec.Template.Spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccomp profile = %q, want %q", deployment.Spec.Template.Spec.SecurityContext.SeccompProfile.Type, corev1.SeccompProfileTypeRuntimeDefault)
	}
	if deployment.Spec.Template.Spec.SecurityContext.RunAsUser == nil || *deployment.Spec.Template.Spec.SecurityContext.RunAsUser != restrictedRunAsUser {
		t.Fatalf("runAsUser = %v, want %d", deployment.Spec.Template.Spec.SecurityContext.RunAsUser, restrictedRunAsUser)
	}
	server := deployment.Spec.Template.Spec.Containers[0]
	if server.SecurityContext == nil || server.SecurityContext.AllowPrivilegeEscalation == nil || *server.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("expected MCPServer container to disallow privilege escalation")
	}
	if server.SecurityContext.Capabilities == nil || len(server.SecurityContext.Capabilities.Drop) != 1 || server.SecurityContext.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Fatalf("expected MCPServer container to drop all capabilities, got %#v", server.SecurityContext.Capabilities)
	}
}

func TestReconcileDeploymentAddsGatewaySidecar(t *testing.T) {
	replicas := int32(1)
	mcpServer := mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example.com/gateway-server",
			ImageTag:    "latest",
			Port:        8088,
			ServicePort: 80,
			IngressHost: "gateway.example.com",
			Replicas:    &replicas,
			Gateway: &mcpv1alpha1.GatewayConfig{
				Enabled: mcpv1alpha1.BoolPtr(true),
				Image:   "example.com/mcp-gateway:latest",
				Port:    8091,
			},
			Analytics: &mcpv1alpha1.AnalyticsConfig{
				IngestURL: "http://analytics.default.svc/api/events",
				Source:    "gateway-server",
				EventType: "mcp.request",
				APIKeySecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "analytics-creds",
					Key:  "api-key",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&mcpServer).Build()
	reconciler := MCPServerReconciler{
		GatewayProxyImage:   "example.com/mcp-gateway:test",
		Client:              client,
		Scheme:              scheme,
		GatewayOTLPEndpoint: "http://otel-collector.mcp-sentinel.svc.cluster.local:4318",
	}
	mcpServer = *reconciler.defaultedMCPServerForReconcile(&mcpServer)

	if err := reconciler.reconcileDeployment(context.Background(), &mcpServer); err != nil {
		t.Fatalf("reconcileDeployment() error = %v", err)
	}

	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &deployment); err != nil {
		t.Fatalf("failed to fetch deployment: %v", err)
	}

	if len(deployment.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(deployment.Spec.Template.Spec.Containers))
	}

	gateway := deployment.Spec.Template.Spec.Containers[1]
	assertEqual(t, "gatewayName", gateway.Name, "mcp-gateway")
	assertEqual(t, "gatewayImage", gateway.Image, "example.com/mcp-gateway:latest")
	if gateway.SecurityContext == nil || gateway.SecurityContext.ReadOnlyRootFilesystem == nil || !*gateway.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("expected gateway sidecar to use a read-only root filesystem")
	}
	if gateway.SecurityContext.AllowPrivilegeEscalation == nil || *gateway.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("expected gateway sidecar to disallow privilege escalation")
	}

	envByName := make(map[string]corev1.EnvVar, len(gateway.Env))
	for _, envVar := range gateway.Env {
		envByName[envVar.Name] = envVar
	}
	assertEqual(t, "gatewayPortEnv", envByName["PORT"].Value, "8091")
	assertEqual(t, "gatewayMetricsPortEnv", envByName["METRICS_PORT"].Value, "9103")
	assertEqual(t, "gatewayUpstreamEnv", envByName["UPSTREAM_URL"].Value, "http://127.0.0.1:8088")
	assertEqual(t, "gatewayOTELServiceName", envByName["OTEL_SERVICE_NAME"].Value, "gateway-server-gateway")
	assertEqual(t, "gatewayOTELEndpoint", envByName["OTEL_EXPORTER_OTLP_ENDPOINT"].Value, "http://otel-collector.mcp-sentinel.svc.cluster.local:4318")
	assertEqual(t, "gatewayExternalBaseURL", envByName["EXTERNAL_BASE_URL"].Value, "http://gateway.example.com")
	assertEqual(t, "analyticsIngestEnv", envByName["ANALYTICS_INGEST_URL"].Value, "http://analytics.default.svc/api/events")
	assertEqual(t, "analyticsSourceEnv", envByName["ANALYTICS_SOURCE"].Value, "gateway-server")
	assertEqual(t, "analyticsEventTypeEnv", envByName["ANALYTICS_EVENT_TYPE"].Value, "mcp.request")
	if envByName["ANALYTICS_API_KEY"].ValueFrom == nil || envByName["ANALYTICS_API_KEY"].ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected analytics api key env var to come from a secret")
	}
	assertEqual(t, "analyticsAPIKeySecretName", envByName["ANALYTICS_API_KEY"].ValueFrom.SecretKeyRef.Name, "analytics-creds")
	assertEqual(t, "analyticsAPIKeySecretKey", envByName["ANALYTICS_API_KEY"].ValueFrom.SecretKeyRef.Key, "api-key")
}

func TestReconcileServiceUsesGatewayPortWhenEnabled(t *testing.T) {
	replicas := int32(1)
	mcpServer := mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-service",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example.com/gateway-service",
			ImageTag:    "latest",
			Port:        8088,
			ServicePort: 80,
			Replicas:    &replicas,
			Gateway: &mcpv1alpha1.GatewayConfig{
				Enabled: mcpv1alpha1.BoolPtr(true),
				Image:   "example.com/mcp-gateway:latest",
				Port:    8091,
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&mcpServer).Build()
	reconciler := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test",
		Client:            client,
		Scheme:            scheme,
	}

	if err := reconciler.reconcileService(context.Background(), &mcpServer); err != nil {
		t.Fatalf("reconcileService() error = %v", err)
	}

	var service corev1.Service
	if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &service); err != nil {
		t.Fatalf("failed to fetch service: %v", err)
	}

	if len(service.Spec.Ports) != 2 {
		t.Fatalf("expected 2 service ports, got %d", len(service.Spec.Ports))
	}
	assertEqual(t, "serviceTargetPort", service.Spec.Ports[0].TargetPort.IntVal, int32(8091))
	assertEqual(t, "serviceMetricsPort", service.Spec.Ports[1].Port, int32(DefaultGatewayMetricsPort))
	assertEqual(t, "serviceManagedByLabel", service.Labels["app.kubernetes.io/managed-by"], "mcp-runtime")
	assertEqual(t, "servicePrometheusScrape", service.Annotations["prometheus.io/scrape"], "true")
	assertEqual(t, "servicePrometheusPath", service.Annotations["prometheus.io/path"], "/metrics")
	assertEqual(t, "servicePrometheusPort", service.Annotations["prometheus.io/port"], "9103")
}

func TestReconcileServicePreservesExistingAnnotations(t *testing.T) {
	replicas := int32(1)
	mcpServer := mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "annotated-gateway",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example.com/gateway-service",
			ImageTag:    "latest",
			Port:        8088,
			ServicePort: 80,
			Replicas:    &replicas,
			Gateway: &mcpv1alpha1.GatewayConfig{
				Enabled: mcpv1alpha1.BoolPtr(true),
				Image:   "example.com/mcp-gateway:latest",
				Port:    8091,
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}

	existingService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpServer.Name,
			Namespace: mcpServer.Namespace,
			Annotations: map[string]string{
				"example.com/custom": "keep-me",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&mcpServer, existingService).Build()
	reconciler := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test",
		Client:            client,
		Scheme:            scheme,
	}

	if err := reconciler.reconcileService(context.Background(), &mcpServer); err != nil {
		t.Fatalf("reconcileService() error = %v", err)
	}

	var service corev1.Service
	if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &service); err != nil {
		t.Fatalf("failed to fetch service: %v", err)
	}

	assertEqual(t, "customAnnotation", service.Annotations["example.com/custom"], "keep-me")
	assertEqual(t, "servicePrometheusScrape", service.Annotations["prometheus.io/scrape"], "true")
}

func TestResolveGatewayImage(t *testing.T) {
	t.Run("uses per-server image when set", func(t *testing.T) {
		reconciler := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Gateway: &mcpv1alpha1.GatewayConfig{
					Enabled: mcpv1alpha1.BoolPtr(true),
					Image:   "example.com/proxy:latest",
				},
			},
		}

		got, err := reconciler.resolveGatewayImage(mcpServer)
		if err != nil {
			t.Fatalf("resolveGatewayImage() unexpected error: %v", err)
		}
		assertEqual(t, "gatewayImage", got, "example.com/proxy:latest")
	})

	t.Run("falls back to reconciler default image", func(t *testing.T) {
		reconciler := MCPServerReconciler{GatewayProxyImage: "example.com/default-proxy:latest"}
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Gateway: &mcpv1alpha1.GatewayConfig{
					Enabled: mcpv1alpha1.BoolPtr(true),
				},
			},
		}

		got, err := reconciler.resolveGatewayImage(mcpServer)
		if err != nil {
			t.Fatalf("resolveGatewayImage() unexpected error: %v", err)
		}
		assertEqual(t, "gatewayImage", got, "example.com/default-proxy:latest")
	})

	t.Run("returns error when no image is configured", func(t *testing.T) {
		reconciler := MCPServerReconciler{}
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Gateway: &mcpv1alpha1.GatewayConfig{
					Enabled: mcpv1alpha1.BoolPtr(true),
				},
			},
		}

		if _, err := reconciler.resolveGatewayImage(mcpServer); err == nil {
			t.Fatal("expected resolveGatewayImage() to return an error")
		}
	})
}

func assertReplicas(t *testing.T, replicas *int32, want int32) {
	t.Helper()
	if replicas == nil || *replicas != want {
		t.Errorf("replicas = %v, want %d", replicas, want)
	}
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestValidateIngressConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}

	t.Run("succeeds with valid config", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				IngressPath: "/test-server",
			},
		}
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
			Build()
		if err := client.Create(context.Background(), mcpServer); err != nil {
			t.Fatalf("failed to create MCPServer: %v", err)
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		err := r.validateIngressConfig(context.Background(), mcpServer, logr.Discard())
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("fails when ingressHost missing", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressPath: "/test-server",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		err := r.validateIngressConfig(context.Background(), mcpServer, logr.Discard())
		if err == nil {
			t.Fatal("expected error for missing ingressHost")
		}
	})

	t.Run("succeeds when publicPathPrefix uses hostless routing", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				PublicPathPrefix: "test-server",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		err := r.validateIngressConfig(context.Background(), mcpServer, logr.Discard())
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("fails when ingressPath missing", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				// IngressPath intentionally missing
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		err := r.validateIngressConfig(context.Background(), mcpServer, logr.Discard())
		if err == nil {
			t.Fatal("expected error for missing ingressPath")
		}
	})
}

func TestFetchMCPServer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}

	t.Run("succeeds with valid name", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		got, _, err := r.fetchMCPServer(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-server", Namespace: "default"}})
		if err != nil {
			t.Fatalf("failed to fetch mcp server: %v", err)
		}
		assertEqual(t, "name", got.Name, "test-server")
		assertEqual(t, "namespace", got.Namespace, "default")
	})
}

func TestReconcileResources(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	t.Run("succeeds with valid resources", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				IngressPath: "/test",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		err := r.reconcileResources(context.Background(), mcpServer, logr.Discard())
		if err != nil {
			t.Fatalf("failed to reconcile resources: %v", err)
		}
	})
}

func TestCheckResourceReadiness(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	t.Run("returns false when resources do not exist", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		readiness, err := r.checkResourceReadiness(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check resource readiness: %v", err)
		}
		// Resources don't exist yet, so they're not ready
		assertEqual(t, "deploymentReady", readiness.Deployment, false)
		assertEqual(t, "serviceReady", readiness.Service, false)
		assertEqual(t, "ingressReady", readiness.Ingress, false)
		assertEqual(t, "policyReady", readiness.Policy, false)
		assertEqual(t, "canaryReady", readiness.Canary, false)
	})
}

func TestDefaultedMCPServerForReconcileDoesNotPersistDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		Spec:       mcpv1alpha1.MCPServerSpec{Image: "test-image"},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
	r := MCPServerReconciler{
		GatewayProxyImage:         "example.com/mcp-gateway:test",
		Client:                    client,
		Scheme:                    scheme,
		DefaultIngressHost:        "example.com",
		DefaultAnalyticsIngestURL: "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events",
	}

	defaulted := r.defaultedMCPServerForReconcile(mcpServer)
	assertEqual(t, "defaultedPort", defaulted.Spec.Port, int32(8088))
	assertReplicas(t, defaulted.Spec.Replicas, 1)
	assertEqual(t, "defaultedIngressHost", defaulted.Spec.IngressHost, "example.com")

	var stored mcpv1alpha1.MCPServer
	if err := client.Get(context.Background(), types.NamespacedName{Name: "test-server", Namespace: "default"}, &stored); err != nil {
		t.Fatalf("failed to fetch stored MCPServer: %v", err)
	}
	assertEqual(t, "storedPort", stored.Spec.Port, int32(0))
	if stored.Spec.Replicas != nil {
		t.Fatalf("expected stored replicas to stay nil, got %v", *stored.Spec.Replicas)
	}
	assertEqual(t, "storedIngressHost", stored.Spec.IngressHost, "")
}

func TestValidateMCPServerSpecRunsAfterDefaulting(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-server", Namespace: "default"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "example.com/server",
			Tools: []mcpv1alpha1.ToolConfig{{
				Name: "read_file",
			}},
		},
	}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(server).
		WithObjects(server.DeepCopy()).
		Build()
	reconciler := &MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
	defaulted := reconciler.defaultedMCPServerForReconcile(server)

	err := reconciler.validateMCPServerSpec(context.Background(), defaulted, logr.Discard())
	if err == nil {
		t.Fatal("expected validation error for missing tool sideEffect")
	}
}

func TestRequireSpecField(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}

	t.Run("succeeds with valid field", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				IngressHost: "example.com",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		err := r.requireSpecField(context.Background(), mcpServer, logr.Discard(), "ingressHost", "example.com", "ingressHost is required")
		if err != nil {
			t.Fatalf("failed to require spec field: %v", err)
		}
	})
}

func TestUpdateStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}

	t.Run("succeeds with valid status", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
			Build()
		if err := client.Create(context.Background(), mcpServer); err != nil {
			t.Fatalf("failed to create MCPServer: %v", err)
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		r.updateStatus(context.Background(), mcpServer, "Ready", "All resources reconciled", resourceReadiness{
			Deployment: true,
			Service:    true,
			Ingress:    true,
			Gateway:    true,
			Policy:     true,
			Canary:     true,
		})
		updated := &mcpv1alpha1.MCPServer{}
		if err := client.Get(context.Background(), types.NamespacedName{
			Name:      "test-server",
			Namespace: "default",
		}, updated); err != nil {
			t.Fatalf("failed to fetch updated MCPServer: %v", err)
		}
		assertEqual(t, "phase", updated.Status.Phase, "Ready")
		assertEqual(t, "message", updated.Status.Message, "All resources reconciled")
	})

	t.Run("publishes the derived public URL", func(t *testing.T) {
		stored := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "buddy", Namespace: "default"},
			Spec:       mcpv1alpha1.MCPServerSpec{PublicPathPrefix: "buddy"},
		}
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
			Build()
		if err := client.Create(context.Background(), stored); err != nil {
			t.Fatalf("failed to create MCPServer: %v", err)
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme, DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}
		r.updateStatus(context.Background(), r.defaultedMCPServerForReconcile(stored), "Ready", "ok", resourceReadiness{})
		updated := &mcpv1alpha1.MCPServer{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "buddy", Namespace: "default"}, updated); err != nil {
			t.Fatalf("failed to fetch updated MCPServer: %v", err)
		}
		assertEqual(t, "status.url", updated.Status.URL, "https://mcp.example.com/buddy/mcp")
	})
}

func TestDeterminePhase(t *testing.T) {
	t.Run("succeeds with valid phase", func(t *testing.T) {
		gatewayDisabledMCP := &mcpv1alpha1.MCPServer{}
		phase, allReady := determinePhase(resourceReadiness{
			Deployment: true,
			Service:    true,
			Ingress:    true,
			Gateway:    true,
			Policy:     true,
			Canary:     true,
		}, gatewayDisabledMCP)
		assertEqual(t, "phase", phase, "Ready")
		assertEqual(t, "allReady", allReady, true)
	})

	t.Run("returns pending when optional resources are disabled and core resources are not ready", func(t *testing.T) {
		gatewayDisabledMCP := &mcpv1alpha1.MCPServer{}
		phase, allReady := determinePhase(resourceReadiness{
			Deployment: false,
			Service:    false,
			Ingress:    false,
			Gateway:    false,
			Policy:     false,
			Canary:     false,
		}, gatewayDisabledMCP)
		assertEqual(t, "phase", phase, "Pending")
		assertEqual(t, "allReady", allReady, false)
	})
}

func TestCheckDeploymentReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	t.Run("returns false when deployment does not exist", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		ready, err := r.checkDeploymentReady(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check deployment readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})
}

func TestCheckServiceReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	t.Run("returns false when service does not exist", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		ready, err := r.checkServiceReady(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check service readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})
}

func TestCheckIngressReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	t.Run("returns false when ingress does not exist", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		ready, err := r.checkIngressReady(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})

	t.Run("mtls server is ready when its IngressRoute and backend secrets exist", func(t *testing.T) {
		mtlsScheme := traefikScheme(t)
		server := mtlsServer()
		route := crFixture(ingressRouteGVK, server.Name, server.Namespace)
		objs := append([]client.Object{server, route}, mtlsBackendSecretObjects(server)...)
		client := fake.NewClientBuilder().WithScheme(mtlsScheme).WithObjects(objs...).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: mtlsScheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		ready, err := r.checkIngressReady(context.Background(), server)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, true)
	})

	t.Run("mtls server is not ready when IngressRoute exists without backend secrets", func(t *testing.T) {
		mtlsScheme := traefikScheme(t)
		server := mtlsServer()
		route := crFixture(ingressRouteGVK, server.Name, server.Namespace)
		client := fake.NewClientBuilder().WithScheme(mtlsScheme).WithObjects(server, route).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: mtlsScheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		ready, err := r.checkIngressReady(context.Background(), server)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})

	t.Run("mtls server is not ready without an IngressRoute", func(t *testing.T) {
		mtlsScheme := traefikScheme(t)
		server := mtlsServer()
		// Only the legacy passthrough IngressRouteTCP exists; the terminate-and-
		// re-encrypt model no longer uses it, so the server must not read ready.
		legacy := crFixture(ingressRouteTCPGVK, server.Name, server.Namespace)
		objs := append([]client.Object{server, legacy}, mtlsBackendSecretObjects(server)...)
		client := fake.NewClientBuilder().WithScheme(mtlsScheme).WithObjects(objs...).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: mtlsScheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		ready, err := r.checkIngressReady(context.Background(), server)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})

	t.Run("returns true when ingress has load balancer status", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Status: networkingv1.IngressStatus{
				LoadBalancer: networkingv1.IngressLoadBalancerStatus{
					Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "10.0.0.1"}},
				},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer, ingress).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		ready, err := r.checkIngressReady(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, true)
	})

	t.Run("returns false when only ingress class exists without admitted status", func(t *testing.T) {
		ingressClassName := "traefik"
		pathType := networkingv1.PathTypePrefix
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: networkingv1.IngressSpec{
				IngressClassName: &ingressClassName,
				Rules: []networkingv1.IngressRule{
					{
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{Path: "/", PathType: &pathType},
								},
							},
						},
					},
				},
			},
		}
		class := &networkingv1.IngressClass{
			ObjectMeta: metav1.ObjectMeta{Name: ingressClassName},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer, ingress, class).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		ready, err := r.checkIngressReady(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to check ingress readiness: %v", err)
		}
		assertEqual(t, "ready", ready, false)
	})

	t.Run("uses configured readiness mode when ingress has rules without load balancer status", func(t *testing.T) {
		pathType := networkingv1.PathTypePrefix
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{Path: "/", PathType: &pathType},
								},
							},
						},
					},
				},
			},
		}

		for _, tt := range []struct {
			name string
			mode string
			want bool
		}{
			{name: "strict", mode: IngressReadinessModeStrict, want: false},
			{name: "permissive", mode: IngressReadinessModePermissive, want: true},
			{name: "invalid falls back to strict", mode: "dev", want: false},
		} {
			t.Run(tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer.DeepCopy(), ingress.DeepCopy()).Build()
				r := MCPServerReconciler{
					GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme, IngressReadinessMode: tt.mode}
				ready, err := r.checkIngressReady(context.Background(), mcpServer)
				if err != nil {
					t.Fatalf("failed to check ingress readiness: %v", err)
				}
				assertEqual(t, "ready", ready, tt.want)
			})
		}
	})
}

func TestRenderGatewayPolicyIncludesCrossNamespaceReferences(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			TeamID: "team-payments",
			Tools: []mcpv1alpha1.ToolConfig{
				{Name: "refund_invoice", SideEffect: mcpv1alpha1.ToolSideEffectWrite},
			},
		},
	}
	grant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-a", Namespace: "team-a"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-1", TeamID: "team-payments"},
			AllowedSideEffects: []mcpv1alpha1.ToolSideEffect{
				mcpv1alpha1.ToolSideEffectWrite,
			},
			ToolRules: []mcpv1alpha1.ToolRule{
				{Name: "refund_invoice", Decision: mcpv1alpha1.PolicyDecisionAllow},
			},
			ExpiresAt: &metav1.Time{Time: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
	defaultedGrant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-default", Namespace: "team-a"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-3"},
		},
	}
	foreignTeamGrant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-foreign", Namespace: "team-a"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-4", TeamID: "team-foreign"},
		},
	}
	session := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session-a", Namespace: "team-b"},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{
			ServerRef:      mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:        mcpv1alpha1.SubjectRef{AgentID: "agent-1", TeamID: "team-payments"},
			ConsentedTrust: mcpv1alpha1.TrustLevelMedium,
		},
	}
	defaultedSession := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session-default", Namespace: "team-b"},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{
			ServerRef:      mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:        mcpv1alpha1.SubjectRef{AgentID: "agent-2"},
			ConsentedTrust: mcpv1alpha1.TrustLevelMedium,
		},
	}
	foreignTeamSession := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session-foreign", Namespace: "team-b"},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{
			ServerRef:      mcpv1alpha1.ServerReference{Name: "payments", Namespace: "servers"},
			Subject:        mcpv1alpha1.SubjectRef{AgentID: "agent-3", TeamID: "team-foreign"},
			ConsentedTrust: mcpv1alpha1.TrustLevelMedium,
		},
	}
	unrelatedGrant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-b", Namespace: "team-a"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "inventory", Namespace: "servers"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-2"},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer, grant, defaultedGrant, foreignTeamGrant, session, defaultedSession, foreignTeamSession, unrelatedGrant).
		Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

	doc, err := r.renderGatewayPolicy(context.Background(), mcpServer)
	if err != nil {
		t.Fatalf("renderGatewayPolicy() error = %v", err)
	}
	if len(doc.Tools) != 1 || doc.Tools[0].SideEffect != "write" {
		t.Fatalf("expected tool side effect to be rendered, got %+v", doc.Tools)
	}
	if doc.Server.TeamID != "team-payments" {
		t.Fatalf("expected server teamID to be rendered, got %q", doc.Server.TeamID)
	}
	if len(doc.Grants) != 3 {
		t.Fatalf("expected 3 matching grants, got %d: %+v", len(doc.Grants), doc.Grants)
	}
	grantsByName := make(map[string]policy.Grant, len(doc.Grants))
	for _, grant := range doc.Grants {
		grantsByName[grant.Name] = grant
	}
	if foreign := grantsByName["grant-foreign"]; foreign.TeamID != "team-foreign" {
		t.Fatalf("expected foreign-team grant to render explicit subject teamID, got %+v", foreign)
	}
	renderedGrant, ok := grantsByName["grant-a"]
	if !ok {
		t.Fatalf("expected cross-namespace grant to be rendered, got %+v", doc.Grants)
	}
	if got := renderedGrant.ExpiresAt; got != "2027-01-01T00:00:00Z" {
		t.Fatalf("rendered grant expiry = %q", got)
	}
	if renderedGrant.TeamID != "team-payments" {
		t.Fatalf("expected grant teamID to be rendered, got %+v", renderedGrant)
	}
	if defaulted := grantsByName["grant-default"]; defaulted.TeamID != "team-payments" {
		t.Fatalf("expected missing grant teamID to default to server teamID, got %+v", defaulted)
	}
	if len(renderedGrant.AllowedSideEffects) != 1 || renderedGrant.AllowedSideEffects[0] != "write" {
		t.Fatalf("expected grant side effects to be rendered, got %+v", renderedGrant.AllowedSideEffects)
	}
	if len(doc.Sessions) != 3 {
		t.Fatalf("expected 3 matching sessions, got %d: %+v", len(doc.Sessions), doc.Sessions)
	}
	sessionsByName := make(map[string]policy.Binding, len(doc.Sessions))
	for _, session := range doc.Sessions {
		sessionsByName[string(session.Name)] = session
	}
	if foreign := sessionsByName["session-foreign"]; foreign.TeamID != "team-foreign" {
		t.Fatalf("expected foreign-team session to render explicit subject teamID, got %+v", foreign)
	}
	if renderedSession := sessionsByName["session-a"]; renderedSession.TeamID != "team-payments" {
		t.Fatalf("expected session teamID to be rendered, got %+v", renderedSession)
	}
	if defaulted := sessionsByName["session-default"]; defaulted.TeamID != "team-payments" {
		t.Fatalf("expected missing session teamID to default to server teamID, got %+v", defaulted)
	}
}

func TestBuildIngressAnnotations(t *testing.T) {
	t.Run("returns user-specified annotations", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				IngressAnnotations: map[string]string{
					"custom": "annotation",
				},
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		annotations := r.buildIngressAnnotations(mcpServer)
		assertEqual(t, "custom annotation", annotations["custom"], "annotation")
	})

	t.Run("includes default traefik annotation", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		annotations := r.buildIngressAnnotations(mcpServer)
		// Should include default traefik entrypoints annotation
		assertEqual(t, "traefik annotation", annotations["traefik.ingress.kubernetes.io/router.entrypoints"], "web")
	})

	t.Run("does not default nginx rewrite target", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				IngressClass: "nginx",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		annotations := r.buildIngressAnnotations(mcpServer)
		if _, exists := annotations["nginx.ingress.kubernetes.io/rewrite-target"]; exists {
			t.Fatal("nginx rewrite-target should only be set when provided by the user")
		}
		assertEqual(t, "nginx ssl redirect annotation", annotations["nginx.ingress.kubernetes.io/ssl-redirect"], "false")
	})

	t.Run("preserves user-specified nginx rewrite target", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				IngressClass: "nginx",
				IngressAnnotations: map[string]string{
					"nginx.ingress.kubernetes.io/rewrite-target": "/$2",
				},
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		annotations := r.buildIngressAnnotations(mcpServer)
		assertEqual(t, "nginx rewrite target", annotations["nginx.ingress.kubernetes.io/rewrite-target"], "/$2")
	})
}

func TestReconcileDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	t.Run("succeeds with valid deployment", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		err := r.reconcileDeployment(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to reconcile deployment: %v", err)
		}
	})
}

func TestReconcileService(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	t.Run("succeeds with valid service", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		err := r.reconcileService(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to reconcile service: %v", err)
		}
	})
}

func TestReconcileIngress(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add networking scheme: %v", err)
	}

	t.Run("succeeds with valid ingress", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				IngressPath: "/test",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		err := r.reconcileIngress(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to reconcile ingress: %v", err)
		}

		var ingress networkingv1.Ingress
		if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &ingress); err != nil {
			t.Fatalf("failed to fetch ingress: %v", err)
		}
		if got := ingress.Spec.Rules[0].HTTP.Paths; len(got) != 1 {
			t.Fatalf("expected 1 ingress path, got %d", len(got))
		} else {
			assertEqual(t, "ingressPath", got[0].Path, "/test")
		}
		assertEqual(t, "ingressHost", ingress.Spec.Rules[0].Host, "example.com")
	})

	t.Run("uses publicPathPrefix for path-based routing", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "oauth-example-go-2025-11-25", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				IngressPath:      "/ignored-when-prefix-set",
				PublicPathPrefix: "go-mcp",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		if err := r.reconcileIngress(context.Background(), mcpServer); err != nil {
			t.Fatalf("failed to reconcile ingress: %v", err)
		}

		var ingress networkingv1.Ingress
		if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &ingress); err != nil {
			t.Fatalf("failed to fetch ingress: %v", err)
		}
		if got := ingress.Spec.Rules[0].HTTP.Paths; len(got) != 1 {
			t.Fatalf("expected 1 ingress path, got %d", len(got))
		} else {
			assertEqual(t, "ingressPath", got[0].Path, "/go-mcp/mcp")
		}
		assertEqual(t, "ingressHost", ingress.Spec.Rules[0].Host, "")
	})

	t.Run("uses ingress host and websecure defaults with publicPathPrefix", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "oauth-example-go-2025-11-25", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				IngressHost:      "mcp.example.com",
				PublicPathPrefix: "go-mcp",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage:         "example.com/mcp-gateway:test",
			Client:                    client,
			Scheme:                    scheme,
			DefaultIngressEntryPoints: "websecure",
			DefaultIngressTLS:         true,
		}
		if err := r.reconcileIngress(context.Background(), mcpServer); err != nil {
			t.Fatalf("failed to reconcile ingress: %v", err)
		}

		var ingress networkingv1.Ingress
		if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &ingress); err != nil {
			t.Fatalf("failed to fetch ingress: %v", err)
		}
		assertEqual(t, "ingressHost", ingress.Spec.Rules[0].Host, "mcp.example.com")
		assertEqual(t, "ingressPath", ingress.Spec.Rules[0].HTTP.Paths[0].Path, "/go-mcp/mcp")
		assertEqual(t, "entrypoints", ingress.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"], "websecure")
		assertEqual(t, "tls", ingress.Annotations["traefik.ingress.kubernetes.io/router.tls"], "true")
	})

	// The route follows the server's own path, never a tenant-chosen
	// audience path that could claim another server's metadata route.
	t.Run("adds oauth protected resource path for the server's own route", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "oauth-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				IngressPath: "/oauth-server/mcp",
				Auth: &mcpv1alpha1.AuthConfig{
					Audience: "https://example.com/custom/resource",
				},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}
		if err := r.reconcileIngress(context.Background(), mcpServer); err != nil {
			t.Fatalf("failed to reconcile ingress: %v", err)
		}

		var ingress networkingv1.Ingress
		if err := client.Get(context.Background(), types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, &ingress); err != nil {
			t.Fatalf("failed to fetch ingress: %v", err)
		}
		if got := ingress.Spec.Rules[0].HTTP.Paths; len(got) != 2 {
			t.Fatalf("expected 2 ingress paths, got %d", len(got))
		} else {
			assertEqual(t, "primaryPath", got[0].Path, "/oauth-server/mcp")
			assertEqual(t, "protectedResourcePath", got[1].Path, "/.well-known/oauth-protected-resource/oauth-server/mcp")
		}
	})
}

func TestBuildEnvVars(t *testing.T) {
	t.Run("converts EnvVars to corev1.EnvVar slice", func(t *testing.T) {
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		input := []mcpv1alpha1.EnvVar{
			{Name: "FOO", Value: "bar"},
			{Name: "BAZ", Value: "qux"},
		}
		envVars := r.buildEnvVars(input, nil)
		assertEqual(t, "len", len(envVars), 2)
		assertEqual(t, "envVars[0].Name", envVars[0].Name, "FOO")
		assertEqual(t, "envVars[0].Value", envVars[0].Value, "bar")
		assertEqual(t, "envVars[1].Name", envVars[1].Name, "BAZ")
		assertEqual(t, "envVars[1].Value", envVars[1].Value, "qux")
	})

	t.Run("returns empty slice for nil input", func(t *testing.T) {
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		envVars := r.buildEnvVars(nil, nil)
		assertEqual(t, "len", len(envVars), 0)
	})
}

func TestBuildImagePullSecrets(t *testing.T) {
	t.Run("returns user-specified pull secrets", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				ImagePullSecrets: []string{"secret1", "secret2"},
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		pullSecrets := r.buildImagePullSecrets(mcpServer)
		assertEqual(t, "len", len(pullSecrets), 2)
		assertEqual(t, "pullSecrets[0]", pullSecrets[0].Name, "secret1")
		assertEqual(t, "pullSecrets[1]", pullSecrets[1].Name, "secret2")
	})

	t.Run("returns empty slice when no pull secrets specified", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		pullSecrets := r.buildImagePullSecrets(mcpServer)
		assertEqual(t, "len", len(pullSecrets), 0)
	})
}

func TestResolveImage(t *testing.T) {
	t.Run("returns user-specified image", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "test-image")
	})
	t.Run("returns user-specified image with tag", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:    "test-image",
				ImageTag: "v1.0.0",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "test-image:v1.0.0")
	})
	t.Run("appends imageTag when image uses hostport registry", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:    "10.43.109.51:5000/example-python-2025-11-25",
				ImageTag: "52c916f",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "10.43.109.51:5000/example-python-2025-11-25:52c916f")
	})
	t.Run("preserves explicit tag when image uses hostport registry", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:    "10.43.109.51:5000/example-python-2025-11-25:52c916f",
				ImageTag: "ignored",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "10.43.109.51:5000/example-python-2025-11-25:52c916f")
	})
	t.Run("returns user-specified image with registry override", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				RegistryOverride: "test-registry",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "test-registry/test-image")
	})
	t.Run("returns user-specified image with registry override and tag", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				ImageTag:         "v1.0.0",
				RegistryOverride: "test-registry",
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "test-registry/test-image:v1.0.0")
	})
	t.Run("useProvisionedRegistry falls back to registry pull host", func(t *testing.T) {
		original := DefaultOperatorConfig
		DefaultOperatorConfig = &OperatorConfig{
			InternalRegistryEndpoint: "10.43.75.207:5000",
			RegistryPullHost:         "10.43.75.207:5000",
		}
		t.Cleanup(func() {
			DefaultOperatorConfig = original
		})

		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:                  "test-image",
				UseProvisionedRegistry: true,
			},
		}
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test"}
		image, err := r.resolveImage(context.Background(), mcpServer)
		if err != nil {
			t.Fatalf("failed to resolve image: %v", err)
		}
		assertEqual(t, "image", image, "10.43.75.207:5000/test-image")
	})
}

func TestReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	t.Run("returns not found when MCPServer does not exist", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		result, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEqual(t, "result", result, ctrl.Result{})
	})

	t.Run("reconciles MCPServer successfully", func(t *testing.T) {
		replicas := int32(1)
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:            "test-image",
				ImageTag:         "latest",
				Port:             8088,
				ServicePort:      80,
				Replicas:         &replicas,
				IngressHost:      "example.com",
				IngressPath:      "/test-server/mcp",
				IngressClass:     "traefik",
				PublicPathPrefix: "test-server",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		result, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "test-server", Namespace: "default"},
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should only requeue for the readiness follow-up since all fields are set.
		assertEqual(t, "result", result, ctrl.Result{RequeueAfter: 10 * time.Second})
	})

	t.Run("reconciles with local defaults", func(t *testing.T) {
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:       "test-image",
				IngressHost: "example.com",
				IngressPath: "/test-server/mcp",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme}

		result, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "test-server", Namespace: "default"},
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEqual(t, "result", result, ctrl.Result{RequeueAfter: 10 * time.Second})

		var stored mcpv1alpha1.MCPServer
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-server", Namespace: "default"}, &stored); err != nil {
			t.Fatalf("failed to fetch stored MCPServer: %v", err)
		}
		assertEqual(t, "storedPort", stored.Spec.Port, int32(0))
		if stored.Spec.Replicas != nil {
			t.Fatalf("expected stored replicas to stay nil, got %v", *stored.Spec.Replicas)
		}
	})
}

func TestSetupWithManager(t *testing.T) {
	// SetupWithManager requires a real manager which is typically tested
	// via integration tests. This test verifies the reconciler struct
	// is properly configured to be set up with a manager.
	t.Run("reconciler has required fields for setup", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = mcpv1alpha1.AddToScheme(scheme)

		r := &MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test",
			Scheme:            scheme,
		}

		// Verify the reconciler has the scheme set (required for SetupWithManager)
		if r.Scheme == nil {
			t.Fatal("Scheme should not be nil")
		}
	})
}

func TestGatewayExternalBaseURLSchemeFollowsIngressTLS(t *testing.T) {
	server := func(annotations map[string]string) *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			Spec: mcpv1alpha1.MCPServerSpec{
				IngressHost:        "mcp.example.com",
				IngressAnnotations: annotations,
			},
		}
	}
	for _, testCase := range []struct {
		name        string
		defaultTLS  bool
		annotations map[string]string
		want        string
	}{
		{"plain http when no tls", false, nil, "http://mcp.example.com"},
		{"operator-wide tls default", true, nil, "https://mcp.example.com"},
		{
			name:        "per-server traefik tls annotation",
			annotations: map[string]string{"traefik.ingress.kubernetes.io/router.tls": "true"},
			want:        "https://mcp.example.com",
		},
		{
			name:        "annotation set to false stays http",
			annotations: map[string]string{"traefik.ingress.kubernetes.io/router.tls": "false"},
			want:        "http://mcp.example.com",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			r := MCPServerReconciler{
				GatewayProxyImage: "example.com/mcp-gateway:test", DefaultIngressTLS: testCase.defaultTLS}
			if got := r.gatewayExternalBaseURL(server(testCase.annotations)); got != testCase.want {
				t.Fatalf("gatewayExternalBaseURL = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestGatewayExternalBaseURLEmptyWithoutAnyHost(t *testing.T) {
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", DefaultIngressTLS: true}
	if got := r.gatewayExternalBaseURL(&mcpv1alpha1.MCPServer{}); got != "" {
		t.Fatalf("gatewayExternalBaseURL = %q, want empty", got)
	}
}

// Path-based servers set publicPathPrefix, which suppresses IngressHost
// defaulting in api/v1alpha1, so the gateway must fall back to the
// operator-wide host or it advertises no public URL at all.
func TestGatewayExternalBaseURLFallsBackToOperatorDefaultHost(t *testing.T) {
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}
	server := &mcpv1alpha1.MCPServer{
		Spec: mcpv1alpha1.MCPServerSpec{PublicPathPrefix: "demo"},
	}
	if got := r.gatewayExternalBaseURL(server); got != "https://mcp.example.com" {
		t.Fatalf("gatewayExternalBaseURL = %q, want https://mcp.example.com", got)
	}
}

func TestGatewayExternalBaseURLPrefersExplicitIngressHost(t *testing.T) {
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", DefaultIngressHost: "fallback.example.com", DefaultIngressTLS: true}
	server := &mcpv1alpha1.MCPServer{
		Spec: mcpv1alpha1.MCPServerSpec{IngressHost: "explicit.example.com"},
	}
	if got := r.gatewayExternalBaseURL(server); got != "https://explicit.example.com" {
		t.Fatalf("gatewayExternalBaseURL = %q, want https://explicit.example.com", got)
	}
}

func TestBuildServerEnvVarsDerivesOAuthResource(t *testing.T) {
	standalone := func(envVars ...mcpv1alpha1.EnvVar) *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "buddy"},
			Spec: mcpv1alpha1.MCPServerSpec{
				PublicPathPrefix: "buddy",
				Gateway:          &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(false)},
				Auth: &mcpv1alpha1.AuthConfig{
					IssuerURL: "https://auth.example.com/mcp-auth",
					Audience:  "https://mcp.example.com/buddy/mcp",
				},
				EnvVars: envVars,
			},
		}
	}
	envMap := func(envVars []corev1.EnvVar) map[string]string {
		result := map[string]string{}
		for _, env := range envVars {
			if _, dup := result[env.Name]; dup {
				t.Fatalf("env %s is set twice", env.Name)
			}
			result[env.Name] = env.Value
		}
		return result
	}
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test"}

	t.Run("standalone oauth server gets the derived resource settings", func(t *testing.T) {
		got := envMap(r.buildServerEnvVars(standalone()))
		for name, want := range map[string]string{
			"MCP_AUTH_RESOURCE":              "https://mcp.example.com/buddy/mcp",
			"MCP_AUTH_RESOURCE_METADATA_URL": "https://mcp.example.com/.well-known/oauth-protected-resource/buddy/mcp",
			"MCP_AUTH_ISSUER":                "https://auth.example.com/mcp-auth",
			"MCP_PATH":                       "/buddy/mcp",
		} {
			assertEqual(t, name, got[name], want)
		}
	})

	t.Run("operator owns derived env vars", func(t *testing.T) {
		got := envMap(r.buildServerEnvVars(standalone(mcpv1alpha1.EnvVar{Name: "MCP_PATH", Value: "/mcp"})))
		assertEqual(t, "MCP_PATH", got["MCP_PATH"], "/buddy/mcp")
		assertEqual(t, "MCP_AUTH_RESOURCE", got["MCP_AUTH_RESOURCE"], "https://mcp.example.com/buddy/mcp")
	})

	t.Run("gateway-fronted servers get only the operator-owned path", func(t *testing.T) {
		server := standalone()
		server.Spec.Gateway.Enabled = mcpv1alpha1.BoolPtr(true)
		got := envMap(r.buildServerEnvVars(server))
		assertEqual(t, "MCP_PATH", got["MCP_PATH"], "/buddy/mcp")
		if _, ok := got["MCP_AUTH_RESOURCE"]; ok {
			t.Fatalf("gateway-fronted server should not get MCP_AUTH_RESOURCE, got %v", got)
		}
	})

}

// A gateway with stripPrefix forwards the shortened path, so MCP_PATH must be
// where the server actually receives requests, not the public path.
func TestUpstreamMCPPathFollowsGatewayStripPrefix(t *testing.T) {
	server := func(prefix, strip string, gateway bool) *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "buddy"},
			Spec: mcpv1alpha1.MCPServerSpec{
				PublicPathPrefix: prefix,
				Gateway:          &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(gateway), StripPrefix: strip},
			},
		}
	}
	for _, testCase := range []struct {
		name   string
		server *mcpv1alpha1.MCPServer
		want   string
	}{
		{"no strip prefix", server("buddy", "", true), "/buddy/mcp"},
		{"strip prefix removes the public segment", server("buddy", "/buddy", true), "/mcp"},
		{"trailing slash on strip prefix", server("buddy", "/buddy/", true), "/mcp"},
		{"strip prefix equal to the whole path", server("buddy", "/buddy/mcp", true), "/"},
		{"non-matching strip prefix is ignored", server("buddy", "/other", true), "/buddy/mcp"},
		{"partial segment is not stripped", server("buddy", "/bud", true), "/buddy/mcp"},
		{"strip prefix without the gateway does nothing", server("buddy", "/buddy", false), "/buddy/mcp"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := upstreamMCPPath(testCase.server); got != testCase.want {
				t.Fatalf("upstreamMCPPath = %q, want %q", got, testCase.want)
			}
			env := map[string]string{}
			for _, e := range (&MCPServerReconciler{
				GatewayProxyImage: "example.com/mcp-gateway:test"}).buildServerEnvVars(testCase.server) {
				env[e.Name] = e.Value
			}
			if env["MCP_PATH"] != testCase.want {
				t.Fatalf("MCP_PATH = %q, want %q", env["MCP_PATH"], testCase.want)
			}
		})
	}
}
