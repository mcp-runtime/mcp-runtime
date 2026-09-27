package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/authfile"
	"mcp-runtime/pkg/metadata"
)

func newKubeTestServerManager(kubectl *core.KubectlClient) *ServerManager {
	mgr := NewServerManager(kubectl, zap.NewNop())
	mgr.useKube = true
	return mgr
}

func TestInitServerCreatesMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	if err := mgr.InitServer("payments", dir, "", "v1", "tenant", "allow-list", "deny", true, 8088, []string{"add", "add", "echo"}, []string{"refund_invoice:high:destructive"}, "", false); err != nil {
		t.Fatalf("InitServer() error = %v", err)
	}

	metadataPath := filepath.Join(dir, "servers.yaml")
	rawMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	raw := string(rawMetadata)
	// auth.mode is written explicitly so downstream tooling sees it without LoadFromFile.
	if !strings.Contains(raw, "mode: header") {
		t.Fatalf("raw metadata missing auth mode:\n%s", rawMetadata)
	}
	// These remain platform-managed: only filled in by LoadFromFile, not written by init.
	for _, platformDefault := range []string{
		"enforceOn:",
		"policyVersion:",
		"store:",
		"headerName:",
		"maxLifetime:",
		"idleTimeout:",
		"upstreamTokenHeader:",
		"port: 8091",
		"upstreamURL:",
	} {
		if strings.Contains(raw, platformDefault) {
			t.Fatalf("raw metadata contains platform-managed default %q:\n%s", platformDefault, rawMetadata)
		}
	}

	registry, err := metadata.LoadFromFile(metadataPath)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	if registry.Version != "v1" || len(registry.Servers) != 1 {
		t.Fatalf("registry = %#v", registry)
	}
	server := registry.Servers[0]
	if server.Name != "payments" || server.Image != "payments" || server.ImageTag != "v1" || server.Scope != metadata.PublishScope("tenant") {
		t.Fatalf("server metadata = %#v", server)
	}
	if server.Route != "/payments/mcp" || server.PublicPathPrefix != "payments" || server.Port != 8088 {
		t.Fatalf("server route/port = %#v", server)
	}
	if len(server.Tools) != 3 || server.Tools[0].Name != "add" || server.Tools[0].SideEffect != metadata.ToolSideEffectRead {
		t.Fatalf("tools = %#v", server.Tools)
	}
	if server.Tools[2].Name != "refund_invoice" || server.Tools[2].RequiredTrust != metadata.TrustLevelHigh || server.Tools[2].SideEffect != metadata.ToolSideEffectDestructive {
		t.Fatalf("tool spec = %#v, want refund_invoice/high/destructive", server.Tools[2])
	}
	if server.Gateway == nil || !server.Gateway.Enabled {
		t.Fatalf("gateway = %#v, want enabled", server.Gateway)
	}
	if server.Auth == nil || server.Auth.Mode != metadata.AuthModeHeader {
		t.Fatalf("auth.mode = %#v, want header", server.Auth)
	}
	if server.Auth.HumanIDHeader == "" || server.Auth.AgentIDHeader == "" {
		t.Fatalf("auth header defaults not populated by LoadFromFile: auth=%#v", server.Auth)
	}
	if server.Policy == nil || server.Policy.Mode != metadata.PolicyModeAllowList || server.Policy.DefaultDecision != metadata.PolicyDecisionDeny {
		t.Fatalf("policy = %#v, want allow-list/deny", server.Policy)
	}
	if server.Session == nil || !server.Session.Required {
		t.Fatalf("session = %#v, want required", server.Session)
	}
}

func TestInitServerSetsToolRisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	if err := mgr.InitServer("payments", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, []string{"add"}, []string{"refund_invoice:high:destructive"}, "high", false); err != nil {
		t.Fatalf("InitServer() error = %v", err)
	}
	registry, err := metadata.LoadFromFile(filepath.Join(dir, "servers.yaml"))
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	for _, tool := range registry.Servers[0].Tools {
		if tool.RiskLevel != metadata.ToolRiskLevelHigh {
			t.Fatalf("tool risk = %#v, want high for %#v", tool.RiskLevel, tool)
		}
	}
}

func TestInitServerAppendsAndRejectsDuplicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	if err := mgr.InitServer("one", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, nil, nil, "", false); err != nil {
		t.Fatalf("InitServer(one) error = %v", err)
	}
	if err := mgr.InitServer("two", dir, "custom/two", "v2", "org", "allow-list", "deny", true, 9000, []string{"search"}, nil, "", false); err != nil {
		t.Fatalf("InitServer(two) error = %v", err)
	}
	err := mgr.InitServer("one", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, nil, nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("duplicate error = %v, want --force guidance", err)
	}

	registry, err := metadata.LoadFromFile(filepath.Join(dir, "servers.yaml"))
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	if len(registry.Servers) != 2 {
		t.Fatalf("servers = %#v, want 2", registry.Servers)
	}
}

// The quickstart runs `server init` inside examples/oauth-example-go-2025-11-25,
// whose .mcp/servers.yaml already lists a server without an image. Merging the
// new entry must not persist the loader's registry.local placeholder into that
// sibling entry.
func TestInitServerDoesNotPersistLoaderDefaultsIntoExistingEntries(t *testing.T) {
	for _, key := range []string{"MCP_REGISTRY_INGRESS_HOST", "MCP_REGISTRY_HOST", "MCP_PLATFORM_DOMAIN"} {
		t.Setenv(key, "")
	}
	dir := filepath.Join(t.TempDir(), ".mcp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "servers.yaml")
	if err := os.WriteFile(path, []byte(`version: v1
servers:
  - name: oauth-example-go-2025-11-25
    route: /oauth-example-go-2025-11-25/mcp
    port: 8088
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	if err := mgr.InitServer("workspace-demo", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, []string{"echo"}, nil, "", false); err != nil {
		t.Fatalf("InitServer() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(data), metadata.DefaultRegistryHost) {
		t.Fatalf("init persisted the %s placeholder:\n%s", metadata.DefaultRegistryHost, data)
	}
	registry, err := metadata.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	if len(registry.Servers) != 2 || registry.Servers[1].Image != "workspace-demo" {
		t.Fatalf("servers = %#v, want sibling kept and workspace-demo appended with a short image", registry.Servers)
	}
}

func TestInitServerUsesGovernanceFlags(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	if err := mgr.InitServer("payments", dir, "", "latest", "tenant", "observe", "allow", false, 8088, nil, nil, "", false); err != nil {
		t.Fatalf("InitServer() error = %v", err)
	}

	registry, err := metadata.LoadFromFile(filepath.Join(dir, "servers.yaml"))
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	server := registry.Servers[0]
	if server.Policy == nil || server.Policy.Mode != metadata.PolicyModeObserve || server.Policy.DefaultDecision != metadata.PolicyDecisionAllow {
		t.Fatalf("policy = %#v, want observe/allow", server.Policy)
	}
	if server.Session != nil {
		t.Fatalf("session = %#v, want omitted for required=false", server.Session)
	}
}

func TestInitServerRejectsInvalidGovernanceFlags(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitServer("payments", dir, "", "latest", "tenant", "audit", "deny", true, 8088, nil, nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "invalid policy mode") {
		t.Fatalf("policy mode error = %v, want invalid policy mode", err)
	}
	err = mgr.InitServer("payments", dir, "", "latest", "tenant", "allow-list", "maybe", true, 8088, nil, nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "invalid default decision") {
		t.Fatalf("default decision error = %v, want invalid default decision", err)
	}
}

func TestInitServerForceReplacesExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	if err := mgr.InitServer("payments", dir, "payments", "v1", "tenant", "allow-list", "deny", true, 8088, []string{"add"}, nil, "", false); err != nil {
		t.Fatalf("InitServer() error = %v", err)
	}
	if err := mgr.InitServer("payments", dir, "payments-v2", "v2", "tenant", "allow-list", "deny", true, 9090, []string{"echo"}, nil, "", true); err != nil {
		t.Fatalf("InitServer(force) error = %v", err)
	}
	registry, err := metadata.LoadFromFile(filepath.Join(dir, "servers.yaml"))
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}
	if len(registry.Servers) != 1 {
		t.Fatalf("servers = %#v, want 1", registry.Servers)
	}
	server := registry.Servers[0]
	if server.Image != "payments-v2" || server.ImageTag != "v2" || server.Port != 9090 || len(server.Tools) != 1 || server.Tools[0].Name != "echo" {
		t.Fatalf("server = %#v", server)
	}
}

func TestInitServerRejectsDuplicateToolSpec(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitServer("payments", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, []string{"add"}, []string{"add:high:write"}, "", false)
	if err == nil || !strings.Contains(err.Error(), "duplicate tool metadata") {
		t.Fatalf("error = %v, want duplicate tool metadata", err)
	}
}

func TestInitServerRejectsInvalidToolSpec(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mcp")
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitServer("payments", dir, "", "latest", "tenant", "allow-list", "deny", true, 8088, nil, []string{"refund_invoice:full:danger"}, "", false)
	if err == nil || !strings.Contains(err.Error(), "trust must be low, medium, or high") {
		t.Fatalf("error = %v, want trust validation", err)
	}
}

func TestServerManager_ListServers(t *testing.T) {
	t.Run("calls kubectl with correct args", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultOutput: []byte("server1\nserver2\n"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ListServers("test-ns", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(mock.Commands) == 0 {
			t.Fatal("expected kubectl command to be called")
		}

		cmd := mock.LastCommand()
		if cmd.Name != "kubectl" {
			t.Errorf("expected kubectl, got %s", cmd.Name)
		}

		// Check args contain namespace
		found := false
		for i, arg := range cmd.Args {
			if arg == "-n" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "test-ns" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected -n test-ns in args, got %v", cmd.Args)
		}
	})

	t.Run("trims namespace and passes to kubectl", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ListServers(" test-ns ", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		found := false
		for i, arg := range cmd.Args {
			if arg == "-n" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "test-ns" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected trimmed namespace in args, got %v", cmd.Args)
		}
	})

	t.Run("rejects empty namespace", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ListServers("   ", "")
		if err != nil {
			t.Fatalf("unexpected error for empty namespace: %v", err)
		}
		cmd := mock.LastCommand()
		found := false
		for i, arg := range cmd.Args {
			if arg == "-n" && i+1 < len(cmd.Args) && cmd.Args[i+1] == core.NamespaceMCPServers {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected default namespace %q in args, got %v", core.NamespaceMCPServers, cmd.Args)
		}
	})

	t.Run("rejects --team when using kubectl mode", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ListServers("", "team-a")
		if err == nil {
			t.Fatal("expected error when team is set in --use-kube mode")
		}
		if !strings.Contains(err.Error(), "cannot use --team with --use-kube") {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.Commands) != 0 {
			t.Fatalf("expected no kubectl command on validation error, got %d", len(mock.Commands))
		}
	})
}

func TestServerManager_ListServersModeSelection(t *testing.T) {
	t.Run("uses platform API by default when logged in", func(t *testing.T) {
		apiCalls := 0
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			if r.URL.Path != "/api/v1/runtime/servers" {
				t.Fatalf("unexpected platform path %q", r.URL.Path)
			}
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"servers":[{"name":"oauth-example-go-2025-11-25","namespace":"mcp-team-acme","ready":"True","status":"Ready","age":"1m"}]}`))
		}))
		defer api.Close()
		t.Setenv(authfile.EnvAPIToken, "token-1")
		t.Setenv(authfile.EnvAPIURL, api.URL)
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewServerManager(kubectl, zap.NewNop())

		out := captureStdout(t, func() error {
			return mgr.ListServers("", "")
		})
		if !strings.Contains(out, "oauth-example-go-2025-11-25") {
			t.Fatalf("platform list output = %q, want server name", out)
		}
		if apiCalls != 1 {
			t.Fatalf("platform API calls = %d, want 1", apiCalls)
		}
		if len(mock.Commands) != 0 {
			t.Fatalf("default platform mode should not call kubectl, got %d commands", len(mock.Commands))
		}
	})

	t.Run("missing platform auth does not fall back to kube", func(t *testing.T) {
		t.Setenv(authfile.EnvAPIToken, "")
		t.Setenv(authfile.EnvAPIURL, "")
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewServerManager(kubectl, zap.NewNop())

		err := mgr.ListServers("", "")
		if err == nil {
			t.Fatal("expected missing platform auth error")
		}
		if !strings.Contains(err.Error(), "mcp-runtime auth login --api-url <platform-url>") {
			t.Fatalf("error missing platform login guidance: %v", err)
		}
		if len(mock.Commands) != 0 {
			t.Fatalf("missing platform auth should not fall back to kubectl, got %d commands", len(mock.Commands))
		}
	})

	t.Run("explicit kube does not call platform API", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("platform API should not be called in explicit kube mode: %s", r.URL.Path)
		}))
		defer api.Close()
		t.Setenv(authfile.EnvAPIToken, "token-1")
		t.Setenv(authfile.EnvAPIURL, api.URL)
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		if err := mgr.ListServers("mcp-servers", ""); err != nil {
			t.Fatalf("unexpected kube list error: %v", err)
		}
		if len(mock.Commands) != 1 {
			t.Fatalf("explicit kube mode commands = %d, want 1", len(mock.Commands))
		}
		if !contains(mock.LastCommand().Args, "mcpserver") {
			t.Fatalf("kubectl args = %v, want mcpserver", mock.LastCommand().Args)
		}
	})

	t.Run("explicit kube forbidden error explains admin boundary", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultRunErr: errors.New(`Error from server (Forbidden): mcpservers.mcpruntime.org is forbidden: User "alice" cannot list resource "mcpservers"`),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ListServers("mcp-servers", "")
		if err == nil {
			t.Fatal("expected forbidden kube error")
		}
		if !strings.Contains(err.Error(), "Direct Kubernetes mode requires admin/operator cluster access") {
			t.Fatalf("error missing admin boundary guidance: %v", err)
		}
		if !strings.Contains(err.Error(), "mcp-runtime auth login --api-url <platform-url>") {
			t.Fatalf("error missing normal platform guidance: %v", err)
		}
	})
}

func TestServerManager_DeleteServer(t *testing.T) {
	t.Run("validates server name", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		// Invalid name with special chars
		err := mgr.DeleteServer("bad;name", "test-ns")
		if err == nil {
			t.Fatal("expected error for invalid server name")
		}

		// Should not have called kubectl
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl with invalid name")
		}
	})

	t.Run("calls kubectl delete with correct args", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.DeleteServer("my-server", "test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "kubectl" {
			t.Errorf("expected kubectl, got %s", cmd.Name)
		}

		// Should contain delete, mcpserver, name, namespace
		argsStr := ""
		for _, a := range cmd.Args {
			argsStr += a + " "
		}
		if !contains(cmd.Args, "delete") {
			t.Errorf("expected 'delete' in args: %s", argsStr)
		}
		if !contains(cmd.Args, "mcpserver") {
			t.Errorf("expected 'mcpserver' in args: %s", argsStr)
		}
		if !contains(cmd.Args, "my-server") {
			t.Errorf("expected 'my-server' in args: %s", argsStr)
		}
	})
}

func TestServerManager_GetServer(t *testing.T) {
	t.Run("validates inputs", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.GetServer("invalid|name", "ns")
		if err == nil {
			t.Fatal("expected error for invalid name")
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl with invalid input")
		}
	})
}

func TestServerManager_CreateServer(t *testing.T) {
	t.Run("requires image", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServer("my-server", "test-ns", "", "latest")
		if err != core.ErrImageRequired {
			t.Fatalf("expected core.ErrImageRequired, got %v", err)
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl when image is missing")
		}
	})

	t.Run("validates inputs", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServer("bad;name", "test-ns", "img", "latest")
		if err == nil {
			t.Fatal("expected error for invalid name")
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl with invalid input")
		}
	})

	t.Run("rejects tag with control characters", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServer("my-server", "test-ns", "repo/image", "bad\n")
		if err == nil {
			t.Fatal("expected error for invalid tag")
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl with invalid tag")
		}
	})

	t.Run("creates manifest and applies via kubectl", func(t *testing.T) {
		var applyCmd *core.MockCommand
		mockExec := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				applyCmd = &core.MockCommand{Args: spec.Args}
				return applyCmd
			},
		}
		kubectl, err := core.NewKubectlClient(mockExec)
		if err != nil {
			t.Fatalf("failed to create kubectl client: %v", err)
		}
		mgr := newKubeTestServerManager(kubectl)

		err = mgr.CreateServer("my-server", "test-ns", "repo/image", "v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mockExec.Commands) == 0 {
			t.Fatal("expected kubectl command to be called")
		}
		cmd := mockExec.LastCommand()
		if cmd.Name != "kubectl" {
			t.Errorf("expected kubectl, got %s", cmd.Name)
		}
		if !contains(cmd.Args, "apply") || !contains(cmd.Args, "-f") || !contains(cmd.Args, "-") {
			t.Errorf("expected apply -f args, got %v", cmd.Args)
		}
		captured, err := io.ReadAll(applyCmd.StdinR)
		if err != nil {
			t.Fatalf("failed to read manifest from stdin: %v", err)
		}

		var manifest mcpServerManifest
		if err := yaml.Unmarshal(captured, &manifest); err != nil {
			t.Fatalf("failed to parse manifest: %v", err)
		}
		if manifest.Metadata.Name != "my-server" {
			t.Errorf("expected name my-server, got %q", manifest.Metadata.Name)
		}
		if manifest.Metadata.Namespace != "test-ns" {
			t.Errorf("expected namespace test-ns, got %q", manifest.Metadata.Namespace)
		}
		if manifest.Spec.Image != "repo/image" {
			t.Errorf("expected image repo/image, got %q", manifest.Spec.Image)
		}
		if manifest.Spec.ImageTag != "v1" {
			t.Errorf("expected tag v1, got %q", manifest.Spec.ImageTag)
		}
	})
}

func TestServerManager_CreateServerFromFile(t *testing.T) {
	t.Run("rejects missing file", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServerFromFile("does-not-exist.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl when file is missing")
		}
	})

	t.Run("rejects directory path", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		dir := t.TempDir()
		err := mgr.CreateServerFromFile(dir)
		if err == nil {
			t.Fatal("expected error for directory path")
		}
		if len(mock.Commands) > 0 {
			t.Error("should not call kubectl when path is a directory")
		}
	})

	t.Run("applies file via kubectl stdin with default validators", func(t *testing.T) {
		var applyCmd *core.MockCommand
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				applyCmd = &core.MockCommand{Args: spec.Args}
				return applyCmd
			},
		}
		kubectl, err := core.NewKubectlClient(mock)
		if err != nil {
			t.Fatalf("failed to create kubectl client: %v", err)
		}
		mgr := newKubeTestServerManager(kubectl)

		tmpFile, err := os.CreateTemp("", "mcpserver-test-*.yaml")
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		if _, err := tmpFile.WriteString("apiVersion: v1\nkind: Namespace\n"); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		if err := tmpFile.Close(); err != nil {
			t.Fatalf("failed to close temp file: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

		err = mgr.CreateServerFromFile(tmpFile.Name())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if cmd.Name != "kubectl" {
			t.Errorf("expected kubectl, got %s", cmd.Name)
		}
		if !contains(cmd.Args, "apply") || !contains(cmd.Args, "-f") || !contains(cmd.Args, "-") {
			t.Errorf("expected apply -f - args, got %v", cmd.Args)
		}
		captured, err := io.ReadAll(applyCmd.StdinR)
		if err != nil {
			t.Fatalf("failed to read manifest from stdin: %v", err)
		}
		if string(captured) != "apiVersion: v1\nkind: Namespace\n" {
			t.Fatalf("unexpected manifest contents: %q", string(captured))
		}
	})
}

func TestServerManager_ViewServerLogs(t *testing.T) {
	t.Run("builds logs command without follow", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ViewServerLogs("my-server", "test-ns", false, false, 200, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if !contains(cmd.Args, "logs") || !contains(cmd.Args, "-l") || !contains(cmd.Args, "-n") {
			t.Errorf("unexpected args: %v", cmd.Args)
		}
		if contains(cmd.Args, "-f") {
			t.Errorf("did not expect -f in args: %v", cmd.Args)
		}
	})

	t.Run("adds follow flag when requested", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.ViewServerLogs("my-server", "test-ns", true, false, 200, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cmd := mock.LastCommand()
		if !contains(cmd.Args, "-f") {
			t.Errorf("expected -f in args: %v", cmd.Args)
		}
	})
}

func TestServerManager_GetServerSuccess(t *testing.T) {
	mock := &core.MockExecutor{
		DefaultOutput: []byte("apiVersion: v1\nkind: MCPServer"),
	}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := newKubeTestServerManager(kubectl)

	err := mgr.GetServer("my-server", "test-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(mock.LastCommand().Args, "get") {
		t.Error("expected get command")
	}
}

func TestServerManager_CreateServerErrors(t *testing.T) {
	t.Run("rejects invalid image with control chars", func(t *testing.T) {
		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServer("my-server", "test-ns", "bad\nimage", "latest")
		if err == nil {
			t.Fatal("expected error for invalid image")
		}
	})

	t.Run("handles kubectl apply error", func(t *testing.T) {
		mock := &core.MockExecutor{DefaultRunErr: errors.New("apply failed")}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		err := mgr.CreateServer("my-server", "test-ns", "repo/image", "latest")
		if err == nil {
			t.Fatal("expected error when kubectl fails")
		}
	})
}

func TestServerManager_ViewServerLogsError(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := newKubeTestServerManager(kubectl)

	err := mgr.ViewServerLogs("bad;name", "test-ns", false, false, 200, "")
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	if len(mock.Commands) > 0 {
		t.Error("should not call kubectl with invalid name")
	}
}

func TestServerManager_ServerStatus(t *testing.T) {
	t.Run("handles empty servers list", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultOutput: []byte(""),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "No MCP servers found") {
			t.Errorf("expected 'No MCP servers found' message, got: %s", buf.String())
		}
	})

	t.Run("handles server list with provisioned registry", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "mcpserver") {
					cmd.OutputData = []byte("server1|image:tag|1|/path|true\n")
				} else if contains(spec.Args, "pods") {
					cmd.OutputData = []byte("NAME READY STATUS RESTARTS\npod-1 true Running 0\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "provisioned") {
			t.Errorf("expected 'provisioned' in output, got: %s", buf.String())
		}
	})

	t.Run("handles kubectl get mcpserver error", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultErr: errors.New("not found"),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err == nil {
			t.Fatal("expected error when kubectl fails")
		}
	})

	t.Run("handles get pods error gracefully", func(t *testing.T) {
		callCount := 0
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				callCount++
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "mcpserver") {
					cmd.OutputData = []byte("server1|image:tag|1|/path|false\n")
				} else if contains(spec.Args, "pods") {
					cmd.RunErr = errors.New("pods not found")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("handles whitespace-only lines in server output", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "mcpserver") {
					cmd.OutputData = []byte("server1|image:tag|1|/path|false\n   \n\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("handles pods command with no pods found", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				cmd := &core.MockCommand{Args: spec.Args}
				if contains(spec.Args, "mcpserver") {
					cmd.OutputData = []byte("server1|image:tag|1|/path|false\n")
				} else if contains(spec.Args, "pods") {
					cmd.OutputData = []byte("NAME READY STATUS RESTARTS\n")
				}
				return cmd
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestServerManager(kubectl)

		var buf bytes.Buffer
		setDefaultPrinterWriter(t, &buf)

		err := mgr.ServerStatus("test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func setDefaultPrinterWriter(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	orig := core.DefaultPrinter.Writer
	core.DefaultPrinter.Writer = w
	t.Cleanup(func() {
		core.DefaultPrinter.Writer = orig
	})
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
	})

	runErr := fn()
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close stdout pipe: %v", closeErr)
	}
	os.Stdout = orig
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read stdout: %v", readErr)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	if runErr != nil {
		t.Fatalf("captured function returned error: %v", runErr)
	}
	return string(out)
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func envVarValue(envVars []mcpv1alpha1.EnvVar, name string) string {
	for _, envVar := range envVars {
		if envVar.Name == name {
			return envVar.Value
		}
	}
	return ""
}

func TestBuildDeployServerSpecEnablesGateway(t *testing.T) {
	spec := buildDeployServerSpec("demo", "registry.example.com/team/demo", "v1.0.0", 2, 8088, 80)
	if spec.Gateway == nil || !spec.Gateway.Enabled {
		t.Fatalf("gateway = %#v, want enabled", spec.Gateway)
	}
	if spec.IngressPath != "/demo/mcp" {
		t.Fatalf("ingressPath = %q, want /demo/mcp", spec.IngressPath)
	}
	if len(spec.EnvVars) != 0 {
		t.Fatalf("envVars = %#v", spec.EnvVars)
	}
}

func TestApplyDeployMetadataDefaultsUsesSingleLocalMetadataServer(t *testing.T) {
	tmp := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := os.Mkdir(".mcp", 0o750); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(".mcp/servers.yaml", []byte(`version: v1
servers:
  - name: oauth-example-go-2025-11-25
    description: Workspace assistant metadata
    tools:
      - name: add
        description: Add two numbers.
        requiredTrust: low
        sideEffect: read
      - name: echo
        requiredTrust: low
        sideEffect: read
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	spec := buildDeployServerSpec("acme-tools", "registry.example.com/acme/go-example", "v0.1.0", 1, 8088, 80)
	if err := applyDeployMetadataDefaults(&spec, "acme-tools", "", ".mcp"); err != nil {
		t.Fatalf("applyDeployMetadataDefaults() error = %v", err)
	}
	if spec.Description != "Workspace assistant metadata" {
		t.Fatalf("description = %q", spec.Description)
	}
	if len(spec.Tools) != 2 || spec.Tools[0].Name != "add" || spec.Tools[0].SideEffect != "read" {
		t.Fatalf("tools = %#v", spec.Tools)
	}
	if spec.IngressPath != "/acme-tools/mcp" {
		t.Fatalf("deploy route should stay based on requested name, got %q", spec.IngressPath)
	}
}

func TestApplyDeployMetadataDefaultsMergesPortReplicasAndIngressHost(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, ".mcp"), 0o750); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".mcp", "servers.yaml"), []byte(`version: v1
servers:
  - name: payments
    route: /custom-payments/mcp
    publicPathPrefix: custom-payments
    ingressHost: mcp.example.com
    port: 9090
    replicas: 3
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	spec := buildDeployServerSpec("payments", "registry.example.com/acme/payments", "v1.0.0", 1, 8088, 80)
	if err := applyDeployMetadataDefaults(&spec, "payments", "", filepath.Join(tmp, ".mcp")); err != nil {
		t.Fatalf("applyDeployMetadataDefaults() error = %v", err)
	}
	if spec.Port != 9090 {
		t.Fatalf("port = %d, want 9090", spec.Port)
	}
	if spec.Replicas == nil || *spec.Replicas != 3 {
		t.Fatalf("replicas = %#v, want 3", spec.Replicas)
	}
	if spec.IngressPath != "/payments/mcp" || spec.PublicPathPrefix != "payments" || spec.IngressHost != "mcp.example.com" {
		t.Fatalf("metadata merge changed route unexpectedly: ingressPath=%q publicPathPrefix=%q ingressHost=%q", spec.IngressPath, spec.PublicPathPrefix, spec.IngressHost)
	}
	if got := envVarValue(spec.EnvVars, "MCP_PATH"); got != "" {
		t.Fatalf("MCP_PATH = %q, want it omitted for operator derivation", got)
	}
}

func TestApplyDeployMetadataDefaultsMergesGovernanceConfig(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, ".mcp"), 0o750); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".mcp", "servers.yaml"), []byte(`version: v1
servers:
  - name: payments
    auth:
      mode: header
    policy:
      mode: allow-list
      defaultDecision: deny
      enforceOn: call_tool
      policyVersion: v2
    session:
      required: true
    gateway:
      enabled: true
      port: 8091
      upstreamURL: http://127.0.0.1:9090
    envVars:
      - name: FEATURE_FLAG
        value: enabled
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	spec := buildDeployServerSpec("payments", "registry.example.com/acme/payments", "v1.0.0", 1, 8088, 80)
	if err := applyDeployMetadataDefaults(&spec, "payments", "", filepath.Join(tmp, ".mcp")); err != nil {
		t.Fatalf("applyDeployMetadataDefaults() error = %v", err)
	}
	if spec.Auth == nil || spec.Auth.Mode != mcpv1alpha1.AuthModeHeader {
		t.Fatalf("auth = %#v, want header", spec.Auth)
	}
	if spec.Policy == nil || spec.Policy.PolicyVersion != "v2" || spec.Policy.DefaultDecision != mcpv1alpha1.PolicyDecisionDeny {
		t.Fatalf("policy = %#v, want metadata policy", spec.Policy)
	}
	if spec.Session == nil || !spec.Session.Required {
		t.Fatalf("session = %#v, want required", spec.Session)
	}
	if spec.Gateway == nil || !spec.Gateway.Enabled || spec.Gateway.UpstreamURL != "http://127.0.0.1:9090" {
		t.Fatalf("gateway = %#v, want metadata gateway", spec.Gateway)
	}
	if got := envVarValue(spec.EnvVars, "FEATURE_FLAG"); got != "enabled" {
		t.Fatalf("FEATURE_FLAG = %q, want enabled", got)
	}
	if got := envVarValue(spec.EnvVars, "MCP_PATH"); got != "" {
		t.Fatalf("MCP_PATH = %q, want it omitted for operator derivation", got)
	}
}

func TestDeployServerWaitsForReadyStatus(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	apiCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams/core":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"team":{"slug":"core","name":"Core","namespace":"mcp-team-core"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/servers":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"server":{"name":"data-utility","namespace":"mcp-team-core","ready":"False","status":"PartiallyReady","age":"0s"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/servers":
			apiCalls++
			w.Header().Set("content-type", "application/json")
			if apiCalls == 1 {
				_, _ = w.Write([]byte(`{"servers":[{"name":"data-utility","namespace":"mcp-team-core","ready":"False","status":"PartiallyReady","age":"1s"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"servers":[{"name":"data-utility","namespace":"mcp-team-core","ready":"True","status":"Ready","age":"3s"}]}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer api.Close()

	t.Setenv(authfile.EnvAPIToken, "token-1")
	t.Setenv(authfile.EnvAPIURL, api.URL)
	origCfg := core.DefaultCLIConfig
	origPoll := serverDeployPollInterval
	core.DefaultCLIConfig = &core.CLIConfig{DeploymentTimeout: 2 * time.Second}
	serverDeployPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		core.DefaultCLIConfig = origCfg
		serverDeployPollInterval = origPoll
	})

	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewServerManager(kubectl, zap.NewNop())

	if err := mgr.DeployServer("data-utility", "", "core", "tenant", "data-utility", "latest", 1, 8088, 80, "", ".mcp", false); err != nil {
		t.Fatalf("DeployServer() error = %v", err)
	}
	if apiCalls < 2 {
		t.Fatalf("expected readiness polling, got %d inventory calls", apiCalls)
	}
}

func TestDeployServerTenantScopeDefaultsSingleTeamNamespace(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, ".mcp"), 0o750); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".mcp", "servers.yaml"), []byte(`version: v1
servers:
  - name: data-utility
    scope: tenant
    image: data-utility
    imageTag: latest
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	var appliedNamespace string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true,"principal":{"role":"user","teams":[{"slug":"core","namespace":"mcp-team-core"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/servers":
			var payload struct {
				Namespace string `json:"namespace"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			appliedNamespace = payload.Namespace
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"server":{"name":"data-utility","namespace":"mcp-team-core","ready":"True","status":"Ready","age":"0s"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/servers":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"servers":[{"name":"data-utility","namespace":"mcp-team-core","ready":"True","status":"Ready","age":"1s"}]}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer api.Close()

	t.Setenv(authfile.EnvAPIToken, "token-1")
	t.Setenv(authfile.EnvAPIURL, api.URL)
	origCfg := core.DefaultCLIConfig
	origPoll := serverDeployPollInterval
	core.DefaultCLIConfig = &core.CLIConfig{DeploymentTimeout: 2 * time.Second}
	serverDeployPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		core.DefaultCLIConfig = origCfg
		serverDeployPollInterval = origPoll
	})

	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	if err := mgr.DeployServer("data-utility", "", "", "", "", "latest", 1, 8088, 80, "", filepath.Join(tmp, ".mcp"), false); err != nil {
		t.Fatalf("DeployServer() error = %v", err)
	}
	if appliedNamespace != "mcp-team-core" {
		t.Fatalf("applied namespace = %q, want mcp-team-core", appliedNamespace)
	}
}

// A quickstart user whose .mcp metadata names the in-cluster registry DNS name
// (written by an older CLI without MCP_* env) must deploy the ref from the
// saved login's public registry; a login without a saved registry (Kind
// test-mode against localhost) must keep the ref unchanged.
func TestDeployServerRewritesClusterOnlyRegistryToSavedLoginRegistry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		registryHost string
		metaImage    string
		wantImage    string
	}{
		{name: "public platform login", registryHost: "registry.example.org", metaImage: "registry.registry.svc.cluster.local:5000/core/data-utility", wantImage: "registry.example.org/core/data-utility"},
		{name: "placeholder registry", registryHost: "registry.example.org", metaImage: "registry.local/core/data-utility", wantImage: "registry.example.org/core/data-utility"},
		{name: "external registry kept", registryHost: "registry.example.org", metaImage: "ghcr.io/core/data-utility", wantImage: "ghcr.io/core/data-utility"},
		{name: "local login keeps cluster registry", registryHost: "", metaImage: "registry.registry.svc.cluster.local:5000/core/data-utility", wantImage: "registry.registry.svc.cluster.local:5000/core/data-utility"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"MCP_REGISTRY_INGRESS_HOST", "MCP_REGISTRY_HOST", "MCP_REGISTRY_ENDPOINT", "MCP_PLATFORM_DOMAIN", authfile.EnvAPIToken, authfile.EnvAPIURL} {
				t.Setenv(key, "")
			}
			tmp := t.TempDir()
			if err := os.Mkdir(filepath.Join(tmp, ".mcp"), 0o750); err != nil {
				t.Fatalf("mkdir metadata: %v", err)
			}
			if err := os.WriteFile(filepath.Join(tmp, ".mcp", "servers.yaml"), []byte(`version: v1
servers:
  - name: data-utility
    scope: tenant
    image: `+tc.metaImage+`
    imageTag: v1
`), 0o600); err != nil {
				t.Fatalf("write metadata: %v", err)
			}
			var appliedImage string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
					w.Header().Set("content-type", "application/json")
					_, _ = w.Write([]byte(`{"authenticated":true,"principal":{"role":"user","teams":[{"slug":"core","namespace":"mcp-team-core"}]}}`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/servers":
					var payload struct {
						Spec struct {
							Image string `json:"image"`
						} `json:"spec"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode payload: %v", err)
					}
					appliedImage = payload.Spec.Image
					w.Header().Set("content-type", "application/json")
					_, _ = w.Write([]byte(`{"server":{"name":"data-utility","namespace":"mcp-team-core","ready":"True","status":"Ready","age":"0s"}}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/servers":
					w.Header().Set("content-type", "application/json")
					_, _ = w.Write([]byte(`{"servers":[{"name":"data-utility","namespace":"mcp-team-core","ready":"True","status":"Ready","age":"1s"}]}`))
				default:
					t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer api.Close()
			configDir := t.TempDir()
			t.Setenv("MCP_RUNTIME_CONFIG_DIR", configDir)
			if err := authfile.SaveProfile(filepath.Join(configDir, "config.json"), "me", authfile.CredentialAccount{
				APIBaseURL:   api.URL,
				Token:        "token-1",
				RegistryHost: tc.registryHost,
			}); err != nil {
				t.Fatalf("save profile: %v", err)
			}
			origCfg := core.DefaultCLIConfig
			origPoll := serverDeployPollInterval
			core.DefaultCLIConfig = &core.CLIConfig{DeploymentTimeout: 2 * time.Second}
			serverDeployPollInterval = 5 * time.Millisecond
			t.Cleanup(func() {
				core.DefaultCLIConfig = origCfg
				serverDeployPollInterval = origPoll
			})

			mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
			if err := mgr.DeployServer("data-utility", "", "", "", "", "latest", 1, 8088, 80, "", filepath.Join(tmp, ".mcp"), false); err != nil {
				t.Fatalf("DeployServer() error = %v", err)
			}
			if appliedImage != tc.wantImage {
				t.Fatalf("applied image = %q, want %q", appliedImage, tc.wantImage)
			}
		})
	}
}

func TestDeployServerTenantScopeRequiresTeamWhenAmbiguous(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/me" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authenticated":true,"principal":{"role":"user","teams":[{"slug":"one","namespace":"mcp-team-one"},{"slug":"two","namespace":"mcp-team-two"}]}}`))
	}))
	defer api.Close()

	t.Setenv(authfile.EnvAPIToken, "token-1")
	t.Setenv(authfile.EnvAPIURL, api.URL)
	mgr := NewServerManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	err := mgr.DeployServer("data-utility", "", "", "tenant", "data-utility", "latest", 1, 8088, 80, "", ".mcp", false)
	if err == nil || !strings.Contains(err.Error(), "--team") {
		t.Fatalf("error = %v, want --team guidance", err)
	}
}

func TestDeployServerFailsWhenServerDoesNotBecomeReady(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams/core":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"team":{"slug":"core","name":"Core","namespace":"mcp-team-core"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/servers":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"server":{"name":"data-utility","namespace":"mcp-team-core","ready":"False","status":"PartiallyReady","age":"0s"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/servers":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"servers":[{"name":"data-utility","namespace":"mcp-team-core","ready":"False","status":"PartiallyReady","age":"1s"}]}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer api.Close()

	t.Setenv(authfile.EnvAPIToken, "token-1")
	t.Setenv(authfile.EnvAPIURL, api.URL)
	origCfg := core.DefaultCLIConfig
	origPoll := serverDeployPollInterval
	core.DefaultCLIConfig = &core.CLIConfig{DeploymentTimeout: 20 * time.Millisecond}
	serverDeployPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		core.DefaultCLIConfig = origCfg
		serverDeployPollInterval = origPoll
	})

	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := NewServerManager(kubectl, zap.NewNop())

	err := mgr.DeployServer("data-utility", "", "core", "tenant", "data-utility", "latest", 1, 8088, 80, "", ".mcp", false)
	if err == nil {
		t.Fatal("expected deploy readiness error")
	}
	if !strings.Contains(err.Error(), "did not become ready") || !strings.Contains(err.Error(), "PartiallyReady") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeployImageRefsEquivalentAcceptsScopedDisplayRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		got      string
		want     bool
	}{
		{
			name:     "short_input_matches_tenant_scoped_inventory",
			expected: "assist",
			got:      "techcorp/assist",
			want:     true,
		},
		{
			name:     "public_ref_matches_sanitized_inventory",
			expected: "registry.mcpruntime.org/techcorp/assist:latest",
			got:      "techcorp/assist",
			want:     true,
		},
		{
			name:     "different_image_name_fails",
			expected: "assist",
			got:      "techcorp/other",
			want:     false,
		},
		{
			name:     "same_scoped_repo_matches",
			expected: "techcorp/assist",
			got:      "techcorp/assist",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := deployImageRefsEquivalent(tt.expected, tt.got); got != tt.want {
				t.Fatalf("deployImageRefsEquivalent(%q, %q) = %v, want %v", tt.expected, tt.got, got, tt.want)
			}
		})
	}
}

func TestSelectDeployMetadataNameMatching(t *testing.T) {
	writeRegistry := func(t *testing.T, dir string, servers []metadata.ServerMetadata) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		data, err := yaml.Marshal(metadata.RegistryFile{Version: "v1", Servers: servers})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "servers.yaml"), data, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	t.Run("exact match always works", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".mcp")
		writeRegistry(t, dir, []metadata.ServerMetadata{{Name: "payments", Image: "registry.example.com/acme/payments"}})
		m, err := selectDeployMetadata("payments", "", dir)
		if err != nil {
			t.Fatalf("selectDeployMetadata(payments) error = %v", err)
		}
		if m.Name != "payments" {
			t.Fatalf("got name %q, want payments", m.Name)
		}
	})

	t.Run("single server fallback when name does not match", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".mcp")
		writeRegistry(t, dir, []metadata.ServerMetadata{{Name: "example-python-2025-11-25", Image: "example-python-2025-11-25"}})
		m, err := selectDeployMetadata("data-utility", "", dir)
		if err != nil {
			t.Fatalf("single-server fallback error = %v", err)
		}
		if m.Name != "example-python-2025-11-25" {
			t.Fatalf("got name %q, want example-python-2025-11-25", m.Name)
		}
	})

	t.Run("multi-server registry requires exact match", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".mcp")
		writeRegistry(t, dir, []metadata.ServerMetadata{
			{Name: "payments", Image: "registry.example.com/acme/payments"},
			{Name: "invoices", Image: "registry.example.com/acme/invoices"},
		})
		if _, err := selectDeployMetadata("typo", "", dir); err == nil {
			t.Fatal("expected error for mismatched name with multiple servers")
		}
	})
}
