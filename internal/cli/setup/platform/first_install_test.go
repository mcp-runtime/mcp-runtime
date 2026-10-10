package platform

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
	"os"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestRegistryRequestedSizeRenderedBeforeCreateAndShrinkRejected(t *testing.T) {
	raw, err := os.ReadFile("../../../../config/registry/base/pvc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := mutateRegistryManifest(string(raw), "", "", "5Gi", false)
	if err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err = yaml.Unmarshal([]byte(rendered), &pvc); err != nil {
		t.Fatal(err)
	}
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if requested.Cmp(resource.MustParse("5Gi")) != 0 {
		t.Fatal("fresh PVC did not use requested size")
	}
	clients := &k8sclient.Clients{Clientset: kubernetesfake.NewSimpleClientset()}
	if err = validateRegistryStorageSize(context.Background(), clients, "registry", "5Gi"); err != nil {
		t.Fatal(err)
	}
	pvc.ObjectMeta = metav1.ObjectMeta{Name: core.RegistryPVCName, Namespace: "registry"}
	pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}
	clients.Clientset = kubernetesfake.NewSimpleClientset(&pvc)
	if err = validateRegistryStorageSize(context.Background(), clients, "registry", "5Gi"); err == nil || !strings.Contains(err.Error(), "20Gi") {
		t.Fatalf("shrink guard: %v", err)
	}
	for _, action := range clients.Clientset.(*kubernetesfake.Clientset).Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("shrink validation mutated cluster")
		}
	}
	if err = validateRegistryStorageSize(context.Background(), clients, "registry", "25Gi"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildxMissingFailsBeforeBuildAndLegacyOptOutSkipsPlugin(t *testing.T) {
	t.Setenv("DOCKER_BUILDKIT", "1")
	mock := &core.MockExecutor{DefaultRunErr: errors.New("missing plugin")}
	t.Cleanup(core.SwapExecExecutor(mock))
	err := buildOperatorImage("example:test")
	if err == nil || !strings.Contains(err.Error(), "buildx") || len(mock.Commands) != 1 || mock.Commands[0].Name != "docker" {
		t.Fatalf("builder not stopped early: %v %#v", err, mock.Commands)
	}
	mock.Reset()
	err = SetupPlatform(zap.NewNop(), setupplan.Plan{}, nil)
	if err == nil || !strings.Contains(err.Error(), "buildx") || len(mock.Commands) != 1 {
		t.Fatalf("setup did not reject missing builder before cluster initialization: %v", err)
	}
	t.Setenv("DOCKER_BUILDKIT", "0")
	mock.Reset()
	if err := validateDockerImageBuilder(); err != nil || len(mock.Commands) != 0 {
		t.Fatal("legacy opt-out still requires buildx")
	}
}

func TestExternalTLSSettingsSurviveSetupAndRerun(t *testing.T) {
	raw, err := os.ReadFile("../../../../k8s/01-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("UI_REQUIRE_HTTPS", "false")
	t.Setenv("UI_FORCE_SECURE_COOKIE", "true")
	rendered, err := renderAnalyticsConfigManifestWithReaders(string(raw), setupplan.PlatformModeTenant, AnalyticsImageSet{}, func(string, string) (map[string]string, error) { return nil, nil }, func() string { return "traefik" })
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &config); err != nil {
		t.Fatal(err)
	}
	if config.Data["UI_REQUIRE_HTTPS"] != "false" || config.Data["UI_FORCE_SECURE_COOKIE"] != "true" {
		t.Fatal("external TLS settings missing")
	}
	t.Setenv("UI_REQUIRE_HTTPS", "")
	t.Setenv("UI_FORCE_SECURE_COOKIE", "")
	rerendered, err := renderAnalyticsConfigManifestWithReaders(string(raw), setupplan.PlatformModeTenant, AnalyticsImageSet{}, func(string, string) (map[string]string, error) { return config.Data, nil }, func() string { return "traefik" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rerendered, `UI_REQUIRE_HTTPS: "false"`) || !strings.Contains(rerendered, `UI_FORCE_SECURE_COOKIE: "true"`) {
		t.Fatal("rerun dropped external TLS settings")
	}
}
