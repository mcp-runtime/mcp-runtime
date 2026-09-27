package platform

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"mcp-runtime/internal/cli/cluster"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

type helperFakeClusterManager struct{}

func (f *helperFakeClusterManager) InitCluster(_, _ string) error                   { return nil }
func (f *helperFakeClusterManager) ConfigureCluster(_ cluster.IngressOptions) error { return nil }

type helperFakeRegistryManager struct{}

func (f *helperFakeRegistryManager) ShowRegistryInfo() error { return nil }
func (f *helperFakeRegistryManager) PushInCluster(_, _, _ string) error {
	return nil
}

func secretStringDataFromManifest(t *testing.T, manifest string) map[string]string {
	t.Helper()
	var payload struct {
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &payload); err != nil {
		t.Fatalf("unmarshal secret manifest: %v", err)
	}
	if payload.StringData == nil {
		t.Fatalf("secret manifest missing stringData: %q", manifest)
	}
	return payload.StringData
}

func namespaceLabelsFromManifest(t *testing.T, manifest, name string) (map[string]string, bool) {
	t.Helper()
	var payload struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name   string            `yaml:"name"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &payload); err != nil {
		t.Fatalf("unmarshal namespace manifest: %v", err)
	}
	if payload.Kind != "Namespace" || payload.Metadata.Name != name {
		return nil, false
	}
	return payload.Metadata.Labels, true
}

func csvHasValue(csv, value string) bool {
	value = strings.TrimSpace(value)
	for _, part := range strings.Split(csv, ",") {
		if strings.TrimSpace(part) == value {
			return true
		}
	}
	return false
}

func TestResolveSetupImagePlatformUsesNodeArchitecture(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	t.Cleanup(resetSetupImagePlatformCacheForTest)
	core.DefaultCLIConfig = &core.CLIConfig{}
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{DefaultOutput: []byte("amd64\namd64\n")})

	got, err := resolveSetupImagePlatform(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "linux/amd64" {
		t.Fatalf("expected linux/amd64, got %q", got)
	}
}

func TestResolveSetupImagePlatformCachesNodeArchitecture(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	t.Cleanup(resetSetupImagePlatformCacheForTest)
	core.DefaultCLIConfig = &core.CLIConfig{}
	mock := &core.MockExecutor{DefaultOutput: []byte("arm64\n")}
	kubectl := core.NewTestKubectlClient(mock)

	for i := 0; i < 3; i++ {
		got, err := resolveSetupImagePlatform(kubectl)
		if err != nil {
			t.Fatalf("resolveSetupImagePlatform call %d returned error: %v", i+1, err)
		}
		if got != "linux/arm64" {
			t.Fatalf("resolveSetupImagePlatform call %d = %q, want linux/arm64", i+1, got)
		}
	}
	if len(mock.Commands) != 1 {
		t.Fatalf("expected one kubectl architecture lookup, got %d", len(mock.Commands))
	}
}

func TestResolveSetupImagePlatformRejectsMixedNodeArchitectures(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	t.Cleanup(resetSetupImagePlatformCacheForTest)
	core.DefaultCLIConfig = &core.CLIConfig{}
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{DefaultOutput: []byte("amd64\narm64\n")})

	_, err := resolveSetupImagePlatform(kubectl)
	if err == nil || !strings.Contains(err.Error(), "mixed Kubernetes node architectures") {
		t.Fatalf("expected mixed architecture error, got %v", err)
	}
}

func TestResolveSetupImagePlatformRejectsExplicitMismatch(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	t.Cleanup(resetSetupImagePlatformCacheForTest)
	core.DefaultCLIConfig = &core.CLIConfig{ImagePlatform: "linux/arm64"}
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{DefaultOutput: []byte("amd64\n")})

	_, err := resolveSetupImagePlatform(kubectl)
	if err == nil || !strings.Contains(err.Error(), "does not match Kubernetes node architecture") {
		t.Fatalf("expected explicit mismatch error, got %v", err)
	}
}

func TestResolveSetupImagePlatformIncludesKubectlStderr(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	t.Cleanup(resetSetupImagePlatformCacheForTest)
	core.DefaultCLIConfig = &core.CLIConfig{}
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		DefaultOutput: []byte("Error from server (Forbidden): nodes is forbidden"),
		DefaultErr:    errors.New("exit status 1"),
	})

	_, err := resolveSetupImagePlatform(kubectl)
	if err == nil || !strings.Contains(err.Error(), "nodes is forbidden") {
		t.Fatalf("expected kubectl stderr in error, got %v", err)
	}
}

func TestBuildOperatorImagePassesDockerPlatform(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{ImagePlatform: "linux/amd64"}
	swapKubernetesClientsForTest(t, platformTestClientsWithNodeArchitectures("amd64"))
	mockExec := &core.MockExecutor{}
	restoreExec := core.SwapExecExecutor(mockExec)
	t.Cleanup(restoreExec)

	if err := buildOperatorImage("registry.example.com/mcp-runtime-operator:test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockExec.Commands) != 1 {
		t.Fatalf("expected one command, got %#v", mockExec.Commands)
	}
	cmd := mockExec.Commands[0]
	if cmd.Name != "make" || !contains(cmd.Args, "DOCKER_PLATFORM=linux/amd64") {
		t.Fatalf("expected make command with DOCKER_PLATFORM, got %s %#v", cmd.Name, cmd.Args)
	}
}

func TestGenerateOperatorWebhookCertificate(t *testing.T) {
	caPEM, caKeyPEM, certPEM, keyPEM, err := generateOperatorWebhookCertificate(time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}

	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatal("expected CA certificate PEM block")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	if !caCert.IsCA {
		t.Fatal("expected CA certificate to be a CA")
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		t.Fatal("expected CA private key PEM block")
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("expected serving certificate PEM block")
	}
	servingCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("parse serving certificate: %v", err)
	}
	if servingCert.IsCA {
		t.Fatal("serving certificate should not be a CA")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("expected serving private key PEM block")
	}

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	serviceDNS := operatorWebhookServiceName + "." + core.NamespaceMCPRuntime + ".svc"
	if _, err := servingCert.Verify(x509.VerifyOptions{DNSName: serviceDNS, Roots: roots}); err != nil {
		t.Fatalf("serving certificate did not verify for %s: %v", serviceDNS, err)
	}
	if !caCert.NotAfter.After(servingCert.NotAfter) {
		t.Fatalf("CA expiry %s must outlive serving certificate expiry %s", caCert.NotAfter, servingCert.NotAfter)
	}
}

func TestReusableOperatorWebhookServingCert(t *testing.T) {
	issued := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	caPEM, _, certPEM, keyPEM, err := generateOperatorWebhookCertificate(issued)
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}

	if !reusableOperatorWebhookServingCert(caPEM, certPEM, keyPEM, issued) {
		t.Fatal("expected freshly generated certificate to be reusable")
	}
	insideRenewalWindow := issued.AddDate(1, 0, 0).Add(-29 * 24 * time.Hour)
	if reusableOperatorWebhookServingCert(caPEM, certPEM, keyPEM, insideRenewalWindow) {
		t.Fatal("expected certificate inside the renewal window to be rotated")
	}
	if reusableOperatorWebhookServingCert(nil, certPEM, keyPEM, issued) {
		t.Fatal("expected secret without ca.crt to be rotated")
	}

	_, _, _, otherKeyPEM, err := generateOperatorWebhookCertificate(issued)
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}
	if reusableOperatorWebhookServingCert(caPEM, certPEM, otherKeyPEM, issued) {
		t.Fatal("expected mismatched private key to be rotated")
	}
}

func operatorWebhookSecretJSON(caPEM, caKeyPEM, certPEM, keyPEM []byte) []byte {
	return []byte(fmt.Sprintf(`{"data":{"ca.crt":%q,"ca.key":%q,"tls.crt":%q,"tls.key":%q}}`,
		base64.StdEncoding.EncodeToString(caPEM),
		base64.StdEncoding.EncodeToString(caKeyPEM),
		base64.StdEncoding.EncodeToString(certPEM),
		base64.StdEncoding.EncodeToString(keyPEM)))
}

func TestEnsureOperatorWebhookTLSSecretReusesValidSecret(t *testing.T) {
	caPEM, caKeyPEM, certPEM, keyPEM, err := generateOperatorWebhookCertificate(time.Now().UTC())
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "secret", operatorWebhookSecretName) {
				return &core.MockCommand{Args: spec.Args, OutputData: operatorWebhookSecretJSON(caPEM, caKeyPEM, certPEM, keyPEM)}
			}
			if commandHasArgs(spec, "apply") {
				t.Errorf("expected no apply while reusing webhook secret, got %#v", spec.Args)
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	got, err := ensureOperatorWebhookTLSSecret(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(caPEM) {
		t.Fatal("expected existing CA bundle to be returned without rotation")
	}
}

func TestEnsureOperatorWebhookTLSSecretRenewsExpiringServingCert(t *testing.T) {
	// Issued ~11.5 months ago, the serving certificate expires inside the
	// 30-day renewal window and must be re-signed with the stored CA.
	caPEM, caKeyPEM, certPEM, keyPEM, err := generateOperatorWebhookCertificate(time.Now().UTC().AddDate(0, -11, -15))
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}
	applied := 0
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "secret", operatorWebhookSecretName) {
				cmd.OutputData = operatorWebhookSecretJSON(caPEM, caKeyPEM, certPEM, keyPEM)
			}
			if commandHasArgs(spec, "apply") {
				applied++
				cmd.RunFunc = func() error {
					_, err := io.ReadAll(cmd.StdinR)
					return err
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	got, err := ensureOperatorWebhookTLSSecret(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied == 0 {
		t.Fatal("expected expiring webhook secret to be re-applied")
	}
	if string(got) != string(caPEM) {
		t.Fatal("expected serving-certificate renewal to keep the existing CA bundle")
	}
}

func TestEnsureOperatorWebhookTLSSecretRotatesLegacySecretWithoutCAKey(t *testing.T) {
	caPEM, _, certPEM, keyPEM, err := generateOperatorWebhookCertificate(time.Now().UTC().AddDate(0, -11, -15))
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}
	applied := 0
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "secret", operatorWebhookSecretName) {
				cmd.OutputData = []byte(fmt.Sprintf(`{"data":{"ca.crt":%q,"tls.crt":%q,"tls.key":%q}}`,
					base64.StdEncoding.EncodeToString(caPEM),
					base64.StdEncoding.EncodeToString(certPEM),
					base64.StdEncoding.EncodeToString(keyPEM)))
			}
			if commandHasArgs(spec, "apply") {
				applied++
				cmd.RunFunc = func() error {
					_, err := io.ReadAll(cmd.StdinR)
					return err
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	got, err := ensureOperatorWebhookTLSSecret(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied == 0 {
		t.Fatal("expected legacy webhook secret without ca.key to be re-applied")
	}
	if string(got) == string(caPEM) {
		t.Fatal("expected legacy secret without ca.key to rotate the CA bundle")
	}
}

func TestEnsureOperatorWebhookTLSSecretClientGoReusesValidSecret(t *testing.T) {
	caPEM, caKeyPEM, certPEM, keyPEM, err := generateOperatorWebhookCertificate(time.Now().UTC())
	if err != nil {
		t.Fatalf("generateOperatorWebhookCertificate failed: %v", err)
	}
	resetPlatformKubeconfig(t)
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients([]runtime.Object{&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorWebhookSecretName, Namespace: core.NamespaceMCPRuntime},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":  caPEM,
			"ca.key":  caKeyPEM,
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}}, nil))

	got, err := ensureOperatorWebhookTLSSecretClientGo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(caPEM) {
		t.Fatal("expected existing CA bundle to be returned without rotation")
	}
}

func TestInjectOperatorWebhookCABundleQualifiesConfigurationNames(t *testing.T) {
	rendered, err := injectOperatorWebhookCABundle([]byte(`
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: mutating-webhook-configuration
webhooks:
- name: example
  clientConfig: {}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: validating-webhook-configuration
webhooks:
- name: example
  clientConfig: {}
`), []byte("test-ca"))
	if err != nil {
		t.Fatalf("injectOperatorWebhookCABundle failed: %v", err)
	}
	body := string(rendered)
	for _, expected := range []string{
		"name: mcp-runtime-mutating-webhook-configuration",
		"name: mcp-runtime-validating-webhook-configuration",
		"caBundle:",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("rendered webhook manifest missing %q:\n%s", expected, body)
		}
	}
}

func TestGetOperatorImage(t *testing.T) {
	origOverride := core.DefaultCLIConfig.OperatorImage
	origTagResolver := setupImageTagResolver
	t.Cleanup(func() {
		core.DefaultCLIConfig.OperatorImage = origOverride
		setupImageTagResolver = origTagResolver
	})

	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	setupImageTagResolver = func() string { return "deadbeef" }

	t.Run("uses override when set", func(t *testing.T) {
		core.DefaultCLIConfig.OperatorImage = "override/operator:v1"
		got := getOperatorImage(nil)
		if got != "override/operator:v1" {
			t.Fatalf("expected override image, got %q", got)
		}
	})

	t.Run("uses external registry URL", func(t *testing.T) {
		core.DefaultCLIConfig.OperatorImage = ""
		ext := &config.ExternalRegistryConfig{URL: "registry.example.com/"}
		got := getOperatorImage(ext)
		if got != "registry.example.com/mcp-runtime-operator:latest" {
			t.Fatalf("unexpected external registry image: %q", got)
		}
	})

	t.Run("uses platform registry URL when external not set", func(t *testing.T) {
		core.DefaultCLIConfig.OperatorImage = ""
		swapKubernetesClientsForTest(t, platformTestClientsWithRegistryService(5000))
		got := getOperatorImage(nil)
		if got != "registry.registry.svc.cluster.local:5000/mcp-runtime-operator:latest" {
			t.Fatalf("unexpected platform registry image: %q", got)
		}
	})

	t.Run("uses versioned tag outside test mode", func(t *testing.T) {
		core.DefaultCLIConfig.OperatorImage = ""
		t.Setenv("MCP_RUNTIME_TEST_MODE", "")
		ext := &config.ExternalRegistryConfig{URL: "registry.example.com/"}
		got := getOperatorImage(ext)
		if got != "registry.example.com/mcp-runtime-operator:deadbeef" {
			t.Fatalf("unexpected versioned image: %q", got)
		}
	})
}

func TestGetGatewayProxyImage(t *testing.T) {
	origOverride := core.DefaultCLIConfig.GatewayProxyImage
	origTagResolver := setupImageTagResolver
	t.Cleanup(func() {
		core.DefaultCLIConfig.GatewayProxyImage = origOverride
		setupImageTagResolver = origTagResolver
	})

	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	setupImageTagResolver = func() string { return "deadbeef" }

	t.Run("uses override when set", func(t *testing.T) {
		core.DefaultCLIConfig.GatewayProxyImage = "override/mcp-gateway:v1"
		got := getGatewayProxyImage(nil)
		if got != "override/mcp-gateway:v1" {
			t.Fatalf("expected override image, got %q", got)
		}
	})

	t.Run("uses external registry URL", func(t *testing.T) {
		core.DefaultCLIConfig.GatewayProxyImage = ""
		ext := &config.ExternalRegistryConfig{URL: "registry.example.com/"}
		got := getGatewayProxyImage(ext)
		if got != "registry.example.com/mcp-sentinel-mcp-gateway:latest" {
			t.Fatalf("unexpected external registry image: %q", got)
		}
	})

	t.Run("uses platform registry URL when external not set", func(t *testing.T) {
		core.DefaultCLIConfig.GatewayProxyImage = ""
		swapKubernetesClientsForTest(t, platformTestClientsWithRegistryService(5000))
		got := getGatewayProxyImage(nil)
		if got != "registry.registry.svc.cluster.local:5000/mcp-sentinel-mcp-gateway:latest" {
			t.Fatalf("unexpected platform registry image: %q", got)
		}
	})

	t.Run("uses versioned tag outside test mode", func(t *testing.T) {
		core.DefaultCLIConfig.GatewayProxyImage = ""
		t.Setenv("MCP_RUNTIME_TEST_MODE", "")
		ext := &config.ExternalRegistryConfig{URL: "registry.example.com/"}
		got := getGatewayProxyImage(ext)
		if got != "registry.example.com/mcp-sentinel-mcp-gateway:deadbeef" {
			t.Fatalf("unexpected versioned image: %q", got)
		}
	})
}

func TestPlatformImageDefaultsUseInternalRegistryWithPlatformDomain(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	origTagResolver := setupImageTagResolver
	t.Cleanup(func() {
		core.DefaultCLIConfig = origConfig
		setupImageTagResolver = origTagResolver
	})

	for _, key := range []string{
		"MCP_REGISTRY_ENDPOINT",
		"MCP_REGISTRY_HOST",
		"MCP_REGISTRY_INGRESS_HOST",
		"MCP_PLATFORM_DOMAIN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	t.Setenv("MCP_PLATFORM_DOMAIN", "mcpruntime.org")
	setupImageTagResolver = func() string { return "deadbeef" }
	core.DefaultCLIConfig = core.LoadCLIConfig()
	swapKubernetesClientsForTest(t, platformTestClientsWithRegistryService(5000))

	gotOperator := getOperatorImage(nil)
	if gotOperator != "registry.registry.svc.cluster.local:5000/mcp-runtime-operator:latest" {
		t.Fatalf("operator image = %q, want internal registry service DNS", gotOperator)
	}
	gotGateway := getGatewayProxyImage(nil)
	if gotGateway != "registry.registry.svc.cluster.local:5000/mcp-sentinel-mcp-gateway:latest" {
		t.Fatalf("gateway image = %q, want internal registry service DNS", gotGateway)
	}
	gotAPI := analyticsImageFor(nil, "mcp-platform-api")
	if gotAPI != "registry.registry.svc.cluster.local:5000/mcp-platform-api:latest" {
		t.Fatalf("analytics image = %q, want internal registry service DNS", gotAPI)
	}
}

func TestApplyPlatformIngressPrunesPathBasedSentinelIngresses(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
	core.DefaultCLIConfig = &core.CLIConfig{PlatformIngressHost: "platform.example.com"}

	clients := platformTestClientsWithIngresses(pathBasedSentinelIngressNames...)
	swapKubernetesClientsForTest(t, clients)

	if err := applyPlatformIngressIfConfigured(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertPlatformIngressAppliedForTest(t, clients, "platform.example.com")
	for _, name := range pathBasedSentinelIngressNames {
		assertIngressDeletedForTest(t, clients, core.DefaultAnalyticsNamespace, name)
	}
}

func TestApplyPlatformIngressSkipsWhenHostUnset(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
	core.DefaultCLIConfig = &core.CLIConfig{}

	if err := applyPlatformIngressIfConfigured(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildOperatorArgs(t *testing.T) {
	t.Run("omits defaults", func(t *testing.T) {
		if got := buildOperatorArgs("", "", false, false); len(got) != 0 {
			t.Fatalf("expected no operator args, got %v", got)
		}
	})

	t.Run("includes explicit overrides", func(t *testing.T) {
		got := buildOperatorArgs(":9090", ":9091", false, true)
		want := []string{
			"--metrics-bind-address=:9090",
			"--health-probe-bind-address=:9091",
			"--leader-elect=false",
		}
		if len(got) != len(want) {
			t.Fatalf("expected %d args, got %d (%v)", len(want), len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("expected arg %d to be %q, got %q", i, want[i], got[i])
			}
		}
	})
}

func findOperatorEnvVar(envVars []operatorEnvVar, name string) *operatorEnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			return &envVars[i]
		}
	}
	return nil
}

func requireOperatorEnvVar(t *testing.T, envVars []operatorEnvVar, name, want string) {
	t.Helper()
	envVar := findOperatorEnvVar(envVars, name)
	if envVar == nil {
		t.Fatalf("expected operator env %s in %v", name, envVars)
	}
	if envVar.Value != want {
		t.Fatalf("operator env %s = %q, want %q", name, envVar.Value, want)
	}
}

func requireOperatorEnvVarNonEmpty(t *testing.T, envVars []operatorEnvVar, name string) {
	t.Helper()
	envVar := findOperatorEnvVar(envVars, name)
	if envVar == nil {
		t.Fatalf("expected operator env %s in %v", name, envVars)
	}
	if strings.TrimSpace(envVar.Value) == "" {
		t.Fatalf("operator env %s was empty in %v", name, envVars)
	}
}

func TestOperatorEnvOverrides(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	t.Setenv("OAUTH_INTERNAL_ISSUER_URL", "")
	t.Setenv("MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", "")
	origAuthPresent := bundledMCPAuthServicePresent
	bundledMCPAuthServicePresent = func() bool { return false }
	t.Cleanup(func() { bundledMCPAuthServicePresent = origAuthPresent })

	t.Run("derives internal OAuth issuer from bundled mcp-auth Service when the mcp-auth step is skipped", func(t *testing.T) {
		bundledMCPAuthServicePresent = func() bool { return true }
		t.Cleanup(func() { bundledMCPAuthServicePresent = func() bool { return false } })
		core.DefaultCLIConfig = &core.CLIConfig{}
		got := operatorEnvOverrides("", "")
		requireOperatorEnvVar(t, got, "OAUTH_INTERNAL_ISSUER_URL", mcpAuthInternalIssuerURLForCluster())
	})

	t.Run("omits internal OAuth issuer without bundled mcp-auth or explicit env", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{}
		for _, envVar := range operatorEnvOverrides("", "") {
			if envVar.Name == "OAUTH_INTERNAL_ISSUER_URL" {
				t.Fatalf("OAUTH_INTERNAL_ISSUER_URL = %q, want unset", envVar.Value)
			}
		}
	})

	t.Run("includes explicit internal OAuth issuer", func(t *testing.T) {
		t.Setenv("OAUTH_INTERNAL_ISSUER_URL", "http://mcp-auth-server.mcp-sentinel.svc.cluster.local:8080")
		core.DefaultCLIConfig = &core.CLIConfig{}
		got := operatorEnvOverrides("", "")
		requireOperatorEnvVar(t, got, "OAUTH_INTERNAL_ISSUER_URL", "http://mcp-auth-server.mcp-sentinel.svc.cluster.local:8080")
	})

	t.Run("returns empty when no gateway override is set", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{}
		got := operatorEnvOverrides("", "")
		if len(got) != 3 {
			t.Fatalf("expected gateway otel, default analytics ingest, and registry endpoint env only, got %v", got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("returns gateway proxy image override", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{GatewayProxyImage: "example.com/mcp-gateway:latest"}
		got := operatorEnvOverrides("", "")
		if len(got) != 4 {
			t.Fatalf("expected gateway, analytics, and registry endpoint env overrides, got %d (%v)", len(got), got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_PROXY_IMAGE", "example.com/mcp-gateway:latest")
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("prefers explicit setup image over config override", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{
			GatewayProxyImage:  "example.com/mcp-gateway:config",
			AnalyticsIngestURL: "http://custom-analytics-ingest",
		}
		got := operatorEnvOverrides("example.com/mcp-gateway:setup", "")
		if len(got) != 4 {
			t.Fatalf("expected gateway, analytics, and registry endpoint env overrides, got %d (%v)", len(got), got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_PROXY_IMAGE", "example.com/mcp-gateway:setup")
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", "http://custom-analytics-ingest")
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("preserves existing gateway otel endpoint when configured", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{}
		got := operatorEnvOverrides("", "http://custom-collector.mcp-observability.svc.cluster.local:4318")
		if len(got) != 3 {
			t.Fatalf("expected gateway otel, default analytics ingest, and registry endpoint env only, got %v", got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", "http://custom-collector.mcp-observability.svc.cluster.local:4318")
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("prefers explicit gateway otel endpoint over existing operator value", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{GatewayOTLPEndpoint: "https://otel.example.com/v1/traces"}
		got := operatorEnvOverrides("", "http://custom-collector.mcp-observability.svc.cluster.local:4318")
		if len(got) != 3 {
			t.Fatalf("expected gateway otel, default analytics ingest, and registry endpoint env only, got %v", got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", "https://otel.example.com/v1/traces")
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("uses analytics ingest override when configured", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{AnalyticsIngestURL: "http://custom-analytics-ingest"}
		got := operatorEnvOverrides("", "")
		if len(got) != 3 {
			t.Fatalf("expected gateway otel, analytics ingest, and registry endpoint env only, got %d (%v)", len(got), got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", "http://custom-analytics-ingest")
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("includes ingress readiness mode when configured", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{IngressReadinessMode: "permissive"}
		got := operatorEnvOverrides("", "")
		if len(got) != 4 {
			t.Fatalf("expected analytics, ingress readiness, and registry endpoint env overrides, got %v", got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVar(t, got, "MCP_INGRESS_READINESS_MODE", "permissive")
		requireOperatorEnvVarNonEmpty(t, got, "MCP_REGISTRY_ENDPOINT")
	})

	t.Run("includes registry endpoint and ingress host when configured", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{
			RegistryEndpoint:    "10.43.39.164:5000",
			RegistryIngressHost: "registry.local",
		}
		got := operatorEnvOverrides("", "")
		if len(got) != 4 {
			t.Fatalf("expected analytics plus registry env overrides, got %v", got)
		}
		requireOperatorEnvVar(t, got, "MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", defaultGatewayOTELExporterOTLPEndpoint)
		requireOperatorEnvVar(t, got, "MCP_SENTINEL_INGEST_URL", defaultAnalyticsIngestURL)
		requireOperatorEnvVar(t, got, "MCP_REGISTRY_ENDPOINT", "10.43.39.164:5000")
		requireOperatorEnvVar(t, got, "MCP_REGISTRY_INGRESS_HOST", "registry.local")
	})

	t.Run("uses websecure ingress defaults when mcp host has tls issuer", func(t *testing.T) {
		core.DefaultCLIConfig = &core.CLIConfig{
			McpIngressHost:            "mcp.mcpruntime.org",
			RegistryClusterIssuerName: "letsencrypt-prod",
		}
		got := operatorEnvOverrides("", "")
		requireOperatorEnvVar(t, got, "MCP_DEFAULT_INGRESS_HOST", "mcp.mcpruntime.org")
		requireOperatorEnvVar(t, got, "MCP_DEFAULT_INGRESS_ENTRYPOINTS", "websecure")
		requireOperatorEnvVar(t, got, "MCP_DEFAULT_INGRESS_TLS", "true")
	})

	t.Run("passes through platform ingress TLS namespace", func(t *testing.T) {
		t.Setenv("MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", "mcp-servers")
		core.DefaultCLIConfig = &core.CLIConfig{}
		got := operatorEnvOverrides("", "")
		requireOperatorEnvVar(t, got, "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", "mcp-servers")
	})
}

func TestConfigureProvisionedRegistryEnv(t *testing.T) {
	t.Setenv("MCP_PLATFORM_MODE", setupplan.PlatformModeTenant)
	t.Run("returns nil when registry not set", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)

		if err := configureProvisionedRegistryEnvWithKubectl(kubectl, nil, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no kubectl calls, got %v", mock.Commands)
		}
	})

	t.Run("sets URL only when no credentials", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		ext := &config.ExternalRegistryConfig{URL: "registry.example.com"}

		if err := configureProvisionedRegistryEnvWithKubectl(kubectl, ext, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 1 {
			t.Fatalf("expected 1 kubectl call, got %d", len(mock.Commands))
		}
		cmd := mock.LastCommand()
		if !contains(cmd.Args, "set") || !contains(cmd.Args, "env") || !contains(cmd.Args, "deployment/mcp-runtime-operator-controller-manager") {
			t.Fatalf("unexpected args: %v", cmd.Args)
		}
		if !contains(cmd.Args, "PROVISIONED_REGISTRY_URL=registry.example.com") {
			t.Fatalf("expected URL env in args: %v", cmd.Args)
		}
		if contains(cmd.Args, "PROVISIONED_REGISTRY_SECRET_NAME="+defaultRegistrySecretName) {
			t.Fatalf("did not expect secret name when no creds: %v", cmd.Args)
		}
	})

	t.Run("creates operator secret and sets secret env when credentials provided in tenant mode", func(t *testing.T) {
		var envData string
		var applyInputs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "create") && contains(spec.Args, "secret") {
					cmd.RunFunc = func() error {
						if cmd.StdinR != nil {
							data, _ := io.ReadAll(cmd.StdinR)
							envData = string(data)
						}
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("apiVersion: v1\nkind: Secret\n"))
						}
						return nil
					}
				}
				if contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-") {
					cmd.RunFunc = func() error {
						if cmd.StdinR != nil {
							data, _ := io.ReadAll(cmd.StdinR)
							applyInputs = append(applyInputs, string(data))
						}
						return nil
					}
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		ext := &config.ExternalRegistryConfig{
			URL:      "registry.example.com",
			Username: "user",
			Password: "pass",
		}

		if err := configureProvisionedRegistryEnvWithKubectl(kubectl, ext, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 3 {
			t.Fatalf("expected 3 kubectl calls, got %d", len(mock.Commands))
		}
		if !strings.Contains(envData, "PROVISIONED_REGISTRY_USERNAME=user") || !strings.Contains(envData, "PROVISIONED_REGISTRY_PASSWORD=pass") {
			t.Fatalf("unexpected env data: %q", envData)
		}
		foundDockerConfig := false
		for _, input := range applyInputs {
			if strings.Contains(input, "kubernetes.io/dockerconfigjson") {
				foundDockerConfig = true
				break
			}
		}
		if foundDockerConfig {
			t.Fatalf("did not expect dockerconfigjson secret manifest in tenant mode")
		}

		setEnv := mock.Commands[len(mock.Commands)-1]
		if !contains(setEnv.Args, "PROVISIONED_REGISTRY_SECRET_NAME="+defaultRegistrySecretName) {
			t.Fatalf("expected secret name env, got %v", setEnv.Args)
		}
		if !contains(setEnv.Args, "--from=secret/"+defaultRegistrySecretName) {
			t.Fatalf("expected from=secret arg, got %v", setEnv.Args)
		}
	})

	t.Run("creates catalog image pull secret when credentials provided in shared mode", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_MODE", setupplan.PlatformModePublic)
		var applyInputs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "create") && contains(spec.Args, "secret") {
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("apiVersion: v1\nkind: Secret\n"))
						}
						return nil
					}
				}
				if contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-") {
					cmd.RunFunc = func() error {
						if cmd.StdinR != nil {
							data, _ := io.ReadAll(cmd.StdinR)
							applyInputs = append(applyInputs, string(data))
						}
						return nil
					}
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		ext := &config.ExternalRegistryConfig{
			URL:      "registry.example.com",
			Username: "user",
			Password: "pass",
		}

		if err := configureProvisionedRegistryEnvWithKubectl(kubectl, ext, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 5 {
			t.Fatalf("expected 5 kubectl calls, got %d", len(mock.Commands))
		}
		foundDockerConfig := false
		var namespaceLabels map[string]string
		for _, input := range applyInputs {
			if labels, ok := namespaceLabelsFromManifest(t, input, setupplan.DefaultPublicCatalogNamespace); ok {
				namespaceLabels = labels
			}
			if strings.Contains(input, "kubernetes.io/dockerconfigjson") && strings.Contains(input, "namespace: mcp-servers-public") {
				foundDockerConfig = true
			}
		}
		if namespaceLabels == nil {
			t.Fatalf("expected catalog namespace manifest in apply inputs")
		}
		wantLabels := map[string]string{
			"platform.mcpruntime.org/managed":    "true",
			"mcpruntime.org/scope":               setupplan.PlatformModePublic,
			"pod-security.kubernetes.io/enforce": "restricted",
			"pod-security.kubernetes.io/audit":   "restricted",
			"pod-security.kubernetes.io/warn":    "restricted",
			core.LabelManagedBy:                  core.LabelManagedByValue,
		}
		for key, want := range wantLabels {
			if got := namespaceLabels[key]; got != want {
				t.Fatalf("catalog namespace label %s = %q, want %q", key, got, want)
			}
		}
		if !foundDockerConfig {
			t.Fatalf("expected dockerconfigjson secret manifest in public catalog apply inputs")
		}
	})
}

func TestEnsureProvisionedRegistrySecret(t *testing.T) {
	t.Run("returns nil when no credentials", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)

		if err := ensureProvisionedRegistrySecretWithKubectl(kubectl, "name", "", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no kubectl calls, got %v", mock.Commands)
		}
	})

	t.Run("creates and applies secret with env data", func(t *testing.T) {
		var envData string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "create") && contains(spec.Args, "secret") {
					cmd.RunFunc = func() error {
						if cmd.StdinR != nil {
							data, _ := io.ReadAll(cmd.StdinR)
							envData = string(data)
						}
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("apiVersion: v1\nkind: Secret\n"))
						}
						return nil
					}
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)

		if err := ensureProvisionedRegistrySecretWithKubectl(kubectl, "custom-secret", "user", "pass"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 2 {
			t.Fatalf("expected 2 kubectl calls, got %d", len(mock.Commands))
		}
		if !strings.Contains(envData, "PROVISIONED_REGISTRY_USERNAME=user") || !strings.Contains(envData, "PROVISIONED_REGISTRY_PASSWORD=pass") {
			t.Fatalf("unexpected env data: %q", envData)
		}
	})
}

func TestEnsureImagePullSecret(t *testing.T) {
	t.Run("returns nil when no credentials", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)

		if err := ensureImagePullSecretWithKubectl(kubectl, "ns", "name", "registry.example.com", "", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no kubectl calls, got %v", mock.Commands)
		}
	})

	t.Run("applies dockerconfigjson secret manifest", func(t *testing.T) {
		var manifest string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-") {
					cmd.RunFunc = func() error {
						if cmd.StdinR != nil {
							data, _ := io.ReadAll(cmd.StdinR)
							manifest = string(data)
						}
						return nil
					}
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)

		if err := ensureImagePullSecretWithKubectl(kubectl, "ns", "name", "registry.example.com", "user", "pass"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(manifest, "kubernetes.io/dockerconfigjson") || !strings.Contains(manifest, ".dockerconfigjson:") {
			t.Fatalf("unexpected secret manifest: %q", manifest)
		}

		var encoded string
		for _, line := range strings.Split(manifest, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, ".dockerconfigjson:") {
				encoded = strings.TrimSpace(strings.TrimPrefix(line, ".dockerconfigjson:"))
				break
			}
		}
		if encoded == "" {
			t.Fatalf("missing dockerconfigjson payload")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("failed to decode dockerconfigjson: %v", err)
		}
		if !strings.Contains(string(decoded), "registry.example.com") {
			t.Fatalf("decoded docker config missing registry: %s", string(decoded))
		}
	})
}

func TestEnsureAnalyticsImagePullSecret(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})

	core.DefaultCLIConfig = &core.CLIConfig{
		ProvisionedRegistryURL:      "registry.example.com",
		ProvisionedRegistryUsername: "user",
		ProvisionedRegistryPassword: "pass",
	}

	var manifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-") {
				cmd.RunFunc = func() error {
					if cmd.StdinR != nil {
						data, _ := io.ReadAll(cmd.StdinR)
						manifest = string(data)
					}
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	secretName, err := ensureAnalyticsImagePullSecret(kubectl, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secretName != defaultRegistrySecretName {
		t.Fatalf("expected secret name %q, got %q", defaultRegistrySecretName, secretName)
	}
	if !strings.Contains(manifest, "namespace: "+core.DefaultAnalyticsNamespace) {
		t.Fatalf("expected analytics namespace in secret manifest, got %q", manifest)
	}
	if !strings.Contains(manifest, "kubernetes.io/dockerconfigjson") {
		t.Fatalf("expected dockerconfigjson secret manifest, got %q", manifest)
	}
}

func TestEnsureAnalyticsImagePullSecretForBundledPublicRegistry(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.mcpruntime.org")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "registry.mcpruntime.org",
		RegistryIngressHost: "registry.mcpruntime.org",
	}

	var manifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "get") && contains(spec.Args, "secret") && contains(spec.Args, "mcp-sentinel-secrets") {
				cmd.OutputData = []byte(base64.StdEncoding.EncodeToString([]byte("admin-key")))
				return cmd
			}
			if contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-") {
				cmd.RunFunc = func() error {
					if cmd.StdinR != nil {
						data, _ := io.ReadAll(cmd.StdinR)
						manifest = string(data)
					}
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	secretName, err := ensureAnalyticsImagePullSecret(kubectl, AnalyticsImageSet{
		PlatformAPI: "registry.mcpruntime.org/mcp-platform-api:dev",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secretName != defaultRegistrySecretName {
		t.Fatalf("expected secret name %q, got %q", defaultRegistrySecretName, secretName)
	}
	if !strings.Contains(manifest, "namespace: "+core.DefaultAnalyticsNamespace) {
		t.Fatalf("expected analytics namespace in secret manifest, got %q", manifest)
	}
	if !strings.Contains(manifest, "kubernetes.io/dockerconfigjson") {
		t.Fatalf("expected public registry dockerconfigjson secret, got %q", manifest)
	}
	encoded := ""
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ".dockerconfigjson:") {
			encoded = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ".dockerconfigjson:"))
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("failed to decode dockerconfigjson: %v", err)
	}
	if !strings.Contains(string(decoded), "registry.mcpruntime.org") || !strings.Contains(string(decoded), "platform-service") {
		t.Fatalf("unexpected dockerconfigjson payload: %s", string(decoded))
	}
}

func TestBundledPublicRegistryPullSecretHostSkipsClusterDNS(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	t.Setenv("MCP_REGISTRY_ENDPOINT", "registry.registry.svc.cluster.local:5000")
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "registry.registry.svc.cluster.local:5000",
		RegistryIngressHost: "registry.local",
	}

	got := bundledPublicRegistryPullSecretHost([]string{"registry.registry.svc.cluster.local:5000/mcp-platform-api:dev"})
	if got != "" {
		t.Fatalf("expected no public pull secret host for cluster DNS, got %q", got)
	}
}

func TestRenderAnalyticsSecretManifestReusesExistingPassword(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("keep-me"))
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(encoded)}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["GRAFANA_ADMIN_PASSWORD"] != "keep-me" {
		t.Fatalf("expected existing grafana password to be reused, got %q", data["GRAFANA_ADMIN_PASSWORD"])
	}
}

func TestRenderAnalyticsSecretManifestReusesExistingAPIKeys(t *testing.T) {
	apiKeyEncoded := base64.StdEncoding.EncodeToString([]byte("api-key"))
	ingestKeyEncoded := base64.StdEncoding.EncodeToString([]byte("ingest-key"))
	adminKeyEncoded := base64.StdEncoding.EncodeToString([]byte("admin-key"))
	uiKeyEncoded := base64.StdEncoding.EncodeToString([]byte("ui-key"))
	passwordEncoded := base64.StdEncoding.EncodeToString([]byte("grafana-password"))
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.data.API_KEYS}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(apiKeyEncoded)}
			case contains(spec.Args, "jsonpath={.data.INGEST_API_KEYS}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(ingestKeyEncoded)}
			case contains(spec.Args, "jsonpath={.data.ADMIN_API_KEYS}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(adminKeyEncoded)}
			case contains(spec.Args, "jsonpath={.data.UI_API_KEY}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(uiKeyEncoded)}
			case contains(spec.Args, "jsonpath={.data.GRAFANA_ADMIN_PASSWORD}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(passwordEncoded)}
			default:
				return &core.MockCommand{Args: spec.Args}
			}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["API_KEYS"] != "api-key,ui-key" {
		t.Fatalf("expected existing API key list to include UI key, got %q", data["API_KEYS"])
	}
	if data["INGEST_API_KEYS"] != "ingest-key" {
		t.Fatalf("expected existing ingest API key list to be reused, got %q", data["INGEST_API_KEYS"])
	}
	if data["ADMIN_API_KEYS"] != "admin-key,ui-key" {
		t.Fatalf("expected existing admin API key list to include UI key, got %q", data["ADMIN_API_KEYS"])
	}
	if data["UI_API_KEY"] != "ui-key" {
		t.Fatalf("expected existing UI API key to be reused, got %q", data["UI_API_KEY"])
	}
	if data["POSTGRES_USER"] != "mcp_runtime" {
		t.Fatalf("expected default postgres user to be rendered, got %q", data["POSTGRES_USER"])
	}
	if data["POSTGRES_DB"] != "mcp_runtime" {
		t.Fatalf("expected default postgres db to be rendered, got %q", data["POSTGRES_DB"])
	}
	if !strings.HasPrefix(data["POSTGRES_DSN"], "postgres://mcp_runtime:") {
		t.Fatalf("expected derived postgres DSN to be rendered, got %q", data["POSTGRES_DSN"])
	}
	if data["GRAFANA_ADMIN_PASSWORD"] != "grafana-password" {
		t.Fatalf("expected existing grafana password to be reused, got %q", data["GRAFANA_ADMIN_PASSWORD"])
	}
}

func TestRenderAnalyticsSecretManifestUsesAdminEnv(t *testing.T) {
	t.Setenv("MCP_PLATFORM_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("MCP_PLATFORM_ADMIN_PASSWORD", "bootstrap-password")
	t.Setenv("MCP_ADMIN_USERS", "ops@example.com, google-sub-123")
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{})

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["PLATFORM_ADMIN_EMAIL"] != "admin@example.com" {
		t.Fatalf("expected platform admin email from env, got %q", data["PLATFORM_ADMIN_EMAIL"])
	}
	if data["PLATFORM_ADMIN_PASSWORD"] != "bootstrap-password" {
		t.Fatalf("expected platform admin password from env, got %q", data["PLATFORM_ADMIN_PASSWORD"])
	}
	for _, want := range []string{"ops@example.com", "google-sub-123", "admin@example.com"} {
		if !csvHasValue(data["ADMIN_USERS"], want) {
			t.Fatalf("expected ADMIN_USERS to include %q, got %q", want, data["ADMIN_USERS"])
		}
	}
}

func TestRenderAnalyticsSecretManifestDoesNotSeedPartialAdminPasswordUser(t *testing.T) {
	t.Setenv("MCP_PLATFORM_ADMIN_EMAIL", "admin@example.com")
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{})

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["PLATFORM_ADMIN_EMAIL"] != "" || data["PLATFORM_ADMIN_PASSWORD"] != "" {
		t.Fatalf("expected partial admin bootstrap fields to be omitted, got email=%q password=%q", data["PLATFORM_ADMIN_EMAIL"], data["PLATFORM_ADMIN_PASSWORD"])
	}
	if !csvHasValue(data["ADMIN_USERS"], "admin@example.com") {
		t.Fatalf("expected admin email to remain in ADMIN_USERS, got %q", data["ADMIN_USERS"])
	}
	for _, part := range strings.Split(data["ADMIN_USERS"], ",") {
		if strings.TrimSpace(part) == "" {
			t.Fatalf("expected ADMIN_USERS to avoid empty entries, got %q", data["ADMIN_USERS"])
		}
	}
}

func TestRenderAnalyticsSecretManifestAllowsPartialAdminOverrideWithExistingPair(t *testing.T) {
	t.Setenv("MCP_PLATFORM_ADMIN_EMAIL", "new-admin@mcpruntime.org")
	existingPassword := base64.StdEncoding.EncodeToString([]byte("existing-password"))
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "jsonpath={.data.PLATFORM_ADMIN_PASSWORD}") {
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(existingPassword)}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["PLATFORM_ADMIN_EMAIL"] != "new-admin@mcpruntime.org" {
		t.Fatalf("expected admin email override, got %q", data["PLATFORM_ADMIN_EMAIL"])
	}
	if data["PLATFORM_ADMIN_PASSWORD"] != "existing-password" {
		t.Fatalf("expected existing password to be retained, got %q", data["PLATFORM_ADMIN_PASSWORD"])
	}
	if !csvHasValue(data["ADMIN_USERS"], "new-admin@mcpruntime.org") {
		t.Fatalf("expected ADMIN_USERS to include override email, got %q", data["ADMIN_USERS"])
	}
}

func TestRenderAnalyticsSecretManifestEscapesPostgresCredentialsInDSN(t *testing.T) {
	postgresUserEncoded := base64.StdEncoding.EncodeToString([]byte("user@runtime"))
	postgresPasswordEncoded := base64.StdEncoding.EncodeToString([]byte(`pa:ss?/#[%]`))
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.data.POSTGRES_USER}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(postgresUserEncoded)}
			case contains(spec.Args, "jsonpath={.data.POSTGRES_PASSWORD}"):
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(postgresPasswordEncoded)}
			default:
				return &core.MockCommand{Args: spec.Args}
			}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)

	encodedUserInfo := url.UserPassword("user@runtime", `pa:ss?/#[%]`).String()
	want := "postgres://" + encodedUserInfo + "@mcp-sentinel-postgres.mcp-sentinel.svc.cluster.local:5432/mcp_runtime?sslmode=disable"
	if data["POSTGRES_DSN"] != want {
		t.Fatalf("expected encoded postgres DSN %q, got %q", want, data["POSTGRES_DSN"])
	}
}

func TestRenderAnalyticsSecretManifestGeneratesKeysWhenMissing(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found"),
					OutputErr:  errors.New("not found"),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["API_KEYS"] == "" {
		t.Fatalf("expected generated API key, got %q", manifest)
	}
	if data["UI_API_KEY"] == "" {
		t.Fatalf("expected generated UI API key, got %q", manifest)
	}
	if data["INGEST_API_KEYS"] == "" {
		t.Fatalf("expected generated ingest API key, got %q", manifest)
	}
	if data["ADMIN_API_KEYS"] == "" {
		t.Fatalf("expected admin API key list, got %q", manifest)
	}
	if !csvHasValue(data["API_KEYS"], data["UI_API_KEY"]) {
		t.Fatalf("expected UI_API_KEY to be included in API_KEYS, got API_KEYS=%q UI_API_KEY=%q", data["API_KEYS"], data["UI_API_KEY"])
	}
	if !csvHasValue(data["ADMIN_API_KEYS"], data["UI_API_KEY"]) {
		t.Fatalf("expected UI_API_KEY to be included in ADMIN_API_KEYS, got ADMIN_API_KEYS=%q UI_API_KEY=%q", data["ADMIN_API_KEYS"], data["UI_API_KEY"])
	}
	if data["GRAFANA_ADMIN_PASSWORD"] == "" {
		t.Fatalf("expected generated grafana password, got %q", manifest)
	}
	if data["POSTGRES_PASSWORD"] == "" {
		t.Fatalf("expected generated postgres password, got %q", manifest)
	}
	if data["POSTGRES_DSN"] == "" {
		t.Fatalf("expected generated postgres DSN, got %q", manifest)
	}
	if data["JWT_SECRET"] == "" {
		t.Fatalf("expected generated jwt secret, got %q", manifest)
	}
	if data["INTERNAL_AUTH_TOKEN"] == "" {
		t.Fatalf("expected generated internal auth token, got %q", manifest)
	}
}

func TestRenderAnalyticsSecretManifestSeedsDevLoginsInTestMode(t *testing.T) {
	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found"),
					OutputErr:  errors.New("not found"),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["PLATFORM_DEV_LOGIN_ENABLED"] != "true" {
		t.Fatalf("expected dev login seed to be enabled, got %q", data["PLATFORM_DEV_LOGIN_ENABLED"])
	}
	if data["PLATFORM_DEV_USER_EMAIL"] != defaultDevUserEmail || data["PLATFORM_DEV_USER_PASSWORD"] != defaultDevUserPassword {
		t.Fatalf("unexpected dev user credentials: email=%q password=%q", data["PLATFORM_DEV_USER_EMAIL"], data["PLATFORM_DEV_USER_PASSWORD"])
	}
	if data["PLATFORM_DEV_ADMIN_EMAIL"] != defaultDevAdminEmail || data["PLATFORM_DEV_ADMIN_PASSWORD"] != defaultDevAdminPassword {
		t.Fatalf("unexpected dev admin credentials: email=%q password=%q", data["PLATFORM_DEV_ADMIN_EMAIL"], data["PLATFORM_DEV_ADMIN_PASSWORD"])
	}
}

func TestRenderAnalyticsSecretManifestDisablesExistingDevLoginsOutsideTestMode(t *testing.T) {
	encoded := func(value string) []byte {
		return []byte(base64.StdEncoding.EncodeToString([]byte(value)))
	}
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.data.PLATFORM_DEV_LOGIN_ENABLED}"):
				return &core.MockCommand{Args: spec.Args, OutputData: encoded("true")}
			case contains(spec.Args, "jsonpath={.data.PLATFORM_DEV_USER_EMAIL}"):
				return &core.MockCommand{Args: spec.Args, OutputData: encoded(defaultDevUserEmail)}
			case contains(spec.Args, "jsonpath={.data.PLATFORM_DEV_USER_PASSWORD}"):
				return &core.MockCommand{Args: spec.Args, OutputData: encoded(defaultDevUserPassword)}
			case contains(spec.Args, "jsonpath={.data.PLATFORM_DEV_ADMIN_EMAIL}"):
				return &core.MockCommand{Args: spec.Args, OutputData: encoded(defaultDevAdminEmail)}
			case contains(spec.Args, "jsonpath={.data.PLATFORM_DEV_ADMIN_PASSWORD}"):
				return &core.MockCommand{Args: spec.Args, OutputData: encoded(defaultDevAdminPassword)}
			default:
				return &core.MockCommand{Args: spec.Args}
			}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	manifest, err := renderAnalyticsSecretManifest(kubectl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := secretStringDataFromManifest(t, manifest)
	if data["PLATFORM_DEV_LOGIN_ENABLED"] != "false" {
		t.Fatalf("expected dev login seed to be disabled outside test mode, got %q", data["PLATFORM_DEV_LOGIN_ENABLED"])
	}
	for _, key := range []string{
		"PLATFORM_DEV_USER_EMAIL",
		"PLATFORM_DEV_USER_PASSWORD",
		"PLATFORM_DEV_ADMIN_EMAIL",
		"PLATFORM_DEV_ADMIN_PASSWORD",
	} {
		if data[key] != "" {
			t.Fatalf("expected %s to be cleared outside test mode, got %q", key, data[key])
		}
	}
}

func TestRenderAnalyticsConfigManifestInjectsMTLSClusterIssuer(t *testing.T) {
	t.Setenv("MCP_MTLS_CLUSTER_ISSUER", "mcp-runtime-ca")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  MCP_MTLS_CLUSTER_ISSUER: ""
  PLATFORM_MODE: "tenant"
`, setupplan.PlatformModeTenant, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["MCP_MTLS_CLUSTER_ISSUER"]; got != "mcp-runtime-ca" {
		t.Fatalf("expected MCP_MTLS_CLUSTER_ISSUER to be injected into the sentinel ConfigMap, got %q", got)
	}
}

func TestRenderAnalyticsConfigManifestPreservesExistingPublicOAuthConfig(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "configmap", "mcp-sentinel-config", "-n", "mcp-sentinel", "-o", "json") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte(`{"data":{"GOOGLE_CLIENT_ID":"client.apps.googleusercontent.com","OIDC_ISSUER":"https://accounts.google.com","OIDC_AUDIENCE":"client.apps.googleusercontent.com","OIDC_JWKS_URL":"https://www.googleapis.com/oauth2/v3/certs","PLATFORM_MODE":"public"}}`),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  GOOGLE_CLIENT_ID: ""
  OIDC_ISSUER: ""
  OIDC_AUDIENCE: ""
  OIDC_JWKS_URL: ""
  PLATFORM_MODE: "tenant"
`, setupplan.PlatformModeTenant, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		APIVersion string            `yaml:"apiVersion"`
		Kind       string            `yaml:"kind"`
		Metadata   map[string]any    `yaml:"metadata"`
		Data       map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if payload.APIVersion != "v1" || payload.Kind != "ConfigMap" {
		t.Fatalf("expected manifest envelope to be preserved, got apiVersion=%q kind=%q", payload.APIVersion, payload.Kind)
	}
	if got := payload.Metadata["name"]; got != "mcp-sentinel-config" {
		t.Fatalf("expected metadata.name to be preserved, got %#v", got)
	}
	if got := payload.Data["GOOGLE_CLIENT_ID"]; got != "client.apps.googleusercontent.com" {
		t.Fatalf("expected GOOGLE_CLIENT_ID to be preserved, got %q", got)
	}
	if got := payload.Data["PLATFORM_MODE"]; got != setupplan.PlatformModePublic {
		t.Fatalf("expected PLATFORM_MODE to stay public, got %q", got)
	}
}

func TestRenderAnalyticsConfigManifestUsesGoogleEnvOnCleanInstall(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "env-client.apps.googleusercontent.com")
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{})

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  GOOGLE_CLIENT_ID: ""
  OIDC_ISSUER: ""
  OIDC_AUDIENCE: ""
  OIDC_JWKS_URL: ""
  PLATFORM_MODE: "public"
`, setupplan.PlatformModePublic, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["GOOGLE_CLIENT_ID"]; got != "env-client.apps.googleusercontent.com" {
		t.Fatalf("expected GOOGLE_CLIENT_ID from env, got %q", got)
	}
	if got := payload.Data["OIDC_ISSUER"]; got != "https://accounts.google.com" {
		t.Fatalf("expected Google issuer default, got %q", got)
	}
	if got := payload.Data["OIDC_AUDIENCE"]; got != "env-client.apps.googleusercontent.com" {
		t.Fatalf("expected OIDC_AUDIENCE to default to Google client ID, got %q", got)
	}
	if got := payload.Data["OIDC_JWKS_URL"]; got != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Fatalf("expected Google JWKS default, got %q", got)
	}
}

func TestRenderAnalyticsConfigManifestDetectsK3sTraefikNamespace(t *testing.T) {
	swapKubernetesClientsForTest(t, platformTestClientsWithTraefikDeployment("kube-system"))
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "deployment", "-A", "--no-headers") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte("kube-system traefik\n"),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  PLATFORM_TRAEFIK_NAMESPACE: ""
`, setupplan.PlatformModePublic, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["PLATFORM_TRAEFIK_NAMESPACE"]; got != "kube-system" {
		t.Fatalf("PLATFORM_TRAEFIK_NAMESPACE = %q, want kube-system", got)
	}
	if got := payload.Data["PLATFORM_TEAM_TRAEFIK_WATCH"]; got != "disabled" {
		t.Fatalf("PLATFORM_TEAM_TRAEFIK_WATCH = %q, want disabled", got)
	}
}

func TestRenderAnalyticsConfigManifestPreservesExplicitTeamTraefikWatch(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  PLATFORM_TRAEFIK_NAMESPACE: kube-system
  PLATFORM_TEAM_TRAEFIK_WATCH: required
`, setupplan.PlatformModeTenant, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["PLATFORM_TEAM_TRAEFIK_WATCH"]; got != "required" {
		t.Fatalf("PLATFORM_TEAM_TRAEFIK_WATCH = %q, want required", got)
	}
}

func TestRenderAnalyticsConfigManifestAppliesExplicitPlatformMode(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "configmap", "mcp-sentinel-config", "-n", "mcp-sentinel", "-o", "json") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte(`{"data":{"PLATFORM_MODE":"public"}}`),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  PLATFORM_MODE: "tenant"
`, setupplan.PlatformModeOrg, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["PLATFORM_MODE"]; got != setupplan.PlatformModeOrg {
		t.Fatalf("expected explicit platform mode to win, got %q", got)
	}
}

func TestRenderAnalyticsConfigManifestSetsRegistryResolutionEnv(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "10.96.223.152:5000",
		RegistryIngressHost: "registry.mcpruntime.org",
	}
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "configmap", "mcp-sentinel-config", "-n", "mcp-sentinel", "-o", "json") {
				return &core.MockCommand{Args: spec.Args, OutputData: []byte(`{"data":{}}`)}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  PLATFORM_MODE: "tenant"
  PLATFORM_REGISTRY_URL: ""
  MCP_REGISTRY_ENDPOINT: ""
  MCP_REGISTRY_INGRESS_HOST: ""
`, setupplan.PlatformModePublic, AnalyticsImageSet{PlatformAPI: "10.96.223.152:5000/mcp-platform-api:a1f967c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["MCP_REGISTRY_ENDPOINT"]; got != "registry.mcpruntime.org" {
		t.Fatalf("MCP_REGISTRY_ENDPOINT = %q, want public registry ingress host", got)
	}
	if got := payload.Data["MCP_REGISTRY_INGRESS_HOST"]; got != "registry.mcpruntime.org" {
		t.Fatalf("MCP_REGISTRY_INGRESS_HOST = %q, want public host", got)
	}
	if got := payload.Data["PLATFORM_REGISTRY_URL"]; got != "registry.mcpruntime.org" {
		t.Fatalf("PLATFORM_REGISTRY_URL = %q, want public registry host", got)
	}
}

func TestRenderAnalyticsConfigManifestHandlesMissingConfigMap(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if commandHasArgs(spec, "get", "configmap", "mcp-sentinel-config", "-n", "mcp-sentinel", "-o", "json") {
				return &core.MockCommand{
					Args:       spec.Args,
					OutputData: []byte("Error from server (NotFound): configmaps \"mcp-sentinel-config\" not found"),
					OutputErr:  errors.New("exit status 1"),
				}
			}
			return &core.MockCommand{Args: spec.Args}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
  namespace: mcp-sentinel
data:
  PLATFORM_MODE: "tenant"
`, setupplan.PlatformModeTenant, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	if got := payload.Data["PLATFORM_MODE"]; got != setupplan.PlatformModeTenant {
		t.Fatalf("expected tenant mode on fresh install, got %q", got)
	}
}

func TestRenderAnalyticsConfigManifestHandlesEmptyConfigMapOutput(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "configmap", "mcp-sentinel-config", "-n", core.DefaultAnalyticsNamespace, "-o", "json") {
				cmd.OutputData = []byte("")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	rendered, err := renderAnalyticsConfigManifest(kubectl, `apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-sentinel-config
data:
  PLATFORM_MODE: "tenant"
`, setupplan.PlatformModeTenant, AnalyticsImageSet{})
	if err != nil {
		t.Fatalf("renderAnalyticsConfigManifest returned error: %v", err)
	}
	if !strings.Contains(rendered, `PLATFORM_MODE: tenant`) {
		t.Fatalf("expected rendered config to preserve platform mode, got %q", rendered)
	}
}

func TestEnsureCSVIncludes(t *testing.T) {
	tests := []struct {
		name  string
		csv   string
		value string
		want  string
	}{
		{name: "appends missing value", csv: "api-key", value: "ui-key", want: "api-key,ui-key"},
		{name: "preserves existing value", csv: "api-key, ui-key", value: "ui-key", want: "api-key,ui-key"},
		{name: "uses value when csv empty", csv: "", value: "ui-key", want: "ui-key"},
		{name: "trims empty value", csv: "api-key", value: " ", want: "api-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureCSVIncludes(tt.csv, tt.value); got != tt.want {
				t.Fatalf("ensureCSVIncludes(%q, %q) = %q, want %q", tt.csv, tt.value, got, tt.want)
			}
		})
	}
}

func TestPrepareAnalyticsImagesUsesTestModeImageSet(t *testing.T) {
	// setupImageTag() reads MCP_RUNTIME_TEST_MODE, not the boolean testMode argument,
	// to decide between the "latest" tag and a git SHA. Opt into the test-mode tag
	// here so the expected ":latest" image refs line up.
	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")

	var buildCalls int32
	var pushCalls int32
	var buildContexts []string
	deps := SetupDeps{
		BuildAnalyticsImage: func(_, _, buildContext string) error {
			atomic.AddInt32(&buildCalls, 1)
			buildContexts = append(buildContexts, buildContext)
			return nil
		},
		PushAnalyticsImage: func(string) error {
			atomic.AddInt32(&pushCalls, 1)
			return nil
		},
	}

	got, err := prepareAnalyticsImages(zap.NewNop(), &config.ExternalRegistryConfig{URL: "registry.example.com"}, true, true, false, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := AnalyticsImageSet{
		Ingest:       "registry.example.com/mcp-sentinel-ingest:latest",
		PlatformAPI:  "registry.example.com/mcp-platform-api:latest",
		RuntimeAPI:   "registry.example.com/mcp-runtime-api:latest",
		AnalyticsAPI: "registry.example.com/mcp-analytics-api:latest",
		Processor:    "registry.example.com/mcp-sentinel-processor:latest",
		UI:           "registry.example.com/mcp-sentinel-ui:latest",
		DoctorSmoke:  "registry.example.com/mcp-runtime-doctor-smoke:latest",
	}
	if got != want {
		t.Fatalf("prepareAnalyticsImages() = %+v, want %+v", got, want)
	}
	if atomic.LoadInt32(&buildCalls) != int32(len(analyticsComponentsForSetup(true))) {
		t.Fatalf("expected %d builds in test mode, got %d", len(analyticsComponentsForSetup(true)), buildCalls)
	}
	// Sentinel service Dockerfiles need the repo root context for shared packages and service modules.
	wantBuildContexts := []string{".", ".", ".", ".", ".", ".", "."}
	if !slices.Equal(buildContexts, wantBuildContexts) {
		t.Fatalf("build contexts = %v, want %v", buildContexts, wantBuildContexts)
	}
	if atomic.LoadInt32(&pushCalls) != int32(len(analyticsComponentsForSetup(true))) {
		t.Fatalf("expected %d pushes in test mode, got %d", len(analyticsComponentsForSetup(true)), pushCalls)
	}
}

func TestPrepareDeploymentImagesParallelBuildsStartsBothBuilds(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	errCh := make(chan error, 1)

	deps := SetupDeps{
		OperatorImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-sentinel-mcp-gateway:latest"
		},
		BuildOperatorImage: func(string) error {
			started <- "operator"
			<-release
			return nil
		},
		PushOperatorImage: func(string) error { return nil },
		BuildGatewayProxyImage: func(string) error {
			started <- "gateway"
			<-release
			return nil
		},
		PushGatewayProxyImage: func(string) error { return nil },
	}

	go func() {
		_, _, err := prepareDeploymentImages(zap.NewNop(), &config.ExternalRegistryConfig{URL: "registry.example.com"}, true, true, true, deps)
		errCh <- err
	}()

	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-timeout:
			t.Fatalf("timed out waiting for parallel runtime image builds, saw %v", seen)
		}
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareDeploymentImagesParallelBuildsPreparesInternalRegistryOnce(t *testing.T) {
	var ensureCalls int32
	var resolveCalls int32

	deps := SetupDeps{
		OperatorImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-runtime-operator:latest"
		},
		GatewayProxyImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "registry.example.com/mcp-sentinel-mcp-gateway:latest"
		},
		BuildOperatorImage:     func(string) error { return nil },
		BuildGatewayProxyImage: func(string) error { return nil },
		EnsureNamespace: func(string) error {
			atomic.AddInt32(&ensureCalls, 1)
			return nil
		},
		ResolvePlatformRegistryURL: func(*zap.Logger) string {
			atomic.AddInt32(&resolveCalls, 1)
			return "registry.local:5000"
		},
		PushOperatorImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
		PushGatewayProxyImageToInternal: func(*zap.Logger, string, string, string) error {
			return nil
		},
	}

	operatorImage, gatewayProxyImage, err := prepareDeploymentImages(zap.NewNop(), &config.ExternalRegistryConfig{URL: "registry.example.com"}, false, true, true, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if operatorImage != "registry.local:5000/mcp-runtime-operator:latest" {
		t.Fatalf("operator image = %q, want internal registry image", operatorImage)
	}
	if gatewayProxyImage != "registry.local:5000/mcp-sentinel-mcp-gateway:latest" {
		t.Fatalf("gateway proxy image = %q, want internal registry image", gatewayProxyImage)
	}
	if got := atomic.LoadInt32(&ensureCalls); got != 1 {
		t.Fatalf("expected one registry namespace ensure, got %d", got)
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 1 {
		t.Fatalf("expected one registry URL resolve, got %d", got)
	}
}

func TestPrepareAnalyticsImagesParallelBuildsStartsAllBuilds(t *testing.T) {
	started := make(chan string, len(analyticsComponentsForSetup(true)))
	release := make(chan struct{})
	errCh := make(chan error, 1)

	deps := SetupDeps{
		BuildAnalyticsImage: func(image, _, _ string) error {
			started <- image
			<-release
			return nil
		},
		PushAnalyticsImage: func(string) error { return nil },
	}

	go func() {
		_, err := prepareAnalyticsImages(zap.NewNop(), &config.ExternalRegistryConfig{URL: "registry.example.com"}, true, true, true, deps)
		errCh <- err
	}()

	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(seen) < len(analyticsComponentsForSetup(true)) {
		select {
		case image := <-started:
			seen[image] = true
		case <-timeout:
			t.Fatalf("timed out waiting for parallel analytics image builds, saw %d of %d", len(seen), len(analyticsComponentsForSetup(true)))
		}
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareAnalyticsImagesParallelBuildsPreparesInternalRegistryOnce(t *testing.T) {
	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")

	var ensureCalls int32
	var resolveCalls int32
	deps := SetupDeps{
		BuildAnalyticsImage: func(string, string, string) error { return nil },
		EnsureNamespace: func(string) error {
			atomic.AddInt32(&ensureCalls, 1)
			return nil
		},
		ResolvePlatformRegistryURL: func(*zap.Logger) string {
			atomic.AddInt32(&resolveCalls, 1)
			return "registry.local:5000"
		},
		PushAnalyticsImageToInternal: func(*zap.Logger, string, string, string) error { return nil },
	}

	got, err := prepareAnalyticsImages(zap.NewNop(), &config.ExternalRegistryConfig{URL: "registry.example.com"}, false, true, true, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := AnalyticsImageSet{
		Ingest:       "registry.local:5000/mcp-sentinel-ingest:latest",
		PlatformAPI:  "registry.local:5000/mcp-platform-api:latest",
		RuntimeAPI:   "registry.local:5000/mcp-runtime-api:latest",
		AnalyticsAPI: "registry.local:5000/mcp-analytics-api:latest",
		Processor:    "registry.local:5000/mcp-sentinel-processor:latest",
		UI:           "registry.local:5000/mcp-sentinel-ui:latest",
		DoctorSmoke:  "registry.local:5000/mcp-runtime-doctor-smoke:latest",
	}
	if got != want {
		t.Fatalf("prepareAnalyticsImages() = %+v, want %+v", got, want)
	}
	if got := atomic.LoadInt32(&ensureCalls); got != 1 {
		t.Fatalf("expected one registry namespace ensure, got %d", got)
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 1 {
		t.Fatalf("expected one registry URL resolve, got %d", got)
	}
}

func TestRenderAnalyticsManifestInjectsImagePullSecrets(t *testing.T) {
	content := `# keep deployment comment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-sentinel-ingest
spec:
  template:
    spec:
      # keep containers comment
      containers:
        - name: ingest
          image: mcp-sentinel-ingest:latest
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: promtail
spec:
  template:
    spec:
      containers:
        - name: promtail
          image: grafana/promtail:2.9.4
`

	rendered, err := renderAnalyticsManifest(content, AnalyticsImageSet{Ingest: "registry.example.com/mcp-sentinel-ingest:latest"}, defaultRegistrySecretName, setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rendered, "image: registry.example.com/mcp-sentinel-ingest:latest") {
		t.Fatalf("expected image replacement, got %s", rendered)
	}
	if !strings.Contains(rendered, "imagePullSecrets:") || !strings.Contains(rendered, "name: "+defaultRegistrySecretName) {
		t.Fatalf("expected injected imagePullSecrets, got %s", rendered)
	}
	if !strings.Contains(rendered, "# keep deployment comment") || !strings.Contains(rendered, "# keep containers comment") {
		t.Fatalf("expected imagePullSecrets injection to preserve manifest comments, got %s", rendered)
	}
}

func TestRenderAnalyticsManifestUsesConfigurableKubernetesAPIPort(t *testing.T) {
	original := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = original })
	core.DefaultCLIConfig = &core.CLIConfig{KubernetesAPIPort: 9443}
	content := "egress:\n- ports:\n  - protocol: TCP\n    port: 6443 # MCP_KUBERNETES_API_PORT\n"
	rendered, err := renderAnalyticsManifest(content, AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rendered, "port: 9443 # MCP_KUBERNETES_API_PORT") {
		t.Fatalf("expected configured Kubernetes API port, got %q", rendered)
	}
}

func TestRenderAnalyticsManifestRejectsInvalidKubernetesAPIPort(t *testing.T) {
	original := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = original })
	core.DefaultCLIConfig = &core.CLIConfig{KubernetesAPIPort: 70000}
	_, err := renderAnalyticsManifest("port: 6443 # MCP_KUBERNETES_API_PORT\n", AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err == nil || !strings.Contains(err.Error(), "MCP_KUBERNETES_API_PORT") {
		t.Fatalf("expected invalid port error, got %v", err)
	}
}

func TestDeployAnalyticsManifestsWithKubectl_RecreatesInitializationJobs(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	core.DefaultCLIConfig = &core.CLIConfig{}
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients(nil, nil))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "k8s")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("failed to create manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	manifestContent := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n  namespace: mcp-sentinel\n"
	for _, name := range []string{
		"00-namespace.yaml",
		"01-config.yaml",
		"03-clickhouse.yaml",
		"04-clickhouse-init.yaml",
		"05-kafka.yaml",
		"05-kafka-topic-init.yaml",
		"06-ingest.yaml",
		"07-processor.yaml",
		"08-platform-api.yaml",
		"08-platform-api-rbac.yaml",
		"08-runtime-api.yaml",
		"08-runtime-api-rbac.yaml",
		"08-analytics-api.yaml",
		"22-split-api-networkpolicy.yaml",
		"09-ui.yaml",
		"10-gateway.yaml",
		"11-prometheus.yaml",
		"12-grafana.yaml",
		"15-otel-collector.yaml",
		"16-tempo.yaml",
		"17-loki.yaml",
		"18-promtail.yaml",
		"19-grafana-datasources.yaml",
		"21-grafana-dashboards.yaml",
		"20-postgres.yaml",
	} {
		if err := os.WriteFile(filepath.Join(manifestDir, name), []byte(manifestContent), 0o644); err != nil {
			t.Fatalf("failed to write fixture manifest %s: %v", name, err)
		}
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed to chdir to fixture root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	clickhouseDeleteIndex := -1
	clickhouseWaitIndex := -1
	kafkaDeleteIndex := -1
	kafkaWaitIndex := -1
	var mock *core.MockExecutor
	mock = &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				cmd.OutputData = []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found")
				cmd.OutputErr = errors.New("not found")
			}
			if contains(spec.Args, "delete") && contains(spec.Args, "job/clickhouse-init") {
				clickhouseDeleteIndex = len(mock.Commands) - 1
				for _, want := range []string{"--ignore-not-found=true", "--wait=true", "--timeout=60s"} {
					if !contains(spec.Args, want) {
						t.Fatalf("delete job args missing %s: %v", want, spec.Args)
					}
				}
			}
			if contains(spec.Args, "wait") && contains(spec.Args, "job/clickhouse-init") {
				clickhouseWaitIndex = len(mock.Commands) - 1
			}
			if contains(spec.Args, "delete") && contains(spec.Args, "job/kafka-topic-init") {
				kafkaDeleteIndex = len(mock.Commands) - 1
			}
			if contains(spec.Args, "wait") && contains(spec.Args, "job/kafka-topic-init") {
				kafkaWaitIndex = len(mock.Commands) - 1
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	err = deployAnalyticsManifestsWithKubectl(kubectl, zap.NewNop(), AnalyticsImageSet{
		Ingest:       "example.com/mcp-sentinel-ingest:latest",
		PlatformAPI:  "example.com/mcp-platform-api:latest",
		RuntimeAPI:   "example.com/mcp-runtime-api:latest",
		AnalyticsAPI: "example.com/mcp-analytics-api:latest",
		Processor:    "example.com/mcp-sentinel-processor:latest",
		UI:           "example.com/mcp-sentinel-ui:latest",
	}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatalf("deployAnalyticsManifestsWithKubectl returned error: %v", err)
	}
	if clickhouseDeleteIndex == -1 {
		t.Fatal("expected setup to delete any existing clickhouse-init job before reapplying it")
	}
	if clickhouseWaitIndex == -1 {
		t.Fatal("expected setup to wait for clickhouse-init job completion")
	}
	if clickhouseDeleteIndex > clickhouseWaitIndex {
		t.Fatalf("expected clickhouse-init delete before wait, got delete index %d wait index %d", clickhouseDeleteIndex, clickhouseWaitIndex)
	}
	if kafkaDeleteIndex == -1 {
		t.Fatal("expected setup to delete any existing kafka-topic-init job before reapplying it")
	}
	if kafkaWaitIndex == -1 {
		t.Fatal("expected setup to wait for kafka-topic-init job completion")
	}
	if kafkaDeleteIndex > kafkaWaitIndex {
		t.Fatalf("expected kafka-topic-init delete before wait, got delete index %d wait index %d", kafkaDeleteIndex, kafkaWaitIndex)
	}
}

func TestGrafanaPrometheusDatasourceUsesRoutePrefix(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/19-grafana-datasources.yaml")
	if err != nil {
		t.Fatalf("failed to read grafana datasource manifest: %v", err)
	}

	rendered, err := renderAnalyticsManifest(string(content), AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatalf("renderAnalyticsManifest returned error: %v", err)
	}
	if !strings.Contains(rendered, "url: http://prometheus:9090/prometheus") {
		t.Fatalf("expected Prometheus datasource URL to include route prefix, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "url: http://prometheus:9090\n") {
		t.Fatalf("Prometheus datasource URL is missing route prefix:\n%s", rendered)
	}
}

func TestPrometheusScrapesProcessorMetricsPort(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatalf("failed to read prometheus manifest: %v", err)
	}
	if !strings.Contains(string(content), `targets: ["mcp-sentinel-processor:9102"]`) {
		t.Fatalf("expected Prometheus to scrape processor metrics port 9102, got:\n%s", content)
	}
	if strings.Contains(string(content), `targets: ["mcp-sentinel-processor:9092"]`) {
		t.Fatalf("Prometheus still scrapes stale processor port 9092:\n%s", content)
	}
}

func TestPrometheusScrapesClickHouseMetricsPort(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatalf("failed to read prometheus manifest: %v", err)
	}
	if !strings.Contains(string(content), `job_name: clickhouse`) {
		t.Fatalf("expected Prometheus to define a ClickHouse scrape job, got:\n%s", content)
	}
	if !strings.Contains(string(content), `targets: ["clickhouse:9363"]`) {
		t.Fatalf("expected Prometheus to scrape ClickHouse metrics port 9363, got:\n%s", content)
	}
}

func TestPrometheusScrapesSplitAPIJobs(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatalf("failed to read prometheus manifest: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"job_name: mcp-platform-api",
		`targets: ["mcp-platform-api:9090"]`,
		"job_name: mcp-runtime-api",
		`targets: ["mcp-runtime-api:9094"]`,
		"job_name: mcp-analytics-api",
		`targets: ["mcp-analytics-api:9095"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Prometheus config to include %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, `job_name: mcp-sentinel-api`) {
		t.Fatalf("Prometheus still references monolith mcp-sentinel-api scrape job:\n%s", text)
	}
}

func TestPrometheusDiscoversGatewaySidecarMetrics(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/11-prometheus.yaml")
	if err != nil {
		t.Fatalf("failed to read prometheus manifest: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"kind: ServiceAccount",
		"name: prometheus",
		"name: mcp-sentinel-prometheus-discovery",
		"resources: [\"endpoints\", \"pods\", \"services\"]",
		"job_name: mcp-gateway-sidecars",
		"role: endpoints",
		"__meta_kubernetes_service_annotation_prometheus_io_scrape",
		"__meta_kubernetes_service_label_app_kubernetes_io_managed_by",
		"__meta_kubernetes_service_annotation_prometheus_io_port",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Prometheus manifest to contain %q, got:\n%s", want, text)
		}
	}
}

func TestClickHouseExposesPrometheusMetrics(t *testing.T) {
	for _, path := range []string{"../../../../k8s/03-clickhouse.yaml", "../../../../k8s/03-clickhouse-hostpath.yaml"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read clickhouse manifest %s: %v", path, err)
		}
		text := string(content)
		for _, want := range []string{
			"name: clickhouse-prometheus-config",
			"<endpoint>/metrics</endpoint>",
			"<port>9363</port>",
			"name: metrics",
			"port: 9363",
			"containerPort: 9363",
			"mountPath: /etc/clickhouse-server/config.d/prometheus.xml",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected %s to contain %q, got:\n%s", path, want, text)
			}
		}
	}
}

func TestTempoLocalBlocksDoNotShareWALPath(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/16-tempo.yaml")
	if err != nil {
		t.Fatalf("failed to read tempo manifest: %v", err)
	}
	if !strings.Contains(string(content), "path: /var/tempo/blocks") {
		t.Fatalf("expected Tempo local block storage under /var/tempo/blocks, got:\n%s", content)
	}
	if !strings.Contains(string(content), "path: /var/tempo/wal") {
		t.Fatalf("expected Tempo WAL storage under /var/tempo/wal, got:\n%s", content)
	}
	if strings.Contains(string(content), "local:\n          path: /var/tempo\n") {
		t.Fatalf("Tempo local block storage must not share the WAL parent path:\n%s", content)
	}
}

func TestAPIManifestIncludesPlatformAdminBootstrapEnv(t *testing.T) {
	content, err := os.ReadFile("../../../../k8s/08-platform-api.yaml")
	if err != nil {
		t.Fatalf("failed to read platform-api manifest: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"key: PLATFORM_ADMIN_EMAIL",
		"key: PLATFORM_ADMIN_PASSWORD",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected api manifest to contain %q, got:\n%s", want, text)
		}
	}
}

func TestDeployAnalyticsManifestsReturnsRolloutFailures(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	core.DefaultCLIConfig = &core.CLIConfig{}
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients(nil, nil))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "k8s")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("failed to create manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	manifestContent := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n  namespace: mcp-sentinel\n"
	for _, name := range []string{
		"00-namespace.yaml",
		"01-config.yaml",
		"03-clickhouse.yaml",
		"03-clickhouse-hostpath.yaml",
		"04-clickhouse-init.yaml",
		"05-kafka.yaml",
		"05-kafka-hostpath.yaml",
		"05-kafka-topic-init.yaml",
		"06-ingest.yaml",
		"07-processor.yaml",
		"08-platform-api.yaml",
		"08-platform-api-rbac.yaml",
		"08-runtime-api.yaml",
		"08-runtime-api-rbac.yaml",
		"08-analytics-api.yaml",
		"22-split-api-networkpolicy.yaml",
		"09-ui.yaml",
		"10-gateway.yaml",
		"11-prometheus.yaml",
		"12-grafana.yaml",
		"15-otel-collector.yaml",
		"16-tempo.yaml",
		"17-loki.yaml",
		"18-promtail.yaml",
		"19-grafana-datasources.yaml",
		"21-grafana-dashboards.yaml",
		"20-postgres.yaml",
		"20-postgres-hostpath.yaml",
	} {
		if err := os.WriteFile(filepath.Join(manifestDir, name), []byte(manifestContent), 0o644); err != nil {
			t.Fatalf("failed to write fixture manifest %s: %v", name, err)
		}
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed to chdir to fixture root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	var applied []string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			switch {
			case contains(spec.Args, "apply") && contains(spec.Args, "-f"):
				for i := 0; i+1 < len(spec.Args); i++ {
					if spec.Args[i] == "-f" {
						applied = append(applied, spec.Args[i+1])
					}
				}
			case contains(spec.Args, "get") && contains(spec.Args, "secret"):
				cmd.OutputData = []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found")
				cmd.OutputErr = errors.New("not found")
			case contains(spec.Args, "rollout") && contains(spec.Args, "deployment/mcp-platform-api"):
				cmd.RunErr = errors.New("image pull failed")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	err = deployAnalyticsManifestsWithKubectl(kubectl, zap.NewNop(), AnalyticsImageSet{
		Ingest:       "example.com/mcp-sentinel-ingest:latest",
		PlatformAPI:  "example.com/mcp-platform-api:latest",
		RuntimeAPI:   "example.com/mcp-runtime-api:latest",
		AnalyticsAPI: "example.com/mcp-analytics-api:latest",
		Processor:    "example.com/mcp-sentinel-processor:latest",
		UI:           "example.com/mcp-sentinel-ui:latest",
	}, "", setupplan.PlatformModeTenant)
	if err == nil {
		t.Fatal("expected rollout failure")
	}
	if !strings.Contains(err.Error(), "deployment/mcp-platform-api") {
		t.Fatalf("expected failing workload in error, got %v", err)
	}
}

func TestDeployAnalyticsManifestsWithKubectl_HostpathUsesHostpathManifests(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{}
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients(nil, nil))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "k8s")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("failed to create manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	manifestContent := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n  namespace: mcp-sentinel\n"
	for _, name := range []string{
		"00-namespace.yaml",
		"01-config.yaml",
		"03-clickhouse-hostpath.yaml",
		"04-clickhouse-init.yaml",
		"05-kafka-hostpath.yaml",
		"05-kafka-topic-init.yaml",
		"20-postgres-hostpath.yaml",
	} {
		if err := os.WriteFile(filepath.Join(manifestDir, name), []byte(manifestContent), 0o644); err != nil {
			t.Fatalf("failed to write fixture manifest %s: %v", name, err)
		}
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed to chdir to fixture root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				cmd.OutputData = []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found")
				cmd.OutputErr = errors.New("not found")
			}
			if contains(spec.Args, "rollout") && contains(spec.Args, "statefulset") && contains(spec.Args, "clickhouse") {
				cmd.RunErr = errors.New("rollout timeout")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	err = deployAnalyticsManifestsWithKubectl(kubectl, zap.NewNop(), AnalyticsImageSet{
		Ingest:       "example.com/mcp-sentinel-ingest:latest",
		PlatformAPI:  "example.com/mcp-platform-api:latest",
		RuntimeAPI:   "example.com/mcp-runtime-api:latest",
		AnalyticsAPI: "example.com/mcp-analytics-api:latest",
		Processor:    "example.com/mcp-sentinel-processor:latest",
		UI:           "example.com/mcp-sentinel-ui:latest",
	}, setupplan.StorageModeHostpath, setupplan.PlatformModeTenant)
	if err == nil {
		t.Fatal("expected failure from rollout timeout")
	}
	if strings.Contains(err.Error(), "03-clickhouse.yaml") || strings.Contains(err.Error(), "05-kafka.yaml") {
		t.Fatalf("expected hostpath manifests to be used (default manifests are not present), got err=%v", err)
	}
}

func TestDeployAnalyticsManifestsWithKubectl_WaitsForPostgresStatefulSet(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{}
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients(nil, nil))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "k8s")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("failed to create manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	manifestContent := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n  namespace: mcp-sentinel\n"
	for _, name := range []string{
		"00-namespace.yaml",
		"01-config.yaml",
		"03-clickhouse.yaml",
		"04-clickhouse-init.yaml",
		"05-kafka.yaml",
		"05-kafka-topic-init.yaml",
		"06-ingest.yaml",
		"07-processor.yaml",
		"08-platform-api.yaml",
		"08-platform-api-rbac.yaml",
		"08-runtime-api.yaml",
		"08-runtime-api-rbac.yaml",
		"08-analytics-api.yaml",
		"22-split-api-networkpolicy.yaml",
		"09-ui.yaml",
		"10-gateway.yaml",
		"11-prometheus.yaml",
		"12-grafana.yaml",
		"15-otel-collector.yaml",
		"16-tempo.yaml",
		"17-loki.yaml",
		"18-promtail.yaml",
		"19-grafana-datasources.yaml",
		"21-grafana-dashboards.yaml",
		"20-postgres.yaml",
	} {
		if err := os.WriteFile(filepath.Join(manifestDir, name), []byte(manifestContent), 0o644); err != nil {
			t.Fatalf("failed to write fixture manifest %s: %v", name, err)
		}
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed to chdir to fixture root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	var sawPostgresStatefulSet bool
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "get") && contains(spec.Args, "secret") {
				cmd.OutputData = []byte("Error from server (NotFound): secrets \"mcp-sentinel-secrets\" not found")
				cmd.OutputErr = errors.New("not found")
			}
			if contains(spec.Args, "rollout") && contains(spec.Args, "statefulset/mcp-sentinel-postgres") {
				sawPostgresStatefulSet = true
				cmd.RunErr = errors.New("rollout timeout")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	err = deployAnalyticsManifestsWithKubectl(kubectl, zap.NewNop(), AnalyticsImageSet{
		Ingest:       "example.com/mcp-sentinel-ingest:latest",
		PlatformAPI:  "example.com/mcp-platform-api:latest",
		RuntimeAPI:   "example.com/mcp-runtime-api:latest",
		AnalyticsAPI: "example.com/mcp-analytics-api:latest",
		Processor:    "example.com/mcp-sentinel-processor:latest",
		UI:           "example.com/mcp-sentinel-ui:latest",
	}, "", setupplan.PlatformModeTenant)
	if err == nil {
		t.Fatal("expected failure from postgres rollout timeout")
	}
	if !sawPostgresStatefulSet {
		t.Fatal("expected setup to wait on statefulset/mcp-sentinel-postgres")
	}
	if !strings.Contains(err.Error(), "statefulset/mcp-sentinel-postgres") {
		t.Fatalf("expected statefulset postgres in error, got %v", err)
	}
}

func TestSetupDepsWithDefaultsSetsNil(t *testing.T) {
	// Avoid importing internal/cli/cluster from this package's tests (import cycle:
	// cli_test -> cluster -> cli). Supply a fake cluster manager; other defaults still apply.
	deps := SetupDeps{ClusterManager: &helperFakeClusterManager{}}.withDefaults(zap.NewNop())
	if deps.ResolveExternalRegistryConfig == nil {
		t.Fatal("expected ResolveExternalRegistryConfig default")
	}
	if _, ok := deps.ClusterManager.(*helperFakeClusterManager); !ok {
		t.Fatal("expected ClusterManager to remain the injected fake")
	}
	if deps.RegistryManager == nil {
		t.Fatal("expected RegistryManager default")
	}
	if deps.LoginRegistry == nil {
		t.Fatal("expected LoginRegistry default")
	}
	if deps.DeployRegistry == nil {
		t.Fatal("expected DeployRegistry default")
	}
	if deps.WaitForDeploymentAvailable == nil {
		t.Fatal("expected WaitForDeploymentAvailable default")
	}
	if deps.WaitForDeploymentRolledOut == nil {
		t.Fatal("expected WaitForDeploymentRolledOut default")
	}
	if deps.PrintDeploymentDiagnostics == nil {
		t.Fatal("expected PrintDeploymentDiagnostics default")
	}
	if deps.SetupTLS == nil {
		t.Fatal("expected SetupTLS default")
	}
	if deps.BuildOperatorImage == nil {
		t.Fatal("expected BuildOperatorImage default")
	}
	if deps.PushOperatorImage == nil {
		t.Fatal("expected PushOperatorImage default")
	}
	if deps.BuildGatewayProxyImage == nil {
		t.Fatal("expected BuildGatewayProxyImage default")
	}
	if deps.PushGatewayProxyImage == nil {
		t.Fatal("expected PushGatewayProxyImage default")
	}
	if deps.EnsureNamespace == nil {
		t.Fatal("expected EnsureNamespace default")
	}
	if deps.EnsureCatalogNamespace == nil {
		t.Fatal("expected EnsureCatalogNamespace default")
	}
	if deps.ResolvePlatformRegistryURL == nil {
		t.Fatal("expected ResolvePlatformRegistryURL default")
	}
	if deps.PushOperatorImageToInternal == nil {
		t.Fatal("expected PushOperatorImageToInternal default")
	}
	if deps.PushGatewayProxyImageToInternal == nil {
		t.Fatal("expected PushGatewayProxyImageToInternal default")
	}
	if deps.DeployOperatorManifests == nil {
		t.Fatal("expected DeployOperatorManifests default")
	}
	if deps.EnsureImagePullSecret == nil {
		t.Fatal("expected EnsureImagePullSecret default")
	}
	if deps.ConfigureProvisionedRegistryEnv == nil {
		t.Fatal("expected ConfigureProvisionedRegistryEnv default")
	}
	if deps.RestartDeployment == nil {
		t.Fatal("expected RestartDeployment default")
	}
	if deps.CheckCRDInstalled == nil {
		t.Fatal("expected CheckCRDInstalled default")
	}
	if deps.GetDeploymentTimeout == nil {
		t.Fatal("expected core.GetDeploymentTimeout default")
	}
	if deps.GetRegistryPort == nil {
		t.Fatal("expected core.GetRegistryPort default")
	}
	if deps.OperatorImageFor == nil {
		t.Fatal("expected OperatorImageFor default")
	}
	if deps.GatewayProxyImageFor == nil {
		t.Fatal("expected GatewayProxyImageFor default")
	}
}

func TestSetupDepsWithDefaultsPreservesNonNil(t *testing.T) {
	clusterMgr := &helperFakeClusterManager{}
	registryMgr := &helperFakeRegistryManager{}
	deps := SetupDeps{
		ClusterManager:  clusterMgr,
		RegistryManager: registryMgr,
		GetRegistryPort: func() int { return 123 },
		OperatorImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "custom-image"
		},
		GatewayProxyImageFor: func(_ *config.ExternalRegistryConfig) string {
			return "custom-gateway-image"
		},
	}

	got := deps.withDefaults(zap.NewNop())
	if got.ClusterManager != clusterMgr {
		t.Fatal("expected ClusterManager to be preserved")
	}
	if got.RegistryManager != registryMgr {
		t.Fatal("expected RegistryManager to be preserved")
	}
	if got.GetRegistryPort() != 123 {
		t.Fatal("expected core.GetRegistryPort to be preserved")
	}
	if got.OperatorImageFor(nil) != "custom-image" {
		t.Fatal("expected OperatorImageFor to be preserved")
	}
	if got.GatewayProxyImageFor(nil) != "custom-gateway-image" {
		t.Fatal("expected GatewayProxyImageFor to be preserved")
	}
}

func TestCheckCRDInstalledWithKubectl(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)

	if err := checkCRDInstalledWithKubectl(kubectl, "example.crd.io"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.Commands) != 1 {
		t.Fatalf("expected 1 kubectl command, got %d", len(mock.Commands))
	}
	if !commandHasArgs(mock.Commands[0], "get", "crd", "example.crd.io") {
		t.Fatalf("unexpected command args: %v", mock.Commands[0].Args)
	}
}

func TestCheckCRDInstalledUsesDefaultKubernetesClient(t *testing.T) {
	swapKubernetesClientsForTest(t, platformTestClientsWithCRD("example.crd.io"))
	mock := &core.MockExecutor{}
	swapDefaultKubectlClientForTest(t, core.NewTestKubectlClient(mock))

	if err := checkCRDInstalled("example.crd.io"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.Commands) != 0 {
		t.Fatalf("expected client-go CRD check to avoid kubectl commands, got %v", mock.Commands)
	}
}

func TestCheckCRDInstalledWithKubectlError(t *testing.T) {
	mock := &core.MockExecutor{DefaultRunErr: errors.New("kubectl failed")}
	kubectl := core.NewTestKubectlClient(mock)

	if err := checkCRDInstalledWithKubectl(kubectl, "example.crd.io"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWaitForDeploymentAvailableWithKubectl(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{Args: spec.Args, OutputData: []byte("1")}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := waitForDeploymentAvailableWithKubectl(kubectl, zap.NewNop(), "registry", "registry", "app=registry", time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.Commands) != 1 {
		t.Fatalf("expected 1 kubectl command, got %d", len(mock.Commands))
	}
	if !commandHasArgs(mock.Commands[0], "get", "deployment", "registry", "-n", "registry", "-o", "jsonpath={.status.availableReplicas}") {
		t.Fatalf("unexpected command args: %v", mock.Commands[0].Args)
	}
}

func TestDeployOperatorManifestsWithKubectlPreservesExistingGatewayOTLPEndpoint(t *testing.T) {
	orig := core.DefaultCLIConfig
	core.DefaultCLIConfig = &core.CLIConfig{}
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})

	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	const customEndpoint = "http://custom-collector.mcp-observability.svc.cluster.local:4318"
	var managerManifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "deployment/"+core.OperatorDeploymentName, "-n", core.NamespaceMCPRuntime) {
				cmd.OutputData = []byte(customEndpoint)
			}
			if isOperatorManagerApplyArgs(spec.Args) {
				cmd.RunFunc = func() error {
					data, err := io.ReadAll(cmd.StdinR)
					if err != nil {
						return err
					}
					managerManifest = string(data)
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), "registry.example.com/mcp-runtime-operator:dev", "", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(managerManifest, "name: MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT") ||
		!strings.Contains(managerManifest, "value: "+customEndpoint) {
		t.Fatalf("expected manager manifest to preserve existing gateway OTLP endpoint, got:\n%s", managerManifest)
	}
	if strings.Contains(managerManifest, "value: "+defaultGatewayOTELExporterOTLPEndpoint) {
		t.Fatalf("expected manager manifest not to overwrite custom gateway OTLP endpoint with default, got:\n%s", managerManifest)
	}
}

func TestWaitForDeploymentAvailableWithKubectlTimeout(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{Args: spec.Args, OutputData: []byte("0")}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := waitForDeploymentAvailableWithKubectl(kubectl, zap.NewNop(), "registry", "registry", "app=registry", -time.Second); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDeployOperatorManifestsWithKubectl(t *testing.T) {

	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	var managerManifest string
	var webhookTLSSecretManifest string
	var webhookServiceManifest string
	var webhookConfigManifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if isOperatorManagerApplyArgs(spec.Args) {
				cmd.RunFunc = func() error {
					data, err := io.ReadAll(cmd.StdinR)
					if err != nil {
						return err
					}
					managerManifest = string(data)
					return nil
				}
			} else if commandHasArgs(spec, "apply", "-f", "-") {
				cmd.RunFunc = func() error {
					data, err := io.ReadAll(cmd.StdinR)
					if err != nil {
						return err
					}
					manifest := string(data)
					switch {
					case strings.Contains(manifest, "name: "+operatorWebhookSecretName):
						webhookTLSSecretManifest = manifest
					case strings.Contains(manifest, "kind: Service") && strings.Contains(manifest, operatorWebhookServiceName):
						webhookServiceManifest = manifest
					case strings.Contains(manifest, "MutatingWebhookConfiguration") || strings.Contains(manifest, "ValidatingWebhookConfiguration"):
						webhookConfigManifest = manifest
					}
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	operatorImage := "registry.example.com/mcp-runtime-operator:dev"
	gatewayProxyImage := "registry.example.com/mcp-sentinel-mcp-gateway:dev"
	operatorArgs := []string{
		"--metrics-bind-address=:9090",
		"--health-probe-bind-address=:9091",
	}
	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), operatorImage, gatewayProxyImage, operatorArgs, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if managerManifest == "" {
		t.Fatal("expected manager manifest to be captured")
	}
	if !strings.Contains(managerManifest, "image: "+operatorImage) {
		t.Fatalf("expected manager manifest to include image %q", operatorImage)
	}
	if !strings.Contains(managerManifest, "imagePullPolicy: Always") {
		t.Fatalf("expected non-test operator image to preserve imagePullPolicy Always, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "- --leader-elect") {
		t.Fatalf("expected manager manifest to preserve leader election flag, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "- --metrics-bind-address=:9090") {
		t.Fatalf("expected manager manifest to include custom metrics arg, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "- --health-probe-bind-address=:9091") {
		t.Fatalf("expected manager manifest to include custom probe arg, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "name: MCP_GATEWAY_PROXY_IMAGE") || !strings.Contains(managerManifest, "value: "+gatewayProxyImage) {
		t.Fatalf("expected manager manifest to include gateway proxy image env, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "name: MCP_SENTINEL_INGEST_URL") || !strings.Contains(managerManifest, "value: "+defaultAnalyticsIngestURL) {
		t.Fatalf("expected manager manifest to include analytics ingest env, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "name: MCP_ENABLE_WEBHOOKS") || !strings.Contains(managerManifest, "value: \"true\"") {
		t.Fatalf("expected manager manifest to enable webhooks, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, "secretName: "+operatorWebhookSecretName) ||
		!strings.Contains(managerManifest, "mountPath: "+operatorWebhookCertDir) {
		t.Fatalf("expected manager manifest to mount webhook cert secret, got:\n%s", managerManifest)
	}
	if !strings.Contains(managerManifest, operatorWebhookCertHashAnnotation+":") {
		t.Fatalf("expected manager manifest to include webhook cert hash annotation, got:\n%s", managerManifest)
	}
	if !strings.Contains(webhookTLSSecretManifest, "name: "+operatorWebhookSecretName) ||
		!strings.Contains(webhookTLSSecretManifest, "tls.crt:") ||
		!strings.Contains(webhookTLSSecretManifest, "tls.key:") {
		t.Fatalf("expected webhook TLS secret manifest, got:\n%s", webhookTLSSecretManifest)
	}
	if !strings.Contains(webhookServiceManifest, "name: "+operatorWebhookServiceName) {
		t.Fatalf("expected webhook service manifest, got:\n%s", webhookServiceManifest)
	}
	if !strings.Contains(webhookConfigManifest, "caBundle:") ||
		!strings.Contains(webhookConfigManifest, "name: "+operatorWebhookServiceName) {
		t.Fatalf("expected webhook configuration with caBundle, got:\n%s", webhookConfigManifest)
	}

	var (
		hasCRD          bool
		hasRBAC         bool
		hasManagerApply bool
		hasNamespace    bool
	)
	for _, cmd := range mock.Commands {
		if commandHasArgs(cmd, "apply", "--validate=false", "-f", "config/crd/bases") {
			hasCRD = true
		}
		if commandHasArgs(cmd, "apply", "-k", "config/rbac/") {
			hasRBAC = true
		}
		if commandHasArgs(cmd, "delete", "deployment/"+core.OperatorDeploymentName, "-n", core.NamespaceMCPRuntime, "--ignore-not-found") {
			t.Fatalf("operator setup must not delete the existing deployment before apply: %v", cmd.Args)
		}
		if isOperatorManagerApplyArgs(cmd.Args) {
			hasManagerApply = true
		}
		if idx := argIndex(cmd.Args, "-f"); idx != -1 && idx+1 < len(cmd.Args) {
			path := cmd.Args[idx+1]
			if path == "-" {
				hasNamespace = true
			}
		}
	}
	if !hasCRD || !hasRBAC || !hasManagerApply || !hasNamespace {
		t.Fatalf("missing expected kubectl commands: crd=%t rbac=%t manager=%t namespace=%t", hasCRD, hasRBAC, hasManagerApply, hasNamespace)
	}
}

func TestDeployOperatorManifestsWithKubectlInjectsImagePullSecret(t *testing.T) {
	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	var managerManifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if isOperatorManagerApplyArgs(spec.Args) {
				cmd.RunFunc = func() error {
					data, err := io.ReadAll(cmd.StdinR)
					if err != nil {
						return err
					}
					managerManifest = string(data)
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), "registry.example.com/mcp-runtime-operator:dev", "", nil, "platform-pull-secret"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(managerManifest, "imagePullSecrets:") || !strings.Contains(managerManifest, "name: platform-pull-secret") {
		t.Fatalf("expected manager manifest to include imagePullSecrets, got:\n%s", managerManifest)
	}
}

func TestEnsureRepoManagedTraefikMiddlewareResourcesAppliesMiddlewareSupportToRepoManagedController(t *testing.T) {
	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	var created []*core.MockCommand
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			created = append(created, cmd)
			switch {
			case commandHasArgs(spec, "get", "deployment", "-A", "--no-headers", "-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name"):
				cmd.OutputData = []byte("traefik traefik\n")
			case commandHasArgs(spec, "get", "deployment", "traefik", "-n", "traefik", "-o", "json"):
				cmd.OutputData = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"traefik","args":["--providers.kubernetesingress=true"],"volumeMounts":[]}],"volumes":[]}}}}`)
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := ensureRepoManagedTraefikMiddlewareResources(kubectl, zap.NewNop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var (
		hasLookup       bool
		hasDynamicApply bool
		hasPluginApply  bool
		hasPatch        bool
	)
	for _, cmd := range mock.Commands {
		if commandHasArgs(cmd, "get", "deployment", "-A", "--no-headers", "-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name") {
			hasLookup = true
		}
		if commandHasArgs(cmd, "patch", "deployment", "traefik", "-n", "traefik", "--type=json") {
			hasPatch = true
		}
	}
	for _, cmd := range created {
		if cmd.StdinR == nil {
			continue
		}
		data, err := io.ReadAll(cmd.StdinR)
		if err != nil {
			t.Fatalf("read stdin: %v", err)
		}
		body := string(data)
		if strings.Contains(body, "name: traefik-dynamic") && strings.Contains(body, "namespace: traefik") {
			hasDynamicApply = true
		}
		if strings.Contains(body, "name: traefik-plugin-pii-redactor") && strings.Contains(body, "namespace: traefik") {
			hasPluginApply = true
		}
	}
	if !hasLookup {
		t.Fatal("expected active traefik deployment lookup")
	}
	if !hasDynamicApply {
		t.Fatal("expected traefik dynamic-config apply")
	}
	if !hasPluginApply {
		t.Fatal("expected traefik plugin-source apply")
	}
	if !hasPatch {
		t.Fatal("expected traefik deployment patch")
	}
}

func TestEnsureRepoManagedTraefikMiddlewareResourcesAppliesMiddlewareSupportToExternalController(t *testing.T) {
	var created []*core.MockCommand
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			created = append(created, cmd)
			switch {
			case commandHasArgs(spec, "get", "deployment", "-A", "--no-headers", "-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name"):
				cmd.OutputData = []byte("kube-system traefik\n")
			case commandHasArgs(spec, "get", "deployment", "traefik", "-n", "kube-system", "-o", "json"):
				cmd.OutputData = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"sidecar","args":[],"volumeMounts":[]},{"name":"traefik","args":["--providers.kubernetesingress"],"volumeMounts":[{"name":"data","mountPath":"/data"}]}],"volumes":[{"name":"data"}]}}}}`)
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := ensureRepoManagedTraefikMiddlewareResources(kubectl, zap.NewNop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var (
		hasDynamicApply bool
		hasPluginApply  bool
		hasPatch        bool
	)
	for _, cmd := range mock.Commands {
		if commandHasArgs(cmd, "patch", "deployment", "traefik", "-n", "kube-system", "--type=json") {
			hasPatch = true
			if idx := argIndex(cmd.Args, "-p"); idx != -1 && idx+1 < len(cmd.Args) {
				patchBody := cmd.Args[idx+1]
				if !strings.Contains(patchBody, "/spec/template/spec/containers/1/") {
					t.Fatalf("expected patch to target traefik container index, got %s", patchBody)
				}
			}
		}
	}
	for _, cmd := range created {
		if cmd.StdinR == nil {
			continue
		}
		data, err := io.ReadAll(cmd.StdinR)
		if err != nil {
			t.Fatalf("read stdin: %v", err)
		}
		body := string(data)
		if strings.Contains(body, "name: traefik-dynamic") && strings.Contains(body, "namespace: kube-system") {
			hasDynamicApply = true
		}
		if strings.Contains(body, "name: traefik-plugin-pii-redactor") && strings.Contains(body, "namespace: kube-system") {
			hasPluginApply = true
		}
	}
	if !hasDynamicApply {
		t.Fatal("expected external traefik dynamic-config apply")
	}
	if !hasPluginApply {
		t.Fatal("expected external traefik plugin-source apply")
	}
	if !hasPatch {
		t.Fatal("expected external traefik deployment patch")
	}
}

func TestPatchTraefikDeploymentForFileMiddlewareSupportTreatsMountPathAsIdempotent(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			switch {
			case commandHasArgs(spec, "get", "deployment", "traefik", "-n", "traefik", "-o", "json"):
				cmd.OutputData = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"traefik","args":["--providers.file.filename=/etc/traefik/dynamic/dynamic.yml","--providers.file.watch=true","--experimental.localplugins.pii-redactor.modulename=github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/pii-redactor","--experimental.localPlugins.spiffe-identity.moduleName=github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/spiffe-identity"],"volumeMounts":[{"name":"traefik-dynamic","mountPath":"/etc/traefik/dynamic"},{"name":"traefik-plugin-source","mountPath":"/plugins-local/src/github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/pii-redactor"},{"name":"spiffe-identity-plugin","mountPath":"/plugins-local/src/github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/spiffe-identity"},{"name":"traefik-plugin-storage","mountPath":"/plugins-storage"}]}],"volumes":[{"name":"traefik-dynamic"},{"name":"traefik-plugin-source"},{"name":"spiffe-identity-plugin"},{"name":"traefik-plugin-storage"}]}}}}`)
			case commandHasArgs(spec, "patch", "deployment", "traefik", "-n", "traefik", "--type=json"):
				t.Fatalf("did not expect duplicate patch when middleware mount paths already exist: %v", spec.Args)
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := patchTraefikDeploymentForFileMiddlewareSupport(kubectl, "traefik"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureRepoManagedTraefikMiddlewareResourcesSkipsWhenMissing(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "deployment", "-A", "--no-headers", "-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name") {
				cmd.OutputData = []byte("")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := ensureRepoManagedTraefikMiddlewareResources(kubectl, zap.NewNop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, cmd := range mock.Commands {
		if idx := argIndex(cmd.Args, "-f"); idx != -1 && idx+1 < len(cmd.Args) {
			t.Fatalf("did not expect manifest apply when repo-managed traefik is absent, got %v", cmd.Args)
		}
	}
}

func TestDeployOperatorManifestsWithKubectlUsesIfNotPresentForTestModeImage(t *testing.T) {

	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	var managerManifest string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if isOperatorManagerApplyArgs(spec.Args) {
				cmd.RunFunc = func() error {
					data, err := io.ReadAll(cmd.StdinR)
					if err != nil {
						return err
					}
					managerManifest = string(data)
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), testModeOperatorImage, "", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if managerManifest == "" {
		t.Fatal("expected manager manifest to be captured")
	}
	if !strings.Contains(managerManifest, "imagePullPolicy: IfNotPresent") {
		t.Fatalf("expected test mode operator image to use IfNotPresent, got:\n%s", managerManifest)
	}
}

func TestDeployOperatorManifestsWithKubectlCRDError(t *testing.T) {
	mockErr := errors.New("apply crd failed")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "apply", "--validate=false", "-f", "config/crd/bases") {
				cmd.RunErr = mockErr
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), "example", "", nil, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeployOperatorManifestsWithKubectlRBACError(t *testing.T) {

	mockErr := errors.New("apply rbac failed")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "apply", "-k", "config/rbac/") {
				cmd.RunErr = mockErr
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), "example", "", nil, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeployOperatorManifestsWithKubectlManagerApplyError(t *testing.T) {

	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	mockErr := errors.New("apply manager failed")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if isOperatorManagerApplyArgs(spec.Args) {
				cmd.RunErr = mockErr
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := deployOperatorManifestsWithKubectl(kubectl, zap.NewNop(), "example", "", nil, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetupTLSWithKubectl(t *testing.T) {
	chdirRepoRootForTest(t)

	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "certificates", "-n", core.NamespaceRegistry, "-o", "json") {
				cmd.RunFunc = func() error {
					if cmd.StdoutW != nil {
						_, _ = cmd.StdoutW.Write([]byte(`{"items":[]}`))
					}
					return nil
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := setupTLSPrivateCA(kubectl, zap.NewNop(), setupplan.Plan{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	timeoutArg := fmt.Sprintf("--timeout=%s", core.GetCertTimeout())
	var (
		hasCRD       bool
		hasSecret    bool
		hasIssuer    bool
		hasNamespace bool
		hasCert      bool
		hasWait      bool
	)
	for _, cmd := range mock.Commands {
		if commandHasArgs(cmd, "get", "crd", core.CertManagerCRDName) {
			hasCRD = true
		}
		if commandHasArgs(cmd, "get", "secret", "mcp-runtime-ca", "-n", "cert-manager") {
			hasSecret = true
		}
		if commandHasArgs(cmd, "apply", "-f", "config/cert-manager/cluster-issuer.yaml") {
			hasIssuer = true
		}
		if commandHasArgs(cmd, "apply", "-f", "-") {
			hasNamespace = true
		}
		// Registry Certificate is applied via `kubectl apply -f - -n registry` with the
		// manifest piped over stdin, not via `apply -f <path>`.
		if commandHasArgs(cmd, "apply", "-f", "-", "-n", core.NamespaceRegistry) {
			hasCert = true
		}
		if commandHasArgs(cmd, "wait", "--for=condition=Ready", "certificate/registry-cert", "-n", core.NamespaceRegistry, timeoutArg) {
			hasWait = true
		}
	}
	if !hasCRD || !hasSecret || !hasIssuer || !hasNamespace || !hasCert || !hasWait {
		t.Fatalf("missing expected kubectl commands: crd=%t secret=%t issuer=%t namespace=%t cert=%t wait=%t", hasCRD, hasSecret, hasIssuer, hasNamespace, hasCert, hasWait)
	}
}

func TestSetupBundledRegistryInternalTLSCreatesMissingCASecret(t *testing.T) {
	chdirRepoRootForTest(t)

	var created []*core.MockCommand
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			created = append(created, cmd)
			if commandHasArgs(spec, "get", "secret", "mcp-runtime-ca", "-n", "cert-manager") {
				cmd.RunErr = errors.New("missing secret")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	err := setupBundledRegistryInternalTLSStep(kubectl, zap.NewNop(), setupplan.Plan{
		RegistryMode: setupplan.RegistryModeBundledHTTPS,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var (
		hasSecretApply bool
		hasIssuerApply bool
		hasCertApply   bool
		hasWait        bool
	)
	for _, cmd := range created {
		spec := core.ExecSpec{Args: cmd.Args}
		if commandHasArgs(spec, "apply", "-f", "config/cert-manager/cluster-issuer.yaml") {
			hasIssuerApply = true
		}
		if commandHasArgs(spec, "wait", "--for=condition=Ready", "certificate/registry-internal-cert", "-n", core.NamespaceRegistry, "--timeout=2m0s") {
			hasWait = true
		}
		if !commandHasArgs(spec, "apply", "-f", "-") || cmd.StdinR == nil {
			continue
		}
		data, err := io.ReadAll(cmd.StdinR)
		if err != nil {
			t.Fatalf("read apply stdin: %v", err)
		}
		body := string(data)
		if strings.Contains(body, `"kind":"Secret"`) && strings.Contains(body, `"name":"mcp-runtime-ca"`) {
			hasSecretApply = true
		}
		if strings.Contains(body, "name: registry-internal-cert") && strings.Contains(body, "secretName: registry-internal-tls") {
			hasCertApply = true
		}
	}
	if !hasSecretApply || !hasIssuerApply || !hasCertApply || !hasWait {
		t.Fatalf("missing expected commands: secret=%t issuer=%t cert=%t wait=%t", hasSecretApply, hasIssuerApply, hasCertApply, hasWait)
	}
}

func TestSetupTLSWithKubectlMissingCRD(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "crd", core.CertManagerCRDName) {
				cmd.RunErr = errors.New("missing crd")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := setupTLSPrivateCA(kubectl, zap.NewNop(), setupplan.Plan{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetupTLSWithKubectlMissingSecret(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "secret", "mcp-runtime-ca", "-n", "cert-manager") {
				cmd.RunErr = errors.New("missing secret")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	if err := setupTLSPrivateCA(kubectl, zap.NewNop(), setupplan.Plan{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetupTLSWithKubectlWaitError(t *testing.T) {

	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if commandHasArgs(spec, "get", "certificates", "-n", core.NamespaceRegistry, "-o", "json") {
				cmd.RunFunc = func() error {
					if cmd.StdoutW != nil {
						_, _ = cmd.StdoutW.Write([]byte(`{"items":[]}`))
					}
					return nil
				}
			}
			if commandHasArgs(spec, "wait", "--for=condition=Ready", "certificate/registry-cert", "-n", core.NamespaceRegistry) {
				cmd.RunErr = errors.New("wait failed")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	swapDefaultKubectlClientForTest(t, kubectl)

	if err := setupTLSPrivateCA(kubectl, zap.NewNop(), setupplan.Plan{}); err == nil {
		t.Fatal("expected error")
	}
}

func commandHasArgs(cmd core.ExecSpec, args ...string) bool {
	for _, arg := range args {
		if !contains(cmd.Args, arg) {
			return false
		}
	}
	return true
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func isOperatorManagerApplyArgs(args []string) bool {
	return contains(args, "apply") &&
		contains(args, "--server-side") &&
		contains(args, "--force-conflicts") &&
		contains(args, "--field-manager=mcp-runtime-setup") &&
		contains(args, "-f") &&
		contains(args, "-")
}

func swapDefaultKubectlClientForTest(t *testing.T, kubectl *core.KubectlClient) {
	t.Helper()
	t.Cleanup(core.SwapDefaultKubectlClient(kubectl))
}

func argIndex(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}

func repoRootForTest(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func chdirRepoRootForTest(t *testing.T) {
	t.Helper()
	root := repoRootForTest(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})
}

func TestIsKafkaClusterIDMismatchLog(t *testing.T) {
	if !isKafkaClusterIDMismatchLog(`kafka.common.InconsistentClusterIdException: The Cluster ID a doesn't match stored clusterId Some(b) in meta.properties`) {
		t.Fatal("expected cluster ID mismatch log to be detected")
	}
	if isKafkaClusterIDMismatchLog(`ordinary startup log without kafka storage mismatch`) {
		t.Fatal("did not expect non-mismatch log to be detected")
	}
}

func TestRecoverKafkaClusterIDMismatchWithKubectlRefusesDestructiveReset(t *testing.T) {
	var commands [][]string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			commands = append(commands, append([]string(nil), spec.Args...))
			cmd := &core.MockCommand{Args: spec.Args}
			switch {
			case slices.Equal(spec.Args, []string{"logs", kafkaPodName, "-n", core.DefaultAnalyticsNamespace, "-c", kafkaPodContainer}):
				cmd.OutputData = []byte(`kafka.common.InconsistentClusterIdException: The Cluster ID a doesn't match stored clusterId Some(b) in meta.properties`)
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	recovered, err := recoverKafkaClusterIDMismatchWithKubectl(kubectl, "")
	if err == nil {
		t.Fatal("expected destructive recovery to be refused")
	}
	if recovered {
		t.Fatal("did not expect Kafka state to be reset")
	}
	if !strings.Contains(err.Error(), "will not delete persistent volume") {
		t.Fatalf("unexpected error: %v", err)
	}
	want := [][]string{{"logs", kafkaPodName, "-n", core.DefaultAnalyticsNamespace, "-c", kafkaPodContainer}}
	if !slices.EqualFunc(commands, want, func(got, want []string) bool {
		return slices.Equal(got, want)
	}) {
		t.Fatalf("kubectl commands = %#v, want %#v", commands, want)
	}
}

func TestKafkaStatefulSetNeedsKRaftRecreateDetectsLegacyLayout(t *testing.T) {
	replicas := int32(1)
	legacy := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "kafka",
			Replicas:    &replicas,
		},
	}
	if !kafkaStatefulSetNeedsKRaftRecreate(legacy) {
		t.Fatal("expected legacy single-broker Kafka layout to require recreate")
	}

	currentReplicas := kafkaKRaftReplicaCount
	current := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"mcpruntime.org/kafka-mode": "kraft"},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         kafkaHeadlessServiceName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Replicas:            &currentReplicas,
		},
	}
	if kafkaStatefulSetNeedsKRaftRecreate(current) {
		t.Fatal("did not expect current KRaft layout to require recreate")
	}
}

// An external Traefik (k3s kube-system) patched by an older setup has only the
// pii-redactor plugin; rerunning setup must add spiffe-identity, or the OAuth
// IngressRoutes that reference it are dropped by Traefik.
func TestTraefikMiddlewarePatchAddsSpiffeIdentityToExistingInstall(t *testing.T) {
	var spec traefikDeploymentSpec
	raw := `{"spec":{"template":{"spec":{"containers":[{"name":"traefik","args":["--providers.file.filename=/etc/traefik/dynamic/dynamic.yml","--providers.file.watch=true","--experimental.localplugins.pii-redactor.modulename=github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/pii-redactor"],"volumeMounts":[{"name":"traefik-dynamic","mountPath":"/etc/traefik/dynamic"},{"name":"traefik-plugin-source","mountPath":"/plugins-local/src/github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/pii-redactor"},{"name":"traefik-plugins","mountPath":"/plugins-storage"}]}],"volumes":[{"name":"traefik-dynamic"},{"name":"traefik-plugin-source"},{"name":"traefik-plugins"}]}}}}`
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	patch, err := traefikMiddlewarePatch(spec, "kube-system")
	if err != nil {
		t.Fatalf("traefikMiddlewarePatch: %v", err)
	}
	var ops []jsonPatchOperation
	if err := json.Unmarshal(patch, &ops); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %+v, want only the spiffe-identity arg, mount, and volume", ops)
	}
	if arg, _ := ops[0].Value.(string); !strings.Contains(arg, "localplugins.spiffe-identity.modulename=") {
		t.Fatalf("first op = %+v, want the spiffe-identity plugin arg", ops[0])
	}
	volume, _ := ops[2].Value.(map[string]any)
	if configMap, _ := volume["configMap"].(map[string]any); configMap["name"] != "traefik-plugin-spiffe-identity" {
		t.Fatalf("volume op = %+v, want the spiffe-identity plugin ConfigMap", ops[2])
	}
}
