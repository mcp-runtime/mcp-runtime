package platform

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

func withPlatformIngressHost(t *testing.T, host string) {
	t.Helper()
	original := core.DefaultCLIConfig.PlatformIngressHost
	core.DefaultCLIConfig.PlatformIngressHost = host
	t.Cleanup(func() { core.DefaultCLIConfig.PlatformIngressHost = original })
}

func clearPublicHostEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"MCP_PLATFORM_DOMAIN", "MCP_PLATFORM_INGRESS_HOST", "MCP_REGISTRY_INGRESS_HOST", "MCP_MCP_INGRESS_HOST"} {
		t.Setenv(key, "")
	}
}

func productionRegistryPlan() setupplan.Plan {
	return setupplan.Plan{TLSEnabled: true, DeployAnalytics: true, RegistryMode: setupplan.RegistryModeBundledHTTPS}
}

func TestNativeRegistryAuthRequiredForProductionShapedInstalls(t *testing.T) {
	clearPublicHostEnv(t)
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")
	withPlatformIngressHost(t, "platform.example.com")

	if !nativeRegistryAuthRequired(productionRegistryPlan(), false) {
		t.Fatal("production-shaped bundled install must enable native registry authentication")
	}
	if got := nativeRegistryRealm(); got != "https://platform.example.com/api/v1/registry/token" {
		t.Fatalf("realm = %q", got)
	}

	cases := map[string]func(*setupplan.Plan) bool{
		"test mode":          func(p *setupplan.Plan) bool { p.TestMode = true; return false },
		"no TLS":             func(p *setupplan.Plan) bool { p.TLSEnabled = false; return false },
		"no platform stack":  func(p *setupplan.Plan) bool { p.DeployAnalytics = false; return false },
		"external registry":  func(*setupplan.Plan) bool { return true },
		"bundled-http + TLS": func(p *setupplan.Plan) bool { p.RegistryMode = setupplan.RegistryModeBundledHTTP; return false },
	}
	for name, mutate := range cases {
		plan := productionRegistryPlan()
		external := mutate(&plan)
		want := name == "bundled-http + TLS"
		if got := nativeRegistryAuthRequired(plan, external); got != want {
			t.Fatalf("%s: required = %v, want %v", name, got, want)
		}
	}
}

func TestNativeRegistryAuthNeedsPublicPlatformHost(t *testing.T) {
	clearPublicHostEnv(t)
	withPlatformIngressHost(t, "platform.example.com")
	if nativeRegistryAuthRequired(productionRegistryPlan(), false) {
		t.Fatal("without public host configuration there is no HTTPS token realm")
	}

	t.Setenv("MCP_PLATFORM_INGRESS_HOST", "platform.local")
	for _, host := range []string{"platform.local", "127.0.0.1", "", "platform.example.com/path"} {
		withPlatformIngressHost(t, host)
		if got := nativeRegistryRealm(); got != "" {
			t.Fatalf("host %q produced realm %q", host, got)
		}
	}
}

func TestNativeRegistryAuthWarningExplainsLabInstalls(t *testing.T) {
	clearPublicHostEnv(t)
	withPlatformIngressHost(t, "")

	if got := nativeRegistryAuthWarning(setupplan.Plan{TestMode: true}, false); got != "" {
		t.Fatalf("test mode must not warn: %q", got)
	}
	if got := nativeRegistryAuthWarning(setupplan.Plan{}, true); got != "" {
		t.Fatalf("external registry must not warn: %q", got)
	}
	lab := nativeRegistryAuthWarning(setupplan.Plan{DeployAnalytics: true}, false)
	if !strings.Contains(lab, "--with-tls") || !strings.Contains(lab, "NetworkPolicy") {
		t.Fatalf("lab warning = %q", lab)
	}
	noStack := nativeRegistryAuthWarning(setupplan.Plan{TLSEnabled: true}, false)
	if !strings.Contains(noStack, "token broker") {
		t.Fatalf("missing platform stack warning = %q", noStack)
	}
	if !containsString(setupWarnings(setupplan.Plan{DeployAnalytics: true}, nil, false), lab) {
		t.Fatal("setup warnings must include the unauthenticated registry warning")
	}

	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")
	withPlatformIngressHost(t, "platform.example.com")
	if got := nativeRegistryAuthWarning(productionRegistryPlan(), false); got != "" {
		t.Fatalf("production-shaped install must not warn: %q", got)
	}
}

func TestBuildSetupStepsEndsWithNativeRegistryAuth(t *testing.T) {
	clearPublicHostEnv(t)
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")
	withPlatformIngressHost(t, "platform.example.com")

	steps := buildSetupSteps(&SetupContext{Plan: productionRegistryPlan()})
	if got := steps[len(steps)-1].Name(); got != "registry-native-auth" {
		t.Fatalf("last step = %q, want registry-native-auth after verify", got)
	}
	if got := steps[len(steps)-2].Name(); got != "verify" {
		t.Fatalf("step before native auth = %q, want verify", got)
	}

	for _, ctx := range []*SetupContext{
		{Plan: setupplan.Plan{TestMode: true, TLSEnabled: true, DeployAnalytics: true}},
		{Plan: productionRegistryPlan(), UsingExternalRegistry: true},
	} {
		for _, step := range buildSetupSteps(ctx) {
			if step.Name() == "registry-native-auth" {
				t.Fatalf("native registry auth must not run for %+v", ctx.Plan)
			}
		}
	}
}

func TestRegistryNativeAuthStepUsesRealmAndFailsClosed(t *testing.T) {
	clearPublicHostEnv(t)
	t.Setenv("MCP_PLATFORM_DOMAIN", "example.com")
	withPlatformIngressHost(t, "platform.example.com")

	var gotRealm string
	deps := SetupDeps{EnableNativeRegistryAuth: func(_ *zap.Logger, realm string) error {
		gotRealm = realm
		return nil
	}}
	if err := (registryNativeAuthStep{}).Run(zap.NewNop(), deps, &SetupContext{}); err != nil {
		t.Fatal(err)
	}
	if gotRealm != "https://platform.example.com/api/v1/registry/token" {
		t.Fatalf("realm = %q", gotRealm)
	}

	deps.EnableNativeRegistryAuth = func(*zap.Logger, string) error { return errors.New("broker unavailable") }
	err := (registryNativeAuthStep{}).Run(zap.NewNop(), deps, &SetupContext{})
	if err == nil || !errors.Is(err, core.ErrSetupStepFailed) || !strings.Contains(err.Error(), "rerun setup") {
		t.Fatalf("activation failure must fail setup with recovery guidance, got %v", err)
	}
}

func TestFirstCSVValue(t *testing.T) {
	for input, want := range map[string]string{"": "", " , ": "", "a,b": "a", " ,b ,c": "b"} {
		if got := firstCSVValue(input); got != want {
			t.Fatalf("firstCSVValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNativeRegistryPullSecretDetectsScopedCredential(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: defaultRegistrySecretName, Namespace: "mcp-platform", Labels: map[string]string{"mcpruntime.org/registry-auth": "pull"}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: defaultRegistrySecretName, Namespace: "mcp-runtime"}},
	)
	clients := &k8sclient.Clients{Clientset: cs}
	if !nativeRegistryPullSecret(context.Background(), clients, "mcp-platform") {
		t.Fatal("labeled node credential must be recognised")
	}
	if nativeRegistryPullSecret(context.Background(), clients, "mcp-runtime") {
		t.Fatal("legacy service-key pull Secret must still be checked for staleness")
	}
	if nativeRegistryPullSecret(context.Background(), nil, "mcp-platform") {
		t.Fatal("nil clients must report false")
	}
}
