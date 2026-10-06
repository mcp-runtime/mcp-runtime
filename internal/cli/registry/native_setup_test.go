package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/registryauth"
)

func registryDeploymentWithEnv(env ...corev1.EnvVar) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: core.RegistryDeploymentName, Namespace: core.NamespaceRegistry},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: env}}}}},
	}
}

func withRegistryNativeAuthActive(t *testing.T, active bool) {
	t.Helper()
	original := registryNativeAuthActive
	registryNativeAuthActive = func() bool { return active }
	t.Cleanup(func() { registryNativeAuthActive = original })
}

func withRegistryClientset(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	original := newRegistryKubernetesClients
	newRegistryKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: cs}, nil }
	t.Cleanup(func() { newRegistryKubernetesClients = original })
}

func TestNativeAuthActiveReadsRegistryDeployment(t *testing.T) {
	ctx := context.Background()
	active, err := NativeAuthActive(ctx, fake.NewSimpleClientset(registryDeploymentWithEnv(corev1.EnvVar{Name: "REGISTRY_AUTH", Value: "token"})))
	if err != nil || !active {
		t.Fatalf("token registry: active=%v err=%v", active, err)
	}
	active, err = NativeAuthActive(ctx, fake.NewSimpleClientset(registryDeploymentWithEnv(corev1.EnvVar{Name: "REGISTRY_HTTP_ADDR", Value: ":5000"})))
	if err != nil || active {
		t.Fatalf("anonymous registry: active=%v err=%v", active, err)
	}
	if _, err := NativeAuthActive(ctx, fake.NewSimpleClientset()); err == nil {
		t.Fatal("missing registry deployment must be reported")
	}
}

func TestEnableNativeAuthForSetupLeavesActiveRegistryUnchanged(t *testing.T) {
	cs := fake.NewSimpleClientset(registryDeploymentWithEnv(corev1.EnvVar{Name: "REGISTRY_AUTH", Value: "token"}))
	withRegistryClientset(t, cs)
	var buf bytes.Buffer
	setDefaultPrinterWriter(t, &buf)

	// An invalid realm and base URL prove the active path makes no API calls.
	if err := EnableNativeAuthForSetup(context.Background(), SetupNativeAuthOptions{Realm: "bad", APIBaseURL: "bad", APIKey: "admin"}); err != nil {
		t.Fatalf("active registry must be left unchanged: %v", err)
	}
	if len(cs.Actions()) != 1 || cs.Actions()[0].GetVerb() != "get" {
		t.Fatalf("active registry must only be read, actions=%v", cs.Actions())
	}
	if err := EnableNativeAuthForSetup(context.Background(), SetupNativeAuthOptions{Realm: "https://p.example.com/api/v1/registry/token"}); err == nil {
		t.Fatal("setup activation requires an administrator key")
	}
}

func TestEnableNativeAuthForSetupRejectsHTTPRealm(t *testing.T) {
	withRegistryClientset(t, fake.NewSimpleClientset(registryDeploymentWithEnv()))
	err := EnableNativeAuthForSetup(context.Background(), SetupNativeAuthOptions{Realm: "http://p.example.com/api/v1/registry/token", APIBaseURL: "https://p.example.com", APIKey: "admin"})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("setup activation must require an HTTPS realm, got %v", err)
	}
}

type recordingCredentialCreator struct{ calls int }

func (r *recordingCredentialCreator) CheckRegistryAdmin(context.Context) error { return nil }
func (r *recordingCredentialCreator) CreateRegistryPullCredential(context.Context, registryauth.PullScope) (platformapi.RegistryPullCredential, error) {
	r.calls++
	return platformapi.RegistryPullCredential{}, errors.New("unexpected credential issuance")
}

func TestConfigureNativeAuthRequiresAcknowledgedBundledBroker(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset(
		registryDeploymentWithEnv(),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api", Namespace: "mcp-platform"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "platform-api", Image: "registry.example.com/mcp-platform-api:v1"}}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: core.NamespaceRegistry}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: core.NamespaceRegistry}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "registry.example.com"}}}},
	)
	for _, name := range []string{"mcp-platform-api-credentials", "mcp-runtime-api-credentials", "mcp-ui-credentials"} {
		owner := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-platform"}, Data: map[string][]byte{"API_KEYS": []byte("service"), "ADMIN_API_KEYS": []byte("service")}}
		if _, err := cs.CoreV1().Secrets("mcp-platform").Create(ctx, owner, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	api := &recordingCredentialCreator{}
	err := configureNativeAuth(ctx, &k8sclient.Clients{Clientset: cs}, api, nativeAuthOptions{Realm: "https://p.example.com/api/v1/registry/token"})
	if err == nil || !strings.Contains(err.Error(), "--allow-bundled-broker") {
		t.Fatalf("bundled broker must require acknowledgement, got %v", err)
	}
	if api.calls != 0 {
		t.Fatal("no credentials may be issued before the broker check passes")
	}

	var buf bytes.Buffer
	setDefaultPrinterWriter(t, &buf)
	err = configureNativeAuth(ctx, &k8sclient.Clients{Clientset: cs}, api, nativeAuthOptions{Realm: "https://p.example.com/api/v1/registry/token", AllowBundledBroker: true, DryRun: true})
	if err != nil {
		t.Fatalf("acknowledged bundled broker dry run: %v", err)
	}
	if !strings.Contains(buf.String(), "running broker replica") {
		t.Fatalf("expected bundled broker warning, got %q", buf.String())
	}
}

func TestPushHelperOverridesLabelAndMountPublisher(t *testing.T) {
	plain := mustOverrides(t, "helper", "")
	if !strings.Contains(plain, `"app.kubernetes.io/name":"registry-push-helper"`) {
		t.Fatalf("helper must carry the NetworkPolicy label: %s", plain)
	}
	if strings.Contains(plain, "publisher") {
		t.Fatalf("anonymous registry helper must not mount publisher credentials: %s", plain)
	}

	var native map[string]any
	if err := json.Unmarshal([]byte(mustOverrides(t, "helper", registryPublisherSecret)), &native); err != nil {
		t.Fatal(err)
	}
	spec := native["spec"].(map[string]any)
	secret := spec["volumes"].([]any)[0].(map[string]any)["secret"].(map[string]any)
	if secret["secretName"] != registryPublisherSecret {
		t.Fatalf("publisher secret = %v", secret["secretName"])
	}
	container := spec["containers"].([]any)[0].(map[string]any)
	mount := container["volumeMounts"].([]any)[0].(map[string]any)
	if mount["mountPath"] != "/registry-publisher" || mount["readOnly"] != true {
		t.Fatalf("publisher mount = %v", mount)
	}
	if spec["securityContext"].(map[string]any)["fsGroup"] == nil {
		t.Fatal("publisher secret must be group-readable by the non-root helper")
	}
}

func mustOverrides(t *testing.T, name, secret string) string {
	t.Helper()
	out, err := registryPushHelperOverrides(name, secret)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPushInClusterUsesPublisherCredentialWhenNativeAuthActive(t *testing.T) {
	withRegistryNativeAuthActive(t, true)
	var execArgs, runArgs []string
	mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		if spec.Name == "kubectl" && contains(spec.Args, "exec") {
			execArgs = append([]string(nil), spec.Args...)
		}
		if spec.Name == "kubectl" && contains(spec.Args, "run") {
			runArgs = append([]string(nil), spec.Args...)
		}
		return &core.MockCommand{Args: spec.Args}
	}}
	mgr := NewRegistryManager(core.NewTestKubectlClient(mock), mock, zap.NewNop())
	var buf bytes.Buffer
	setDefaultPrinterWriter(t, &buf)

	if err := mgr.PushInCluster("source:tag", "target:tag", "registry"); err != nil {
		t.Fatal(err)
	}
	if !contains(execArgs, "--dest-authfile="+registryPublisherAuthFile) {
		t.Fatalf("native push must authenticate with the publisher credential: %v", execArgs)
	}
	if !strings.Contains(strings.Join(runArgs, " "), registryPublisherSecret) {
		t.Fatalf("native helper must mount the publisher secret: %v", runArgs)
	}

	if err := mgr.PushInCluster("source:tag", "target:tag", "mcp-platform"); err == nil || !strings.Contains(err.Error(), "registry namespace") {
		t.Fatalf("native helpers outside the registry namespace must be refused, got %v", err)
	}
}

func TestPushInClusterStaysAnonymousWithoutNativeAuth(t *testing.T) {
	withRegistryNativeAuthActive(t, false)
	var execArgs []string
	mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		if spec.Name == "kubectl" && contains(spec.Args, "exec") {
			execArgs = append([]string(nil), spec.Args...)
		}
		return &core.MockCommand{Args: spec.Args}
	}}
	mgr := NewRegistryManager(core.NewTestKubectlClient(mock), mock, zap.NewNop())
	var buf bytes.Buffer
	setDefaultPrinterWriter(t, &buf)
	if err := mgr.PushInCluster("source:tag", "target:tag", "registry"); err != nil {
		t.Fatal(err)
	}
	for _, arg := range execArgs {
		if strings.HasPrefix(arg, "--dest-authfile") {
			t.Fatalf("test-mode push must not require publisher credentials: %v", execArgs)
		}
	}
}
