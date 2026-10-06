package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
)

func TestMain(m *testing.M) {
	orig := newRegistryKubernetesClients
	newRegistryKubernetesClients = func() (*k8sclient.Clients, error) {
		return nil, errors.New("client-go disabled for registry unit tests unless explicitly stubbed")
	}
	code := m.Run()
	newRegistryKubernetesClients = orig
	os.Exit(code)
}

func TestRegistryManager_CheckRegistryStatus(t *testing.T) {
	t.Run("returns error when deployment not found", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultErr: errors.New("not found"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.CheckRegistryStatus("registry")
		if err == nil {
			t.Fatal("expected error when registry not found")
		}
	})

	t.Run("calls kubectl get deployment", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultOutput: []byte("1"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		_ = mgr.CheckRegistryStatus("registry")

		if !mock.HasCommand("kubectl") {
			t.Error("expected kubectl to be called")
		}

		// Should query deployment status
		found := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "kubectl" && contains(cmd.Args, "get") && contains(cmd.Args, "deployment") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected kubectl get deployment to be called")
		}
	})

	t.Run("uses client-go when available", func(t *testing.T) {
		swapRegistryKubernetesClientsForTest(t, func() (*k8sclient.Clients, error) {
			clientset := fake.NewSimpleClientset(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: core.RegistryDeploymentName, Namespace: "registry"},
					Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
					Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: "registry"},
					Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.5", Ports: []corev1.ServicePort{{Port: 5000}}},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "registry-0", Namespace: "registry", Labels: map[string]string{"app": "registry"}},
					Status:     corev1.PodStatus{Phase: corev1.PodRunning},
				},
			)
			return &k8sclient.Clients{Clientset: clientset, Namespace: "default"}, nil
		})

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())
		if err := mgr.CheckRegistryStatus("registry"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.HasCommand("kubectl") {
			t.Fatal("did not expect kubectl fallback when client-go data is available")
		}
	})
}

func TestRegistryManager_LoginRegistry(t *testing.T) {
	t.Run("calls docker login", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.LoginRegistry("localhost:5000", "user", "pass")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !mock.HasCommand("docker") {
			t.Error("expected docker to be called")
		}

		// Check docker login args
		found := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" && contains(cmd.Args, "login") {
				found = true
				if !contains(cmd.Args, "localhost:5000") {
					t.Errorf("expected registry URL in args, got %v", cmd.Args)
				}
				break
			}
		}
		if !found {
			t.Error("expected docker login to be called")
		}
	})
}

func TestRegistryManager_PushDirect(t *testing.T) {
	t.Run("calls docker tag and push", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushDirect("source:tag", "target:tag")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !mock.HasCommand("docker") {
			t.Error("expected docker to be called")
		}

		// Should call docker tag first, then docker push
		tagFound := false
		pushFound := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" && contains(cmd.Args, "tag") {
				tagFound = true
			}
			if cmd.Name == "docker" && contains(cmd.Args, "push") {
				pushFound = true
			}
		}
		if !tagFound {
			t.Error("expected docker tag to be called")
		}
		if !pushFound {
			t.Error("expected docker push to be called")
		}
	})
}

func TestRunRegistryPushRequiresPlatformCredentials(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	t.Setenv("MCP_PLATFORM_API_TOKEN", "")
	t.Setenv("MCP_PLATFORM_API_URL", "")

	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

	err := RunRegistryPush(context.Background(), mgr, "source:tag", "registry.example.com", "demo", "")
	if err == nil {
		t.Fatal("expected missing platform credentials error")
	}
	if !strings.Contains(err.Error(), "server push requires platform credentials") {
		t.Fatalf("error = %v", err)
	}
	if mock.HasCommand("docker") {
		t.Fatal("server push should fail before docker commands when platform credentials are missing")
	}
}

func TestRunAdminRegistryPushOrgScopeDoesNotRequirePlatformLogin(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	t.Setenv("MCP_PLATFORM_API_TOKEN", "")
	t.Setenv("MCP_PLATFORM_API_URL", "")

	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if spec.Name == "kubectl" && contains(spec.Args, "cluster-info") {
				cmd.OutputData = []byte("Kubernetes control plane")
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

	err := RunAdminRegistryPush(context.Background(), mgr, "source:tag", "registry.example.com", "demo", "org", "direct", "registry")
	if err != nil {
		t.Fatalf("RunAdminRegistryPush() error = %v", err)
	}
	if !mock.HasCommand("docker") {
		t.Fatal("expected docker commands for direct admin push")
	}
}

func TestBuildRegistryPushTargetInClusterAutoResolvesInternalRegistry(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
	core.DefaultCLIConfig = &core.CLIConfig{}
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	t.Setenv("MCP_PLATFORM_API_TOKEN", "")
	t.Setenv("MCP_PLATFORM_API_URL", "")

	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			switch {
			case contains(spec.Args, "jsonpath={.spec.clusterIP}"):
				cmd.OutputData = []byte("10.43.69.247")
			case contains(spec.Args, "jsonpath={.spec.ports[0].port}"):
				cmd.OutputData = []byte("5000")
			}
			return cmd
		},
	}
	swapDefaultKubectlForTest(t, mock)

	target, normalizedScope, err := buildRegistryPushTarget(context.Background(), NewRegistryManager(core.DefaultKubectlClient(), mock, zap.NewNop()), nil, "source:tag", "", "demo", "", "in-cluster")
	if err != nil {
		t.Fatalf("buildRegistryPushTarget() error = %v", err)
	}
	if normalizedScope != "" {
		t.Fatalf("normalizedScope = %q, want empty", normalizedScope)
	}
	if want := "registry.registry.svc.cluster.local:5000/demo:tag"; target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
}

func TestScopedRegistryRepositoryHelpers(t *testing.T) {
	if got := prefixRepositoryScope("demo", "public"); got != "public/demo" {
		t.Fatalf("prefixRepositoryScope = %q, want public/demo", got)
	}
	if got := prefixRepositoryScope("public/demo", "public"); got != "public/demo" {
		t.Fatalf("prefixRepositoryScope double-prefixed: %q", got)
	}
	principal := platformapi.Principal{
		Teams: []platformapi.Team{{
			Slug:      "acme",
			Namespace: "mcp-team-acme",
		}},
	}
	if got, err := scopedTenantRegistryRepository("demo", principal); err != nil || got != "acme/demo" {
		t.Fatalf("scopedTenantRegistryRepository team = %q, %v; want acme/demo", got, err)
	}
	if got, err := scopedTenantRegistryRepository("mcp-team-acme/demo", principal); err != nil || got != "mcp-team-acme/demo" {
		t.Fatalf("scopedTenantRegistryRepository namespace prefix = %q, %v; want mcp-team-acme/demo", got, err)
	}
	if _, err := scopedTenantRegistryRepository("user-1/demo", principal); err == nil || !strings.Contains(err.Error(), "one of your teams") {
		t.Fatalf("expected non-team prefix to be rejected, got %v", err)
	}
	if _, err := scopedTenantRegistryRepository("demo", platformapi.Principal{Namespace: "user-1", Subject: "user-1"}); err == nil || !strings.Contains(err.Error(), "no team membership") {
		t.Fatalf("expected user namespace principal to be rejected, got %v", err)
	}
	multiTeam := platformapi.Principal{Teams: []platformapi.Team{{Slug: "ait", Namespace: "mcp-team-ait"}, {Slug: "qa-audit", Namespace: "mcp-team-qa-audit"}}}
	if _, err := scopedTenantRegistryRepository("demo", multiTeam); err == nil || !strings.Contains(err.Error(), "multiple team memberships") {
		t.Fatalf("expected unscoped repository to be rejected for multiple teams, got %v", err)
	}
	if got, err := scopedTenantRegistryRepository("qa-audit/demo", multiTeam); err != nil || got != "qa-audit/demo" {
		t.Fatalf("scopedTenantRegistryRepository explicit team = %q, %v; want qa-audit/demo", got, err)
	}
}

func TestEnsureRegistryStorageSize(t *testing.T) {

	t.Run("skips when size empty", func(t *testing.T) {
		mock := &core.MockExecutor{}
		swapDefaultKubectlForTest(t, mock)

		if err := ensureRegistryStorageSize(zap.NewNop(), "registry", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 0 {
			t.Fatalf("expected no kubectl calls, got %v", mock.Commands)
		}
	})

	t.Run("no-op when size matches", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") && contains(spec.Args, "pvc") {
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("10Gi"))
						}
						return nil
					}
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		if err := ensureRegistryStorageSize(zap.NewNop(), "registry", "10Gi"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 1 {
			t.Fatalf("expected 1 kubectl call, got %d", len(mock.Commands))
		}
		if contains(mock.Commands[0].Args, "patch") {
			t.Fatalf("did not expect patch call")
		}
	})

	t.Run("patches when size differs", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") && contains(spec.Args, "pvc") {
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("5Gi"))
						}
						return nil
					}
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		if err := ensureRegistryStorageSize(zap.NewNop(), "registry", "10Gi"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 2 {
			t.Fatalf("expected 2 kubectl calls, got %d", len(mock.Commands))
		}
		foundPatch := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "kubectl" && contains(cmd.Args, "patch") {
				foundPatch = true
				break
			}
		}
		if !foundPatch {
			t.Fatalf("expected patch command, got %v", mock.Commands)
		}
	})

	t.Run("returns error when get pvc fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") && contains(spec.Args, "pvc") {
					cmd.RunErr = errors.New("pvc not found")
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		err := ensureRegistryStorageSize(zap.NewNop(), "registry", "10Gi")
		if err == nil {
			t.Fatal("expected error when get pvc fails")
		}
	})

	t.Run("returns error when patch fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") && contains(spec.Args, "pvc") {
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write([]byte("5Gi"))
						}
						return nil
					}
				} else if contains(spec.Args, "patch") {
					cmd.RunErr = errors.New("patch failed")
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		err := ensureRegistryStorageSize(zap.NewNop(), "registry", "10Gi")
		if err == nil {
			t.Fatal("expected error when patch fails")
		}
	})
}

func TestShowRegistryInfo(t *testing.T) {
	t.Run("displays registry info when available", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "jsonpath={.spec.clusterIP}") {
					cmd.OutputData = []byte("10.0.0.1")
				} else if contains(spec.Args, "jsonpath={.spec.ports[0].port}") {
					cmd.OutputData = []byte("5000")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ShowRegistryInfo()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "10.0.0.1") {
			t.Errorf("expected IP in output, got: %s", buf.String())
		}
	})

	t.Run("shows warning when registry not found", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultErr: errors.New("not found"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ShowRegistryInfo()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "Registry not found") {
			t.Errorf("expected warning message, got: %s", buf.String())
		}
	})
}

func TestLoginRegistryError(t *testing.T) {
	mock := &core.MockExecutor{DefaultRunErr: errors.New("login failed")}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

	err := mgr.LoginRegistry("localhost:5000", "user", "pass")
	if err == nil {
		t.Fatal("expected error when login fails")
	}
}

func TestPushDirectErrors(t *testing.T) {
	t.Run("returns error when tag fails", func(t *testing.T) {
		mock := &core.MockExecutor{DefaultRunErr: errors.New("tag failed")}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushDirect("source:tag", "target:tag")
		if err == nil {
			t.Fatal("expected error when tag fails")
		}
	})

	t.Run("returns error when push fails", func(t *testing.T) {
		callCount := 0
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				callCount++
				cmd := &core.MockCommand{Args: spec.Args}
				if callCount > 1 { // First call is tag, second is push
					cmd.RunErr = errors.New("push failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushDirect("source:tag", "target:tag")
		if err == nil {
			t.Fatal("expected error when push fails")
		}
	})
}

func TestPushInCluster(t *testing.T) {
	t.Run("returns error when namespace not found", func(t *testing.T) {
		mock := &core.MockExecutor{DefaultRunErr: errors.New("namespace not found")}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "missing-ns")
		if err == nil {
			t.Fatal("expected error when namespace not found")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected namespace not found error, got: %v", err)
		}
	})

	t.Run("returns error when docker save fails", func(t *testing.T) {
		callCount := 0
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				callCount++
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "docker" && contains(spec.Args, "save") {
					cmd.RunErr = errors.New("save failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err == nil {
			t.Fatal("expected error when save fails")
		}
	})

	t.Run("returns error when run helper pod fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "run") {
					cmd.RunErr = errors.New("run failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err == nil {
			t.Fatal("expected error when run helper fails")
		}
	})

	t.Run("returns error when wait fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "wait") {
					cmd.RunErr = errors.New("wait failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err == nil {
			t.Fatal("expected error when wait fails")
		}
	})

	t.Run("returns error when cp fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "cp") {
					cmd.RunErr = errors.New("cp failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err == nil {
			t.Fatal("expected error when cp fails")
		}
	})

	t.Run("returns error when exec skopeo fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "exec") {
					cmd.RunErr = errors.New("exec failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err == nil {
			t.Fatal("expected error when exec fails")
		}
	})

	t.Run("succeeds and cleans up helper pod", func(t *testing.T) {
		deleteCalled := false
		var runArgs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "run") {
					runArgs = append([]string(nil), spec.Args...)
				}
				if spec.Name == "kubectl" && contains(spec.Args, "delete") {
					deleteCalled = true
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.PushInCluster("source:tag", "target:tag", "registry")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !deleteCalled {
			t.Error("expected delete to be called for cleanup")
		}
		overrideArg := ""
		for _, arg := range runArgs {
			if strings.HasPrefix(arg, "--overrides=") {
				overrideArg = arg
				break
			}
		}
		if overrideArg == "" {
			t.Fatalf("expected kubectl run to include --overrides, got %v", runArgs)
		}
		for _, want := range []string{
			`"allowPrivilegeEscalation":false`,
			`"runAsNonRoot":true`,
			`"seccompProfile":{"type":"RuntimeDefault"}`,
			`"drop":["ALL"]`,
		} {
			if !strings.Contains(overrideArg, want) {
				t.Fatalf("override %q missing %s", overrideArg, want)
			}
		}
	})

	t.Run("rewrites registry.local push target to service DNS", func(t *testing.T) {
		origConfig := core.DefaultCLIConfig
		t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
		core.DefaultCLIConfig = &core.CLIConfig{
			RegistryEndpoint:    "registry.local",
			RegistryIngressHost: "registry.local",
			RegistryPort:        5000,
		}

		var skopeoTarget string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kubectl" && contains(spec.Args, "jsonpath={.spec.ports[0].port}") {
					cmd.OutputData = []byte("5000")
				}
				if spec.Name == "kubectl" && contains(spec.Args, "exec") {
					for i, a := range spec.Args {
						if strings.HasPrefix(a, "docker://") && skopeoTarget == "" {
							skopeoTarget = a
							_ = i
						}
					}
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		if err := mgr.PushInCluster("source:tag", "registry.local/mcp-runtime-operator:c63dea8", "registry"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "docker://registry.registry.svc.cluster.local:5000/mcp-runtime-operator:c63dea8"
		if skopeoTarget != want {
			t.Fatalf("expected skopeo target %q, got %q", want, skopeoTarget)
		}
		if !strings.Contains(buf.String(), "helper destination registry.registry.svc.cluster.local:5000/mcp-runtime-operator:c63dea8") {
			t.Fatalf("expected helper destination in output, got %q", buf.String())
		}
	})
}

func TestRewriteTargetHostForInClusterPush(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origConfig })

	cases := []struct {
		name     string
		endpoint string
		ingress  string
		target   string
		wantHost string
	}{
		{
			name:     "rewrites ingress host",
			endpoint: "registry.local",
			ingress:  "registry.local",
			target:   "registry.local/foo:tag",
			wantHost: "registry.registry.svc.cluster.local:5000",
		},
		{
			name:     "rewrites ingress host with port",
			endpoint: "registry.local:5000",
			ingress:  "registry.local",
			target:   "registry.local:5000/foo:tag",
			wantHost: "registry.registry.svc.cluster.local:5000",
		},
		{
			name:     "leaves svc dns target unchanged",
			endpoint: "registry.local",
			ingress:  "registry.local",
			target:   "registry.registry.svc.cluster.local:5000/foo:tag",
			wantHost: "registry.registry.svc.cluster.local:5000",
		},
		{
			name:     "leaves unrelated registry unchanged",
			endpoint: "registry.local",
			ingress:  "registry.local",
			target:   "ghcr.io/owner/foo:tag",
			wantHost: "ghcr.io",
		},
		{
			name:     "rewrites configured endpoint host",
			endpoint: "internal-registry.example.com",
			ingress:  "registry.prod.example.com",
			target:   "internal-registry.example.com/foo:tag",
			wantHost: "registry.registry.svc.cluster.local:5000",
		},
		{
			name:     "rewrites discovered ingress host",
			endpoint: "registry.local",
			target:   "registry.mcpruntime.org/foo:tag",
			wantHost: "registry.registry.svc.cluster.local:5000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core.DefaultCLIConfig = &core.CLIConfig{
				RegistryEndpoint:    tc.endpoint,
				RegistryIngressHost: tc.ingress,
				RegistryPort:        5000,
			}
			var kubectl *core.KubectlClient
			if tc.name == "rewrites discovered ingress host" {
				mock := &core.MockExecutor{
					CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
						switch {
						case contains(spec.Args, "ingress"):
							return &core.MockCommand{OutputData: []byte("registry.mcpruntime.org")}
						case contains(spec.Args, "service"):
							return &core.MockCommand{OutputData: []byte("5000")}
						default:
							return &core.MockCommand{}
						}
					},
				}
				kubectl = core.NewTestKubectlClient(mock)
			}
			got := rewriteTargetHostForInClusterPush(tc.target, kubectl)
			slash := strings.Index(got, "/")
			if slash < 0 {
				t.Fatalf("unexpected result without path: %q", got)
			}
			if got[:slash] != tc.wantHost {
				t.Fatalf("expected host %q, got %q (full: %q)", tc.wantHost, got[:slash], got)
			}
		})
	}
}

func TestRewriteTargetHostForInClusterPush_UsesClientGoDiscovery(t *testing.T) {
	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
	core.DefaultCLIConfig = &core.CLIConfig{
		RegistryEndpoint:    "registry.local",
		RegistryIngressHost: "registry.local",
		RegistryPort:        5000,
	}
	swapRegistryKubernetesClientsForTest(t, func() (*k8sclient.Clients, error) {
		clientset := fake.NewSimpleClientset(
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: core.NamespaceRegistry},
				Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.8", Ports: []corev1.ServicePort{{Port: 5443}}},
			},
			&networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: core.RegistryServiceName, Namespace: core.NamespaceRegistry},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "registry.mcpruntime.org"}}},
			},
		)
		return &k8sclient.Clients{Clientset: clientset, Namespace: "default"}, nil
	})
	got := rewriteTargetHostForInClusterPush("registry.mcpruntime.org/foo:tag", nil)
	if !strings.HasPrefix(got, "registry.registry.svc.cluster.local:5443/") {
		t.Fatalf("expected client-go discovered service port in rewritten host, got %q", got)
	}
}

func TestDeployRegistry(t *testing.T) {

	t.Run("defaults to docker registry type", func(t *testing.T) {

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") &&
					contains(spec.Args, "deployment") &&
					contains(spec.Args, "jsonpath={.status.availableReplicas}") {
					cmd.OutputData = []byte("1")
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		// Create temp manifest dir
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "registry")
		if err := os.MkdirAll(manifestPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(manifestPath, "kustomization.yaml"), []byte("resources: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		err := deployRegistry(zap.NewNop(), "registry", 5000, "", "", manifestPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("applies image override via rendered manifest", func(t *testing.T) {
		origConfig := core.DefaultCLIConfig
		t.Cleanup(func() { core.DefaultCLIConfig = origConfig })
		t.Setenv(registryImageOverrideEnv, "docker.io/library/mcp-runtime-registry:latest")
		core.DefaultCLIConfig = &core.CLIConfig{RegistryEndpoint: "10.43.39.164:5000", RegistryIngressHost: "registry.prod.example.com"}

		var applyCmd *core.MockCommand
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				switch {
				case contains(spec.Args, "kustomize"):
					cmd.OutputData = []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
spec:
  template:
    spec:
      containers:
      - name: registry
        image: registry:2.8.3
`)
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write(cmd.OutputData)
						}
						return nil
					}
				case contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-"):
					applyCmd = cmd
				case contains(spec.Args, "get") &&
					contains(spec.Args, "deployment") &&
					contains(spec.Args, "jsonpath={.status.availableReplicas}"):
					cmd.OutputData = []byte("1")
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		err := deployRegistry(zap.NewNop(), "registry", 5000, "docker", "", "config/registry")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applyCmd == nil {
			t.Fatal("expected apply command to be used for registry image override")
		}
		captured, err := io.ReadAll(applyCmd.StdinR)
		if err != nil {
			t.Fatalf("failed to read apply stdin: %v", err)
		}
		if !strings.Contains(string(captured), "image: docker.io/library/mcp-runtime-registry:latest") {
			t.Fatalf("expected overridden registry image in manifest, got: %s", string(captured))
		}
	})

	t.Run("rewrites registry host in rendered manifest", func(t *testing.T) {
		manifest := "spec:\n  tls:\n  - hosts:\n    - registry.local\n"
		got := rewriteRegistryHost(manifest, "registry.prod.example.com")
		if !strings.Contains(got, "registry.prod.example.com") {
			t.Fatalf("expected rewritten registry host, got: %s", got)
		}
		if strings.Contains(got, "registry.local") {
			t.Fatalf("expected registry.local to be replaced, got: %s", got)
		}
	})

	t.Run("strips registry ingress-shim annotation", func(t *testing.T) {
		manifest := "metadata:\n  annotations:\n    traefik.ingress.kubernetes.io/router.entrypoints: websecure\n    cert-manager.io/cluster-issuer: mcp-runtime-ca\nspec:\n"
		got := stripRegistryClusterIssuerAnnotation(manifest)
		if strings.Contains(got, "cert-manager.io/cluster-issuer") {
			t.Fatalf("expected cert-manager annotation to be stripped, got: %s", got)
		}
		if !strings.Contains(got, "traefik.ingress.kubernetes.io/router.entrypoints: websecure") {
			t.Fatalf("expected Traefik annotation to remain, got: %s", got)
		}
	})

	t.Run("does not strip incidental cluster issuer text", func(t *testing.T) {
		manifest := "metadata:\n  annotations:\n    note: keep cert-manager.io/cluster-issuer: in docs\n    \"cert-manager.io/cluster-issuer\": mcp-runtime-ca\nspec:\n"
		got := stripRegistryClusterIssuerAnnotation(manifest)
		if strings.Contains(got, "\"cert-manager.io/cluster-issuer\"") {
			t.Fatalf("expected quoted cert-manager annotation to be stripped, got: %s", got)
		}
		if !strings.Contains(got, "note: keep cert-manager.io/cluster-issuer: in docs") {
			t.Fatalf("expected incidental text to remain, got: %s", got)
		}
	})

	t.Run("tls overlays do not request ingress-shim certificates", func(t *testing.T) {
		root := repoRootForRegistryTest(t)
		for _, rel := range []string{
			"config/registry/overlays/tls/ingress-tls.yaml",
			"config/registry/overlays/hostpath-tls/ingress-tls.yaml",
		} {
			content, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			if strings.Contains(string(content), "cert-manager.io/cluster-issuer") {
				t.Fatalf("%s must not request an ingress-shim Certificate for registry-tls", rel)
			}
		}
	})

	t.Run("rejects unsupported registry type", func(t *testing.T) {

		mock := &core.MockExecutor{}
		swapDefaultKubectlForTest(t, mock)

		err := deployRegistry(zap.NewNop(), "registry", 5000, "harbor", "", "")
		if err == nil {
			t.Fatal("expected error for unsupported registry type")
		}
		if !strings.Contains(err.Error(), "unsupported registry type") {
			t.Fatalf("expected unsupported registry type error, got: %v", err)
		}
	})

	t.Run("returns error when ensure namespace fails", func(t *testing.T) {

		mock := &core.MockExecutor{DefaultRunErr: errors.New("namespace failed")}
		swapDefaultKubectlForTest(t, mock)

		err := deployRegistry(zap.NewNop(), "registry", 5000, "docker", "", "config/registry")
		if err == nil {
			t.Fatal("expected error when namespace fails")
		}
	})

	t.Run("returns error when apply fails", func(t *testing.T) {

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				switch {
				case contains(spec.Args, "kustomize"):
					cmd.OutputData = []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: registry\n")
					cmd.RunFunc = func() error {
						if cmd.StdoutW != nil {
							_, _ = cmd.StdoutW.Write(cmd.OutputData)
						}
						return nil
					}
				case contains(spec.Args, "apply") && contains(spec.Args, "-f") && contains(spec.Args, "-"):
					cmd.RunErr = errors.New("apply failed")
				}
				return cmd
			},
		}
		swapDefaultKubectlForTest(t, mock)

		err := deployRegistry(zap.NewNop(), "registry", 5000, "docker", "", "config/registry")
		if err == nil {
			t.Fatal("expected error when apply fails")
		}
	})
}

func TestRenderKustomizeManifestWrapsRunError(t *testing.T) {
	cause := errors.New("kustomize failed")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "kustomize") {
				cmd.RunFunc = func() error {
					if cmd.StderrW != nil {
						_, _ = cmd.StderrW.Write([]byte("bad overlay"))
					}
					return cause
				}
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	_, err := renderKustomizeManifest(kubectl, "config/registry")
	if !errors.Is(err, cause) {
		t.Fatalf("renderKustomizeManifest() error = %v, want errors.Is(..., cause)", err)
	}
}

func TestCheckRegistryStatusStarting(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if contains(spec.Args, "deployment") {
				cmd.OutputData = []byte("0/1") // Starting state
			}
			return cmd
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

	var buf bytes.Buffer
	setDefaultPrinterWriter(t, &buf)

	err := mgr.CheckRegistryStatus("registry")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should show "Starting" status for 0/1 replicas
}

func TestRegistryProvisionCmdWithOperatorImage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origConfig := core.DefaultCLIConfig
	t.Cleanup(func() {
		core.DefaultCLIConfig = origConfig
	})
	core.DefaultCLIConfig = &core.CLIConfig{}

	// Mock executor that returns error for make command (build step)
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			cmd := &core.MockCommand{Args: spec.Args}
			if spec.Name == "make" {
				cmd.RunErr = errors.New("make build failed")
			}
			return cmd
		},
	}
	t.Cleanup(core.SwapExecExecutor(mock))
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewRegistryManager(kubectl, mock, zap.NewNop())

	err := RunRegistryProvision(mgr, "registry.example.com", "", "", "registry.example.com/operator:latest", false)
	if err == nil {
		t.Fatal("expected error when build fails")
	}
	if !strings.Contains(err.Error(), "build") && !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected build error, got: %v", err)
	}
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func swapDefaultKubectlForTest(t *testing.T, exec core.Executor) {
	t.Helper()
	t.Cleanup(core.SwapDefaultKubectlClient(core.NewTestKubectlClient(exec)))
}

func swapRegistryKubernetesClientsForTest(t *testing.T, stub func() (*k8sclient.Clients, error)) {
	t.Helper()
	orig := newRegistryKubernetesClients
	newRegistryKubernetesClients = stub
	t.Cleanup(func() { newRegistryKubernetesClients = orig })
}

func int32Ptr(v int32) *int32 { return &v }

func setDefaultPrinterWriter(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	prevWriter := core.DefaultPrinter.Writer
	prevQuiet := core.DefaultPrinter.Quiet
	core.DefaultPrinter.Writer = w
	core.DefaultPrinter.Quiet = false
	t.Cleanup(func() {
		core.DefaultPrinter.Writer = prevWriter
		core.DefaultPrinter.Quiet = prevQuiet
	})
}

func repoRootForRegistryTest(t *testing.T) string {
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
