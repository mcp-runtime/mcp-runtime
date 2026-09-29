// Package integration provides controller integration tests using envtest.
//
// These tests use controller-runtime's envtest to spin up a local API server
// and test the operator controller WITHOUT requiring a real Kubernetes cluster.
//
// envtest provides:
//   - Local API server (kube-apiserver)
//   - Local etcd
//   - CRD registration
//
// envtest does NOT provide:
//   - Kubernetes controllers (Deployment, Service, etc.)
//   - Kubelet or node simulation
//   - Networking
//
// Prerequisites:
//   - Install envtest binaries: setup-envtest use -p path k8s/1.28
//   - Set KUBEBUILDER_ASSETS to the path of the binaries
//
// Run with: go test -v ./test/integration/...
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/internal/operator"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// TestControllerWithEnvtest tests the full controller setup with envtest.
func TestControllerWithEnvtest(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Find CRD path (relative to test/integration/)
	crdPath := filepath.Join("..", "..", "config", "crd", "bases")
	assetsDir := os.Getenv("KUBEBUILDER_ASSETS")
	if assetsDir == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; skipping envtest integration")
	}
	if _, err := os.Stat(filepath.Join(assetsDir, "etcd")); err != nil {
		t.Skipf("envtest binaries not found in KUBEBUILDER_ASSETS: %v", err)
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assetsDir,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("failed to stop test environment: %v", err)
		}
	}()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	t.Run("SetupWithManager", func(t *testing.T) {
		testSetupWithManager(t, cfg, scheme)
	})

	t.Run("ReconcileCreatesResources", func(t *testing.T) {
		testReconcileCreatesResources(t, cfg, scheme)
	})

	t.Run("ReconcileHandlesDeletion", func(t *testing.T) {
		testReconcileHandlesDeletion(t, cfg, scheme)
	})

	t.Run("ReconcileUsesDefaultsWithoutPersistingSpec", func(t *testing.T) {
		testReconcileUsesDefaultsWithoutPersistingSpec(t, cfg, scheme)
	})
}

func testSetupWithManager(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    server.Options{BindAddress: "0"},
		Controller: skipControllerNameValidation(),
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	reconciler := &operator.MCPServerReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		GatewayProxyImage: "example.com/mcp-gateway:test",
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager failed: %v", err)
	}
	t.Log("SetupWithManager completed successfully")
}

func testReconcileCreatesResources(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	mgr, k8sClient, cancel := startManager(t, cfg, scheme)
	defer cancel()

	createNamespace(t, k8sClient, "test-create")

	replicas := int32(1)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "test-create",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:        "nginx",
			ImageTag:     "alpine",
			Port:         80,
			ServicePort:  80,
			Replicas:     &replicas,
			IngressHost:  "test.example.com",
			IngressPath:  "/test",
			IngressClass: "nginx",
		},
	}

	ctx := context.Background()
	if err := k8sClient.Create(ctx, mcpServer); err != nil {
		t.Fatalf("failed to create MCPServer: %v", err)
	}

	key := types.NamespacedName{Name: "test-server", Namespace: "test-create"}

	// Verify resources are created
	var deployment appsv1.Deployment
	if err := waitForResource(ctx, k8sClient, &deployment, key, 30*time.Second); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
	if len(deployment.OwnerReferences) == 0 {
		t.Error("deployment should have owner reference")
	}

	var service corev1.Service
	if err := waitForResource(ctx, k8sClient, &service, key, 10*time.Second); err != nil {
		t.Fatalf("service not created: %v", err)
	}

	var ingress networkingv1.Ingress
	if err := waitForResource(ctx, k8sClient, &ingress, key, 10*time.Second); err != nil {
		t.Fatalf("ingress not created: %v", err)
	}

	_ = mgr // suppress unused warning
	t.Log("All resources created with owner references")
}

func testReconcileHandlesDeletion(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	_, k8sClient, cancel := startManager(t, cfg, scheme)
	defer cancel()

	createNamespace(t, k8sClient, "test-delete")

	replicas := int32(1)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "delete-test",
			Namespace: "test-delete",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:        "nginx",
			ImageTag:     "alpine",
			Port:         80,
			ServicePort:  80,
			Replicas:     &replicas,
			IngressHost:  "delete.example.com",
			IngressPath:  "/delete",
			IngressClass: "nginx",
		},
	}

	ctx := context.Background()
	if err := k8sClient.Create(ctx, mcpServer); err != nil {
		t.Fatalf("failed to create MCPServer: %v", err)
	}

	key := types.NamespacedName{Name: "delete-test", Namespace: "test-delete"}

	// Wait for deployment to be created
	var deployment appsv1.Deployment
	if err := waitForResource(ctx, k8sClient, &deployment, key, 30*time.Second); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}

	// Delete MCPServer
	if err := k8sClient.Delete(ctx, mcpServer); err != nil {
		t.Fatalf("failed to delete MCPServer: %v", err)
	}

	// Verify MCPServer is deleted
	if err := waitForResourceDeletion(ctx, k8sClient, &mcpv1alpha1.MCPServer{}, key, 10*time.Second); err != nil {
		t.Fatalf("MCPServer not deleted: %v", err)
	}

	t.Log("MCPServer deletion handled successfully")
}

func testReconcileUsesDefaultsWithoutPersistingSpec(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	_, k8sClient, cancel := startManager(t, cfg, scheme)
	defer cancel()

	createNamespace(t, k8sClient, "test-defaults")

	// Create MCPServer with minimal spec
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "defaults-test",
			Namespace: "test-defaults",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "nginx",
			IngressHost: "defaults.example.com",
			IngressPath: "/defaults-test/mcp",
		},
	}

	ctx := context.Background()
	if err := k8sClient.Create(ctx, mcpServer); err != nil {
		t.Fatalf("failed to create MCPServer: %v", err)
	}

	key := types.NamespacedName{Name: "defaults-test", Namespace: "test-defaults"}

	var deployment appsv1.Deployment
	if err := waitForResource(ctx, k8sClient, &deployment, key, 30*time.Second); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("deployment should have at least one container")
	}
	ports := deployment.Spec.Template.Spec.Containers[0].Ports
	if len(ports) == 0 || ports[0].ContainerPort != 8088 {
		t.Fatalf("expected default container port 8088, got %#v", ports)
	}

	var service corev1.Service
	if err := waitForResource(ctx, k8sClient, &service, key, 10*time.Second); err != nil {
		t.Fatalf("service not created: %v", err)
	}
	if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].Port != 80 {
		t.Fatalf("expected default service port 80, got %#v", service.Spec.Ports)
	}

	var updated mcpv1alpha1.MCPServer
	if err := k8sClient.Get(ctx, key, &updated); err != nil {
		t.Fatalf("failed to get MCPServer: %v", err)
	}
	if updated.Spec.Port != 0 {
		t.Fatalf("controller should not persist default port, got %d", updated.Spec.Port)
	}
	if updated.Spec.Replicas != nil {
		t.Fatalf("controller should not persist default replicas, got %v", *updated.Spec.Replicas)
	}
	if updated.Spec.ImageTag != "" {
		t.Fatalf("controller should not persist default imageTag, got %q", updated.Spec.ImageTag)
	}
}

// Helper functions

func startManager(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) (ctrl.Manager, client.Client, context.CancelFunc) {
	t.Helper()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme,
		LeaderElection: false,
		Metrics:        server.Options{BindAddress: "0"},
		Controller:     skipControllerNameValidation(),
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	reconciler := &operator.MCPServerReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		GatewayProxyImage: "example.com/mcp-gateway:test",
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Logf("manager stopped: %v", err)
		}
	}()

	time.Sleep(2 * time.Second) // Wait for cache to sync

	return mgr, mgr.GetClient(), cancel
}

func skipControllerNameValidation() controllerconfig.Controller {
	skip := true
	return controllerconfig.Controller{SkipNameValidation: &skip}
}

func createNamespace(t *testing.T, c client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Logf("namespace %s may already exist: %v", name, err)
	}
}

func waitForResource(ctx context.Context, c client.Client, obj client.Object, key types.NamespacedName, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, obj); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return c.Get(ctx, key, obj)
}

func waitForResourceDeletion(ctx context.Context, c client.Client, obj client.Object, key types.NamespacedName, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, obj); err != nil {
			return nil // Resource deleted
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := c.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("timed out waiting for resource %s/%s deletion: %w", key.Namespace, key.Name, err)
	}
	return fmt.Errorf("timed out waiting for resource %s/%s deletion", key.Namespace, key.Name)
}
