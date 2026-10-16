package cluster

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
)

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv(key, previous); err != nil {
				t.Fatalf("restore %s: %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("restore unset %s: %v", key, err)
		}
	})
}

func TestClusterManager_CheckClusterStatus(t *testing.T) {
	t.Run("calls kubectl cluster-info", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultOutput: []byte("Kubernetes control plane is running"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.CheckClusterStatus()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !mock.HasCommand("kubectl") {
			t.Error("expected kubectl to be called")
		}

		// Should call cluster-info
		found := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "kubectl" && contains(cmd.Args, "cluster-info") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected kubectl cluster-info to be called")
		}
	})
}

func TestClusterConfigRunE_WithProviderAndContext(t *testing.T) {
	mockExec := &core.MockExecutor{}
	mockKubectl := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mockKubectl)
	mgr := NewClusterManager(kubectl, mockExec, zap.NewNop())

	configCmd := findClusterSubcommand(t, newTestClusterCommand(mgr), "config")

	tempDir := t.TempDir()
	kubeconfigPath := filepath.Join(tempDir, "config")
	if err := os.WriteFile(kubeconfigPath, []byte("kubeconfig"), 0o644); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	manifestPath := filepath.Join(tempDir, "ingress.yaml")
	if err := os.WriteFile(manifestPath, []byte("kind: List\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := configCmd.Flags().Set("provider", "eks"); err != nil {
		t.Fatalf("set provider: %v", err)
	}
	if err := configCmd.Flags().Set("region", "us-east-1"); err != nil {
		t.Fatalf("set region: %v", err)
	}
	if err := configCmd.Flags().Set("name", "cluster-1"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := configCmd.Flags().Set("kubeconfig", kubeconfigPath); err != nil {
		t.Fatalf("set kubeconfig: %v", err)
	}
	if err := configCmd.Flags().Set("context", "dev"); err != nil {
		t.Fatalf("set context: %v", err)
	}
	if err := configCmd.Flags().Set("ingress-manifest", manifestPath); err != nil {
		t.Fatalf("set ingress-manifest: %v", err)
	}

	if err := configCmd.RunE(configCmd, nil); err != nil {
		t.Fatalf("config RunE error: %v", err)
	}

	if !hasCommand(mockExec.Commands, "aws", "eks", "update-kubeconfig", "--name", "cluster-1", "--region", "us-east-1", "--kubeconfig", kubeconfigPath) {
		t.Fatalf("expected aws update-kubeconfig call, got: %#v", mockExec.Commands)
	}
	if !hasCommand(mockKubectl.Commands, "kubectl", "config", "use-context", "dev") {
		t.Fatalf("expected kubectl config use-context call, got: %#v", mockKubectl.Commands)
	}
	if !hasCommand(mockKubectl.Commands, "kubectl", "get", "ingressclass", "-o", "name") {
		t.Fatalf("expected kubectl get ingressclass call, got: %#v", mockKubectl.Commands)
	}
	if !hasCommand(mockKubectl.Commands, "kubectl", "apply", "-f", manifestPath) {
		t.Fatalf("expected kubectl apply call, got: %#v", mockKubectl.Commands)
	}
}

func TestClusterConfigRunE_UnsupportedProvider(t *testing.T) {
	mockExec := &core.MockExecutor{}
	mockKubectl := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mockKubectl)
	mgr := NewClusterManager(kubectl, mockExec, zap.NewNop())

	configCmd := findClusterSubcommand(t, newTestClusterCommand(mgr), "config")
	if err := configCmd.Flags().Set("provider", "unknown"); err != nil {
		t.Fatalf("set provider: %v", err)
	}

	if err := configCmd.RunE(configCmd, nil); err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if len(mockExec.Commands) > 0 || len(mockKubectl.Commands) > 0 {
		t.Fatalf("expected no commands to be executed, got exec=%v kubectl=%v", mockExec.Commands, mockKubectl.Commands)
	}
}

func findClusterSubcommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, cmd := range root.Commands() {
		if cmd.Use == name {
			return cmd
		}
	}
	t.Fatalf("subcommand %q not found", name)
	return nil
}

func newTestClusterCommand(mgr *ClusterManager) *cobra.Command {
	cmd := &cobra.Command{Use: "cluster"}

	var ingressMode string
	var ingressManifest string
	var forceIngressInstall bool
	var configKubeconfig string
	var configContext string
	var provider string
	var region string
	var clusterName string
	var resourceGroup string
	var project string
	var zone string
	configCmd := &cobra.Command{
		Use: "config",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider != "" {
				if err := mgr.ConfigureKubeconfigFromProvider(provider, region, clusterName, resourceGroup, project, zone, configKubeconfig); err != nil {
					return err
				}
			}
			if configKubeconfig != "" || configContext != "" || provider != "" {
				if err := mgr.ConfigureKubeconfig(configKubeconfig, configContext); err != nil {
					return err
				}
			}
			return mgr.ConfigureClusterWithValues(ingressMode, ingressManifest, forceIngressInstall)
		},
	}
	configCmd.Flags().StringVar(&ingressMode, "ingress", "traefik", "")
	configCmd.Flags().StringVar(&ingressManifest, "ingress-manifest", "config/ingress/overlays/prod", "")
	configCmd.Flags().BoolVar(&forceIngressInstall, "force-ingress-install", false, "")
	configCmd.Flags().StringVar(&configKubeconfig, "kubeconfig", "", "")
	configCmd.Flags().StringVar(&configContext, "context", "", "")
	configCmd.Flags().StringVar(&provider, "provider", "", "")
	configCmd.Flags().StringVar(&region, "region", "us-west-1", "")
	configCmd.Flags().StringVar(&clusterName, "name", "mcp-runtime", "")
	configCmd.Flags().StringVar(&resourceGroup, "resource-group", "", "")
	configCmd.Flags().StringVar(&project, "project", "", "")
	configCmd.Flags().StringVar(&zone, "zone", "", "")

	cmd.AddCommand(configCmd)
	return cmd
}

func TestClusterDiagnosticsCommandsHaveSeparateLifecycleSemantics(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	doctor := newClusterDoctorCmd(mgr)
	if doctor.Use != "doctor" {
		t.Fatalf("doctor command Use = %q", doctor.Use)
	}
	if doctor.Flags().Lookup("for-setup") != nil {
		t.Fatal("doctor must not expose the removed --for-setup flag")
	}
	diagnostics := newClusterDiagnosticsCmd(mgr)
	if diagnostics.Use != "diagnostics" {
		t.Fatalf("diagnostics command Use = %q", diagnostics.Use)
	}
}

func hasCommand(cmds []core.ExecSpec, name string, args ...string) bool {
	for _, cmd := range cmds {
		if cmd.Name != name {
			continue
		}
		if containsAll(cmd.Args, args) {
			return true
		}
	}
	return false
}

func containsAll(slice []string, vals []string) bool {
	for _, val := range vals {
		if !contains(slice, val) {
			return false
		}
	}
	return true
}

func TestClusterManager_EnsureNamespace(t *testing.T) {
	t.Run("calls kubectl apply with namespace yaml via stdin", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.EnsureNamespace("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "kubectl" {
			t.Errorf("expected kubectl, got %s", cmd.Name)
		}
		// Uses apply -f - (stdin)
		if !contains(cmd.Args, "apply") || !contains(cmd.Args, "-f") || !contains(cmd.Args, "-") {
			t.Errorf("expected 'apply -f -' in args, got %v", cmd.Args)
		}
	})
}

func TestClusterManager_InitCluster(t *testing.T) {
	t.Run("sets kubeconfig context when provided", func(t *testing.T) {
		tmpDir := t.TempDir()
		kubeconfig := filepath.Join(tmpDir, "config")
		if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
			t.Fatalf("failed to write kubeconfig: %v", err)
		}

		mock := &core.MockExecutor{
			DefaultOutput: []byte("Switched to context"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.InitCluster(kubeconfig, "my-context")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should call use-context
		found := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "kubectl" && contains(cmd.Args, "use-context") && contains(cmd.Args, "my-context") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected kubectl config use-context my-context to be called")
		}
	})
}

func TestClusterManager_ConfigureKubeconfig(t *testing.T) {
	t.Run("sets KUBECONFIG and switches context", func(t *testing.T) {
		tmpDir := t.TempDir()
		kubeconfig := filepath.Join(tmpDir, "config")
		if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
			t.Fatalf("failed to write kubeconfig: %v", err)
		}

		mock := &core.MockExecutor{
			DefaultOutput: []byte("Switched to context"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		previous := os.Getenv("KUBECONFIG")
		t.Cleanup(func() {
			if err := os.Setenv("KUBECONFIG", previous); err != nil {
				t.Fatalf("failed to restore KUBECONFIG: %v", err)
			}
		})

		if err := mgr.ConfigureKubeconfig(kubeconfig, "my-context"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := os.Getenv("KUBECONFIG"); got != kubeconfig {
			t.Fatalf("expected KUBECONFIG=%q, got %q", kubeconfig, got)
		}

		found := false
		for _, cmd := range mock.Commands {
			if cmd.Name == "kubectl" && contains(cmd.Args, "use-context") && contains(cmd.Args, "my-context") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected kubectl config use-context my-context to be called")
		}
	})

	t.Run("uses default kubeconfig when empty path provided", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("HOME", tmpDir)
		defaultPath := filepath.Join(tmpDir, ".kube", "config")
		if err := os.MkdirAll(filepath.Dir(defaultPath), 0755); err != nil {
			t.Fatalf("failed to create .kube dir: %v", err)
		}
		if err := os.WriteFile(defaultPath, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
			t.Fatalf("failed to write kubeconfig: %v", err)
		}

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		previous := os.Getenv("KUBECONFIG")
		t.Cleanup(func() {
			if err := os.Setenv("KUBECONFIG", previous); err != nil {
				t.Fatalf("failed to restore KUBECONFIG: %v", err)
			}
		})

		if err := mgr.ConfigureKubeconfig("", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := os.Getenv("KUBECONFIG"); got != defaultPath {
			t.Fatalf("expected KUBECONFIG=%q, got %q", defaultPath, got)
		}
	})

	t.Run("errors when kubeconfig is missing", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.ConfigureKubeconfig("/path/does/not/exist", ""); err == nil {
			t.Fatal("expected error for missing kubeconfig")
		}
	})
}

func TestClusterManager_ConfigureKubeconfigFromProvider(t *testing.T) {
	t.Run("dispatches to eks config", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureKubeconfigFromProvider("EKS", "us-west-2", "my-eks", "", "", "", "/tmp/kubeconfig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "aws" {
			t.Fatalf("expected aws command, got %q", cmd.Name)
		}
		if !contains(cmd.Args, "eks") || !contains(cmd.Args, "update-kubeconfig") {
			t.Fatalf("expected aws eks update-kubeconfig args, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, "my-eks") {
			t.Fatalf("expected --name my-eks, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--region") || !contains(cmd.Args, "us-west-2") {
			t.Fatalf("expected --region us-west-2, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--kubeconfig") || !contains(cmd.Args, "/tmp/kubeconfig") {
			t.Fatalf("expected --kubeconfig /tmp/kubeconfig, got %v", cmd.Args)
		}
	})

	t.Run("errors on unsupported provider", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.ConfigureKubeconfigFromProvider("digitalocean", "us-west-2", "cluster", "", "", "", ""); err == nil {
			t.Fatal("expected error for unsupported provider")
		} else if !strings.Contains(err.Error(), "unsupported provider") {
			t.Fatalf("expected unsupported provider error, got %v", err)
		}
	})
}

func TestProvisionEKSCluster(t *testing.T) {
	t.Run("uses eksctl with args", func(t *testing.T) {
		mock := &core.MockExecutor{}
		err := provisionEKSCluster(zap.NewNop(), mock, "us-west-2", 3, "my-eks", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "eksctl" {
			t.Fatalf("expected eksctl command, got %q", cmd.Name)
		}
		if !contains(cmd.Args, "create") || !contains(cmd.Args, "cluster") {
			t.Fatalf("expected eksctl create cluster args, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, "my-eks") {
			t.Fatalf("expected --name my-eks, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--region") || !contains(cmd.Args, "us-west-2") {
			t.Fatalf("expected --region us-west-2, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--nodes") || !contains(cmd.Args, "3") {
			t.Fatalf("expected --nodes 3, got %v", cmd.Args)
		}
	})

	t.Run("defaults cluster name when empty", func(t *testing.T) {
		mock := &core.MockExecutor{}
		err := provisionEKSCluster(zap.NewNop(), mock, "us-west-2", 2, "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, defaultClusterName) {
			t.Fatalf("expected --name %s, got %v", defaultClusterName, cmd.Args)
		}
	})
}

func TestConfigureEKSKubeconfig(t *testing.T) {
	t.Run("uses aws eks update-kubeconfig", func(t *testing.T) {
		mock := &core.MockExecutor{}
		err := configureEKSKubeconfig(mock, "us-west-2", "my-eks", "/tmp/kubeconfig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "aws" {
			t.Fatalf("expected aws command, got %q", cmd.Name)
		}
		if !contains(cmd.Args, "eks") || !contains(cmd.Args, "update-kubeconfig") {
			t.Fatalf("expected aws eks update-kubeconfig args, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, "my-eks") {
			t.Fatalf("expected --name my-eks, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--region") || !contains(cmd.Args, "us-west-2") {
			t.Fatalf("expected --region us-west-2, got %v", cmd.Args)
		}
		if !contains(cmd.Args, "--kubeconfig") || !contains(cmd.Args, "/tmp/kubeconfig") {
			t.Fatalf("expected --kubeconfig /tmp/kubeconfig, got %v", cmd.Args)
		}
	})

	t.Run("defaults cluster name when empty", func(t *testing.T) {
		mock := &core.MockExecutor{}
		err := configureEKSKubeconfig(mock, "us-west-2", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, defaultClusterName) {
			t.Fatalf("expected --name %s, got %v", defaultClusterName, cmd.Args)
		}
	})

	t.Run("returns error when aws command fails", func(t *testing.T) {
		mock := &core.MockExecutor{DefaultRunErr: errors.New("aws failed")}
		err := configureEKSKubeconfig(mock, "us-west-2", "my-eks", "")
		if err == nil {
			t.Fatal("expected error when aws fails")
		}
	})
}

func TestProvisionCluster(t *testing.T) {
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "")

	t.Run("dispatches to kind", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("kind", "us-west-2", 3, "test-cluster", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !mock.HasCommand("kind") {
			t.Error("expected kind command")
		}
	})

	t.Run("returns gke not implemented error", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("gke", "us-west-2", 3, "test-cluster", false)
		if err == nil {
			t.Fatal("expected error for gke")
		}
		if !strings.Contains(err.Error(), "not yet implemented") {
			t.Fatalf("expected not implemented error, got: %v", err)
		}
	})

	t.Run("returns aks not implemented error", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("aks", "us-west-2", 3, "test-cluster", false)
		if err == nil {
			t.Fatal("expected error for aks")
		}
		if !strings.Contains(err.Error(), "not yet implemented") {
			t.Fatalf("expected not implemented error, got: %v", err)
		}
	})

	t.Run("returns error for unsupported provider", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("unknown", "us-west-2", 3, "test-cluster", false)
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
		if !strings.Contains(err.Error(), "unsupported provider") {
			t.Fatalf("expected unsupported provider error, got: %v", err)
		}
	})

	t.Run("normalizes provider name", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster(" KIND ", "us-west-2", 1, "test-cluster", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !mock.HasCommand("kind") {
			t.Error("expected kind command")
		}
	})

	t.Run("rejects invalid node count before shelling out", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("kind", "us-west-2", 0, "test-cluster", false)
		if err == nil {
			t.Fatal("expected error for invalid node count")
		}
		if !errors.Is(err, core.ErrInvalidNodeCount) {
			t.Fatalf("expected ErrInvalidNodeCount, got %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no commands to run, got %v", mock.Commands)
		}
	})

	t.Run("rejects invalid cluster name before shelling out", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("kind", "us-west-2", 1, "Bad_Name", false)
		if err == nil {
			t.Fatal("expected error for invalid cluster name")
		}
		if !errors.Is(err, core.ErrInvalidClusterName) {
			t.Fatalf("expected ErrInvalidClusterName, got %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no commands to run, got %v", mock.Commands)
		}
	})

	t.Run("rejects cluster name control characters before shelling out", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("kind", "us-west-2", 1, "test\ncluster", false)
		if err == nil {
			t.Fatal("expected error for invalid cluster name")
		}
		if !errors.Is(err, core.ErrInvalidClusterName) {
			t.Fatalf("expected ErrInvalidClusterName, got %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected no commands to run, got %v", mock.Commands)
		}
	})

	t.Run("eks provisioning", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("eks", "us-west-2", 3, "test-cluster", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !mock.HasCommand("eksctl") {
			t.Error("expected eksctl command")
		}
	})
}

func TestProvisionKindCluster(t *testing.T) {
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "")

	t.Run("creates cluster with default name", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ProvisionCluster("kind", "us-west-2", 3, "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "kind" {
			t.Fatalf("expected kind command, got %q", cmd.Name)
		}
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, defaultClusterName) {
			t.Fatalf("expected --name %s, got %v", defaultClusterName, cmd.Args)
		}
	})

	t.Run("creates cluster with custom name", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.provisionKindCluster(2, "my-cluster", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if !contains(cmd.Args, "--name") || !contains(cmd.Args, "my-cluster") {
			t.Fatalf("expected --name my-cluster, got %v", cmd.Args)
		}
	})

	t.Run("returns error when kind fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kind" && contains(spec.Args, "create") {
					cmd.RunErr = errors.New("kind failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.provisionKindCluster(1, "test", false)
		if err == nil {
			t.Fatal("expected error when kind fails")
		}
	})

	t.Run("dry run skips docker and kind commands", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.provisionKindCluster(2, "test", true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Fatalf("expected dry run to skip external commands, got %v", mock.Commands)
		}
	})

	t.Run("checks docker before kind create", func(t *testing.T) {
		t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "docker")
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.provisionKindCluster(1, "test", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) < 3 {
			t.Fatalf("expected docker preflight, kind list, and kind create commands, got %v", mock.Commands)
		}
		if got := mock.Commands[0]; got.Name != "docker" || !contains(got.Args, "info") {
			t.Fatalf("expected first command to be docker info, got %v", got)
		}
		if got := mock.Commands[1]; got.Name != "kind" || !contains(got.Args, "get") || !contains(got.Args, "clusters") {
			t.Fatalf("expected second command to list kind clusters, got %v", got)
		}
		if got := mock.Commands[2]; got.Name != "kind" || !contains(got.Args, "create") || !contains(got.Args, "cluster") {
			t.Fatalf("expected third command to create kind cluster, got %v", got)
		}
	})

	t.Run("lets kind auto-detect runtime when provider env is unset", func(t *testing.T) {
		unsetEnvForTest(t, "KIND_EXPERIMENTAL_PROVIDER")
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.provisionKindCluster(1, "test", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.HasCommand("docker") {
			t.Fatalf("expected kind to auto-detect runtime without docker preflight, got %v", mock.Commands)
		}
		if len(mock.Commands) < 2 {
			t.Fatalf("expected kind list and kind create commands, got %v", mock.Commands)
		}
		if got := mock.Commands[0]; got.Name != "kind" || !contains(got.Args, "get") || !contains(got.Args, "clusters") {
			t.Fatalf("expected first command to list kind clusters, got %v", got)
		}
		if got := mock.Commands[1]; got.Name != "kind" || !contains(got.Args, "create") || !contains(got.Args, "cluster") {
			t.Fatalf("expected second command to create kind cluster, got %v", got)
		}
	})

	t.Run("skips docker check for non-docker kind provider", func(t *testing.T) {
		t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "podman")
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		if err := mgr.provisionKindCluster(1, "test", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.HasCommand("docker") {
			t.Fatalf("expected no docker preflight for podman kind provider, got %v", mock.Commands)
		}
		if !mock.HasCommand("kind") {
			t.Fatal("expected kind commands to run")
		}
	})

	t.Run("returns docker preflight error before kind commands", func(t *testing.T) {
		t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "docker")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "docker" {
					cmd.RunErr = errors.New("daemon down")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.provisionKindCluster(1, "test", false)
		if err == nil {
			t.Fatal("expected docker preflight error")
		}
		if !errors.Is(err, core.ErrDockerDaemonNotReachable) {
			t.Fatalf("expected ErrDockerDaemonNotReachable, got %v", err)
		}
		if mock.HasCommand("kind") {
			t.Fatalf("expected no kind commands after docker preflight failure, got %v", mock.Commands)
		}
	})

	t.Run("returns error when cluster already exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kind" && contains(spec.Args, "get") && contains(spec.Args, "clusters") {
					cmd.OutputData = []byte("other\ntest\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.provisionKindCluster(1, "test", false)
		if err == nil {
			t.Fatal("expected existing cluster error")
		}
		if !errors.Is(err, core.ErrKindClusterAlreadyExists) {
			t.Fatalf("expected ErrKindClusterAlreadyExists, got %v", err)
		}
		for _, cmd := range mock.Commands {
			if cmd.Name == "kind" && contains(cmd.Args, "create") {
				t.Fatalf("should not create kind cluster when it already exists: %v", mock.Commands)
			}
		}
	})

	t.Run("returns error when listing kind clusters fails", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if spec.Name == "kind" && contains(spec.Args, "get") && contains(spec.Args, "clusters") {
					cmd.OutputErr = errors.New("kind missing")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.provisionKindCluster(1, "test", false)
		if err == nil {
			t.Fatal("expected kind list error")
		}
		if !strings.Contains(err.Error(), "failed to list existing kind clusters") {
			t.Fatalf("expected kind list guidance, got %v", err)
		}
	})
}

func TestProvisionGKECluster(t *testing.T) {
	t.Run("defaults cluster name", func(t *testing.T) {
		err := provisionGKECluster(zap.NewNop(), "us-west-2", 3, "", false)
		if err == nil {
			t.Fatal("expected not implemented error")
		}
		if !strings.Contains(err.Error(), defaultClusterName) {
			t.Fatalf("expected default cluster name in error, got: %v", err)
		}
	})
}

func TestProvisionAKSCluster(t *testing.T) {
	t.Run("defaults cluster name", func(t *testing.T) {
		err := provisionAKSCluster(zap.NewNop(), "us-west-2", 3, "", false)
		if err == nil {
			t.Fatal("expected not implemented error")
		}
		if !strings.Contains(err.Error(), defaultClusterName) {
			t.Fatalf("expected default cluster name in error, got: %v", err)
		}
	})
}

func TestProvisionEKSClusterError(t *testing.T) {
	mock := &core.MockExecutor{DefaultRunErr: errors.New("eksctl failed")}
	err := provisionEKSCluster(zap.NewNop(), mock, "us-west-2", 3, "test", false)
	if err == nil {
		t.Fatal("expected error when eksctl fails")
	}
}

func TestCheckClusterStatusError(t *testing.T) {
	mock := &core.MockExecutor{
		DefaultErr: errors.New("cluster not accessible"),
	}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	err := mgr.CheckClusterStatus()
	if err == nil {
		t.Fatal("expected error when cluster not accessible")
	}
}

func TestConfigureCluster(t *testing.T) {
	t.Run("skips ingress when mode is none", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "none"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should not have called kubectl for ingress
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "ingressclass") {
				t.Error("should not check ingress class when mode is none")
			}
		}
	})

	t.Run("returns error for unsupported ingress", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "nginx"})
		if err == nil {
			t.Fatal("expected error for unsupported ingress")
		}
		if !strings.Contains(err.Error(), "unsupported ingress") {
			t.Fatalf("expected unsupported ingress error, got: %v", err)
		}
	})

	t.Run("skips install when ingress already present", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "ingressclass") {
					cmd.OutputData = []byte("ingressclass.networking.k8s.io/traefik\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should not have called apply
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") {
				t.Error("should not apply when ingress already present")
			}
		}
	})

	t.Run("forces install when flag set", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "ingress.yaml")
		if err := os.WriteFile(manifestPath, []byte("kind: List\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "ingressclass") {
					cmd.OutputData = []byte("ingressclass.networking.k8s.io/traefik\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Force: true, Manifest: manifestPath})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have called apply despite ingress present
		found := false
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected apply when force is true")
		}
	})

	t.Run("skips repo install when external traefik exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "deployment") && contains(spec.Args, "traefik") && contains(spec.Args, "-n") && contains(spec.Args, "kube-system") {
					cmd.OutputData = []byte("traefik")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") {
				t.Fatalf("should not apply repo-managed ingress when external traefik exists: %v", mock.Commands)
			}
		}
	})

	t.Run("force refuses external traefik", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "deployment") && contains(spec.Args, "traefik") && contains(spec.Args, "-n") && contains(spec.Args, "kube-system") {
					cmd.OutputData = []byte("traefik")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Force: true})
		if err == nil {
			t.Fatal("expected force to refuse external traefik")
		}
		if !strings.Contains(err.Error(), "external Traefik exists") {
			t.Fatalf("expected external Traefik error, got: %v", err)
		}
	})

	t.Run("fails when duplicate traefik installs exist", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "deployment") && contains(spec.Args, "traefik") && contains(spec.Args, "-n") &&
					(contains(spec.Args, "kube-system") || contains(spec.Args, "traefik")) {
					cmd.OutputData = []byte("traefik")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik"})
		if err == nil {
			t.Fatal("expected duplicate traefik error")
		}
		if !strings.Contains(err.Error(), "multiple Traefik installs detected") {
			t.Fatalf("expected duplicate Traefik error, got: %v", err)
		}
	})

	t.Run("does not skip repo install for stale external traefik service alone", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "ingress.yaml")
		if err := os.WriteFile(manifestPath, []byte("kind: List\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "service") && contains(spec.Args, "traefik") && contains(spec.Args, "-n") && contains(spec.Args, "kube-system") {
					cmd.OutputData = []byte("traefik")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Manifest: manifestPath})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		foundApply := false
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") {
				foundApply = true
				break
			}
		}
		if !foundApply {
			t.Fatalf("expected repo-managed ingress install when only a stale external service exists: %v", mock.Commands)
		}
	})

	t.Run("skips repo install when external traefik exists in a custom namespace", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "get") && contains(spec.Args, "deployment") && contains(spec.Args, "-A") && contains(spec.Args, "custom-columns=NS:.metadata.namespace,NAME:.metadata.name") {
					cmd.OutputData = []byte("edge traefik\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") {
				t.Fatalf("should not apply repo-managed ingress when external traefik exists in a custom namespace: %v", mock.Commands)
			}
		}
	})

	t.Run("uses kustomize for directory manifest", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestDir := filepath.Join(tmpDir, "ingress")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(manifestDir, "kustomization.yaml"), []byte("resources: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Manifest: manifestDir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have called apply with -k
		found := false
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") && contains(cmd.Args, "-k") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected apply -k for directory manifest")
		}
	})

	t.Run("uses kustomize for kustomization.yaml file", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestDir := filepath.Join(tmpDir, "ingress")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatal(err)
		}
		kustomizePath := filepath.Join(manifestDir, "kustomization.yaml")
		if err := os.WriteFile(kustomizePath, []byte("resources: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Manifest: kustomizePath})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have called apply with -k pointing to directory
		found := false
		for _, cmd := range mock.Commands {
			if contains(cmd.Args, "apply") && contains(cmd.Args, "-k") && contains(cmd.Args, manifestDir) {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected apply -k with directory for kustomization.yaml")
		}
	})

	t.Run("returns error when apply fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, "ingress.yaml")
		if err := os.WriteFile(manifestPath, []byte("kind: List\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "apply") {
					cmd.RunErr = errors.New("apply failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.ConfigureCluster(IngressOptions{Mode: "traefik", Manifest: manifestPath})
		if err == nil {
			t.Fatal("expected error when apply fails")
		}
	})
}

func TestInitClusterErrors(t *testing.T) {
	t.Run("returns error when CRD install fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		kubeconfig := filepath.Join(tmpDir, "config")
		if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
			t.Fatal(err)
		}

		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "apply") &&
					contains(spec.Args, "-f") &&
					contains(spec.Args, "config/crd/bases/mcpruntime.org_mcpservers.yaml") {
					cmd.RunErr = errors.New("crd install failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.InitCluster(kubeconfig, "")
		if err == nil {
			t.Fatal("expected error when CRD install fails")
		}
	})

	t.Run("returns error when namespace creation fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		kubeconfig := filepath.Join(tmpDir, "config")
		if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
			t.Fatal(err)
		}

		callCount := 0
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				callCount++
				cmd := &core.MockCommand{Args: spec.Args}
				// Fail on second apply (namespace creation)
				if contains(spec.Args, "apply") && callCount > 1 {
					cmd.RunErr = errors.New("namespace creation failed")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewClusterManager(kubectl, mock, zap.NewNop())

		err := mgr.InitCluster(kubeconfig, "")
		if err == nil {
			t.Fatal("expected error when namespace creation fails")
		}
	})
}

func TestEnsureNamespaceError(t *testing.T) {
	mock := &core.MockExecutor{DefaultRunErr: errors.New("apply failed")}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	err := mgr.EnsureNamespace("test-ns")
	if err == nil {
		t.Fatal("expected error when apply fails")
	}
}

func TestConfigureKubeconfigFromProviderAKS(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	err := mgr.ConfigureKubeconfigFromProvider("aks", "us-west-2", "cluster", "rg", "", "", "")
	if err == nil {
		t.Fatal("expected error for aks")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("expected not implemented error, got: %v", err)
	}
}

func TestConfigureKubeconfigFromProviderGKE(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	err := mgr.ConfigureKubeconfigFromProvider("gke", "us-west-2", "cluster", "", "project", "zone", "")
	if err == nil {
		t.Fatal("expected error for gke")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("expected not implemented error, got: %v", err)
	}
}

func TestResolveKubeconfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	defaultPath := filepath.Join(tmpDir, ".kube", "config")
	const k3sPath = "/etc/rancher/k3s/k3s.yaml"

	tests := []struct {
		name       string
		provided   string
		defaultErr error
		k3sErr     error
		want       string
		wantProbes []string
	}{
		{
			name:     "explicit path skips autodetection",
			provided: "/custom/path",
			want:     "/custom/path",
		},
		{
			name:       "existing user config takes precedence over k3s",
			want:       defaultPath,
			wantProbes: []string{defaultPath},
		},
		{
			name:       "missing user and k3s configs returns default path",
			defaultErr: os.ErrNotExist,
			k3sErr:     os.ErrNotExist,
			want:       defaultPath,
			wantProbes: []string{defaultPath, k3sPath},
		},
		{
			name:       "missing user config discovers k3s",
			defaultErr: os.ErrNotExist,
			want:       k3sPath,
			wantProbes: []string{defaultPath, k3sPath},
		},
		{
			name:       "failed user config stat still discovers k3s",
			defaultErr: os.ErrPermission,
			want:       k3sPath,
			wantProbes: []string{defaultPath, k3sPath},
		},
		{
			name:       "failed k3s stat returns default path",
			defaultErr: os.ErrNotExist,
			k3sErr:     os.ErrPermission,
			want:       defaultPath,
			wantProbes: []string{defaultPath, k3sPath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var probes []string
			stat := func(path string) (os.FileInfo, error) {
				probes = append(probes, path)
				switch path {
				case defaultPath:
					return nil, tt.defaultErr
				case k3sPath:
					return nil, tt.k3sErr
				default:
					t.Fatalf("unexpected file probe %q", path)
					return nil, os.ErrNotExist
				}
			}
			path, err := resolveKubeconfigPathWithStat(tt.provided, stat)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, path)
			}
			if !slices.Equal(probes, tt.wantProbes) {
				t.Fatalf("expected file probes %q, got %q", tt.wantProbes, probes)
			}
		})
	}
}

func TestConfigureClusterConfigCmdFlags(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewClusterManager(kubectl, mock, zap.NewNop())

	cmd := findClusterSubcommand(t, newTestClusterCommand(mgr), "config")

	// Verify all flags are registered
	flags := []string{"ingress", "ingress-manifest", "force-ingress-install", "kubeconfig", "context", "provider", "region", "name", "resource-group", "project", "zone"}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected flag %q to be registered", flag)
		}
	}
}
