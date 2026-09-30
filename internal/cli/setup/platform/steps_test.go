package platform

import (
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

type fakeRegistryManagerForSteps struct {
	showInfoCalls int32
}

func (f *fakeRegistryManagerForSteps) ShowRegistryInfo() error {
	atomic.AddInt32(&f.showInfoCalls, 1)
	return nil
}

func (f *fakeRegistryManagerForSteps) PushInCluster(_, _, _ string) error {
	return nil
}

type fakeClusterManagerForKubeconfig struct {
	init func(kubeconfig, context string) error
}

func (f *fakeClusterManagerForKubeconfig) InitCluster(kubeconfig, context string) error {
	if f.init != nil {
		return f.init(kubeconfig, context)
	}
	return nil
}

func (f *fakeClusterManagerForKubeconfig) ConfigureCluster(cluster.IngressOptions) error { return nil }

func TestBuildSetupStepsOrderWithTLS(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			TLSEnabled: true,
		},
	}
	steps := buildSetupSteps(ctx)
	if len(steps) != 8 {
		t.Fatalf("expected 8 steps, got %d", len(steps))
	}

	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name()
	}
	want := []string{"preflight", "cluster", "tls", "registry", "registry-auth-disable", "operator-image", "operator-deploy", "verify"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildSetupStepsOrderWithoutTLS(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			TLSEnabled: false,
		},
	}
	steps := buildSetupSteps(ctx)
	if len(steps) != 7 {
		t.Fatalf("expected 7 steps, got %d", len(steps))
	}

	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name()
	}
	want := []string{"preflight", "cluster", "registry", "registry-auth-disable", "operator-image", "operator-deploy", "verify"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestBuildSetupStepsOrderWithWorkloadPKI(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			MTLSClusterIssuer: setupplan.DefaultTestMTLSClusterIssuer,
		},
	}
	steps := buildSetupSteps(ctx)

	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name()
	}
	want := []string{"preflight", "cluster", "workload-pki", "registry", "registry-auth-disable", "operator-image", "operator-deploy", "verify"}
	if len(got) != len(want) {
		t.Fatalf("expected %d steps, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestTestModeDefaultsWorkloadIssuer(t *testing.T) {
	got := setupplan.Build(setupplan.Input{TestMode: true})
	if got.MTLSClusterIssuer != setupplan.DefaultTestMTLSClusterIssuer {
		t.Fatalf("MTLSClusterIssuer = %q, want %q", got.MTLSClusterIssuer, setupplan.DefaultTestMTLSClusterIssuer)
	}

	explicit := setupplan.Build(setupplan.Input{TestMode: true, MTLSClusterIssuer: "company-ca"})
	if explicit.MTLSClusterIssuer != "company-ca" {
		t.Fatalf("explicit MTLSClusterIssuer = %q, want company-ca", explicit.MTLSClusterIssuer)
	}

	production := setupplan.Build(setupplan.Input{})
	if production.MTLSClusterIssuer != "" {
		t.Fatalf("production MTLSClusterIssuer = %q, want empty", production.MTLSClusterIssuer)
	}
}

func TestBuildSetupStepsOrderWithAnalytics(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			DeployAnalytics: true,
		},
	}
	steps := buildSetupSteps(ctx)
	if len(steps) != 9 {
		t.Fatalf("expected 9 steps, got %d", len(steps))
	}

	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name()
	}
	want := []string{"preflight", "cluster", "registry", "registry-auth-disable", "operator-image", "analytics-images", "operator-deploy", "analytics-deploy", "verify"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestOperatorImageStepSetsContext(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{},
		ExternalRegistry: &config.ExternalRegistryConfig{
			URL: "registry.example.com",
		},
		UsingExternalRegistry: true,
	}
	deps := SetupDeps{
		OperatorImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-sentinel-mcp-gateway:latest"
		},
		BuildOperatorImage:     func(string) error { return nil },
		PushOperatorImage:      func(string) error { return nil },
		BuildGatewayProxyImage: func(string) error { return nil },
		PushGatewayProxyImage:  func(string) error { return nil },
	}

	step := operatorImageStep{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("operator image step failed: %v", err)
	}
	if ctx.OperatorImage != "registry.example.com/mcp-runtime-operator:latest" {
		t.Fatalf("expected operator image to be set, got %q", ctx.OperatorImage)
	}
	if ctx.GatewayProxyImage != "registry.example.com/mcp-sentinel-mcp-gateway:latest" {
		t.Fatalf("expected gateway proxy image to be set, got %q", ctx.GatewayProxyImage)
	}
}

func TestOperatorImageStepTestModeBuildsAndPushesToRegistry(t *testing.T) {
	var buildCalls int32
	var gatewayBuildCalls int32
	var pushCalls int32
	var gatewayPushCalls int32
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			TestMode: true,
		},
		ExternalRegistry:      &config.ExternalRegistryConfig{URL: "registry.example.com"},
		UsingExternalRegistry: true,
	}
	deps := SetupDeps{
		OperatorImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-sentinel-mcp-gateway:latest"
		},
		BuildOperatorImage: func(string) error { atomic.AddInt32(&buildCalls, 1); return nil },
		PushOperatorImage:  func(string) error { atomic.AddInt32(&pushCalls, 1); return nil },
		BuildGatewayProxyImage: func(string) error {
			atomic.AddInt32(&gatewayBuildCalls, 1)
			return nil
		},
		PushGatewayProxyImage: func(string) error { atomic.AddInt32(&gatewayPushCalls, 1); return nil },
	}

	step := operatorImageStep{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("operator image step failed: %v", err)
	}
	if ctx.OperatorImage != "registry.example.com/mcp-runtime-operator:latest" {
		t.Fatalf("expected test mode operator image to use registry, got %q", ctx.OperatorImage)
	}
	if ctx.GatewayProxyImage != "registry.example.com/mcp-sentinel-mcp-gateway:latest" {
		t.Fatalf("expected test mode gateway image to use registry, got %q", ctx.GatewayProxyImage)
	}
	if atomic.LoadInt32(&buildCalls) != 1 {
		t.Fatalf("expected operator build in test mode, got %d calls", buildCalls)
	}
	if atomic.LoadInt32(&gatewayBuildCalls) != 1 {
		t.Fatalf("expected gateway build in test mode, got %d calls", gatewayBuildCalls)
	}
	if atomic.LoadInt32(&pushCalls) != 1 {
		t.Fatalf("expected operator push in test mode, got %d calls", pushCalls)
	}
	if atomic.LoadInt32(&gatewayPushCalls) != 1 {
		t.Fatalf("expected gateway push in test mode, got %d calls", gatewayPushCalls)
	}
}

func TestDeployOperatorStepCmdPassesOperatorArgs(t *testing.T) {
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			OperatorArgs: []string{"--metrics-bind-address=:9090", "--leader-elect=false"},
		},
		OperatorImage:         "registry.example.com/mcp-runtime-operator:latest",
		GatewayProxyImage:     "registry.example.com/mcp-sentinel-mcp-gateway:latest",
		UsingExternalRegistry: false,
	}
	var gotArgs []string
	var gotGatewayImage string
	deps := SetupDeps{
		DeployOperatorManifests: func(_ *zap.Logger, image, gatewayImage string, args []string, imagePullSecretName string) error {
			if image != ctx.OperatorImage {
				t.Fatalf("expected operator image %q, got %q", ctx.OperatorImage, image)
			}
			gotGatewayImage = gatewayImage
			gotArgs = append([]string(nil), args...)
			return nil
		},
		RestartDeployment: func(string, string) error { return nil },
	}

	step := deployOperatorStepCmd{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("deploy operator step failed: %v", err)
	}
	if len(gotArgs) != len(ctx.Plan.OperatorArgs) {
		t.Fatalf("expected %d operator args, got %d (%v)", len(ctx.Plan.OperatorArgs), len(gotArgs), gotArgs)
	}
	for i := range ctx.Plan.OperatorArgs {
		if gotArgs[i] != ctx.Plan.OperatorArgs[i] {
			t.Fatalf("expected operator arg %d to be %q, got %q", i, ctx.Plan.OperatorArgs[i], gotArgs[i])
		}
	}
	if gotGatewayImage != ctx.GatewayProxyImage {
		t.Fatalf("expected gateway image %q, got %q", ctx.GatewayProxyImage, gotGatewayImage)
	}
}

func TestDeployOperatorStepCmdCreatesOperatorPullSecretForExternalRegistry(t *testing.T) {
	ctx := &SetupContext{
		ExternalRegistry: &config.ExternalRegistryConfig{
			URL:      "registry.example.com",
			Username: "user",
			Password: "pass",
		},
		UsingExternalRegistry: true,
		RegistrySecretName:    defaultRegistrySecretName,
		OperatorImage:         "registry.example.com/mcp-runtime-operator:latest",
	}
	var ensuredNamespace string
	var ensuredName string
	var gotPullSecret string
	deps := SetupDeps{
		EnsureImagePullSecret: func(namespace, name, registryURL, username, password string) error {
			ensuredNamespace = namespace
			ensuredName = name
			if registryURL != ctx.ExternalRegistry.URL || username != "user" || password != "pass" {
				t.Fatalf("unexpected pull secret inputs: %q %q/%q", registryURL, username, password)
			}
			return nil
		},
		DeployOperatorManifests: func(_ *zap.Logger, image, gatewayImage string, args []string, imagePullSecretName string) error {
			gotPullSecret = imagePullSecretName
			return nil
		},
		ConfigureProvisionedRegistryEnv: func(*config.ExternalRegistryConfig, string) error { return nil },
		RestartDeployment:               func(string, string) error { return nil },
	}

	step := deployOperatorStepCmd{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("deploy operator step failed: %v", err)
	}
	wantName := defaultPlatformRegistryPullSecretName
	if ensuredNamespace != core.NamespaceMCPRuntime || ensuredName != wantName {
		t.Fatalf("expected operator pull secret %s/%s, got %s/%s", core.NamespaceMCPRuntime, wantName, ensuredNamespace, ensuredName)
	}
	if gotPullSecret != ensuredName {
		t.Fatalf("expected deployed operator pull secret %q, got %q", ensuredName, gotPullSecret)
	}
}

func TestClusterStepPassesKubeconfigAndContext(t *testing.T) {
	var gotKubeconfig string
	var gotContext string

	deps := SetupDeps{
		ClusterManager: &fakeClusterManagerForKubeconfig{
			init: func(kubeconfig, context string) error {
				gotKubeconfig = kubeconfig
				gotContext = context
				return nil
			},
		},
	}

	ctx := &SetupContext{
		Plan: setupplan.Plan{
			Kubeconfig: "/etc/rancher/k3s/k3s.yaml",
			Context:    "k3s",
			Ingress: cluster.IngressOptions{
				Mode:     "traefik",
				Manifest: "config/ingress/overlays/http",
			},
		},
	}

	step := clusterStep{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("cluster step failed: %v", err)
	}
	if gotKubeconfig != ctx.Plan.Kubeconfig {
		t.Fatalf("expected kubeconfig %q, got %q", ctx.Plan.Kubeconfig, gotKubeconfig)
	}
	if gotContext != ctx.Plan.Context {
		t.Fatalf("expected context %q, got %q", ctx.Plan.Context, gotContext)
	}
}

func TestRegistryStepDeploysInternalRegistry(t *testing.T) {
	var deployCalls int32
	var waitCalls int32
	fakeRegistry := &fakeRegistryManagerForSteps{}
	ctx := &SetupContext{
		Plan: setupplan.Plan{
			RegistryType:        "docker",
			RegistryStorageSize: "1Gi",
			RegistryManifest:    "config/registry",
		},
		UsingExternalRegistry: false,
	}
	deps := SetupDeps{
		DeployRegistry: func(_ *zap.Logger, namespace string, port int, registryType, registryStorageSize, manifestPath string) error {
			if namespace != "registry" || port != 5000 || registryType != "docker" || registryStorageSize != "1Gi" || manifestPath != "config/registry" {
				t.Fatalf("unexpected deploy args: %s %d %s %s %s", namespace, port, registryType, registryStorageSize, manifestPath)
			}
			atomic.AddInt32(&deployCalls, 1)
			return nil
		},
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, namespace, selector string, _ time.Duration) error {
			if name != "registry" || namespace != "registry" || selector != "app=registry" {
				t.Fatalf("unexpected wait args: %s %s %s", name, namespace, selector)
			}
			atomic.AddInt32(&waitCalls, 1)
			return nil
		},
		PrintDeploymentDiagnostics: func(_, _, _ string) {},
		GetDeploymentTimeout:       func() time.Duration { return time.Second },
		GetRegistryPort:            func() int { return 5000 },
		RegistryManager:            fakeRegistry,
	}

	step := registryStep{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("registry step failed: %v", err)
	}
	if atomic.LoadInt32(&deployCalls) != 1 {
		t.Fatalf("expected deploy to be called once, got %d", deployCalls)
	}
	if atomic.LoadInt32(&waitCalls) != 1 {
		t.Fatalf("expected wait to be called once, got %d", waitCalls)
	}
	if atomic.LoadInt32(&fakeRegistry.showInfoCalls) != 1 {
		t.Fatalf("expected registry info to be shown once, got %d", fakeRegistry.showInfoCalls)
	}
}

func TestVerifyStepCallsChecks(t *testing.T) {
	var waitCalls int32
	var crdCalls int32
	var smokeCalls int32
	ctx := &SetupContext{
		UsingExternalRegistry: false,
	}
	deps := SetupDeps{
		WaitForDeploymentAvailable: func(_ *zap.Logger, name, namespace, selector string, _ time.Duration) error {
			atomic.AddInt32(&waitCalls, 1)
			return nil
		},
		PrintDeploymentDiagnostics: func(_, _, _ string) {},
		CheckCRDInstalled: func(_ string) error {
			atomic.AddInt32(&crdCalls, 1)
			return nil
		},
		RunPostSetupSmoke: func() error {
			atomic.AddInt32(&smokeCalls, 1)
			return nil
		},
		GetDeploymentTimeout: func() time.Duration { return time.Second },
	}

	step := verifyStep{}
	if err := step.Run(zap.NewNop(), deps, ctx); err != nil {
		t.Fatalf("verify step failed: %v", err)
	}
	if atomic.LoadInt32(&waitCalls) != 2 {
		t.Fatalf("expected 2 wait calls, got %d", waitCalls)
	}
	if atomic.LoadInt32(&crdCalls) != 1 {
		t.Fatalf("expected 1 CRD check, got %d", crdCalls)
	}
	if atomic.LoadInt32(&smokeCalls) != 1 {
		t.Fatalf("expected 1 post-setup smoke call, got %d", smokeCalls)
	}
}
