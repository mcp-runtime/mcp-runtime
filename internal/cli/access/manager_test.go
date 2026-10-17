package access

import (
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

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/authfile"
)

func newKubeTestAccessManager(kubectl *core.KubectlClient) *AccessManager {
	mgr := NewAccessManager(kubectl, zap.NewNop())
	mgr.useKube = true
	return mgr
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func TestInitGrantManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "grant.yaml")
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name:        "payments-globex-cursor",
		Namespace:   "mcp-team-acme",
		Server:      "payments",
		TeamID:      "team-globex",
		AgentID:     "cursor",
		Trust:       "medium",
		Mode:        "monitor",
		SideEffects: []string{"read", "write", "read"},
		Tools:       []string{"list_invoices", "refund_invoice", "list_invoices"},
		ToolRules:   []string{"delete_invoice:deny:high"},
		Output:      output,
	})
	if err != nil {
		t.Fatalf("InitGrantManifest() error = %v", err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"kind: MCPAccessGrant",
		"name: payments-globex-cursor",
		"namespace: mcp-team-acme",
		"name: payments",
		"teamID: team-globex",
		"agentID: cursor",
		"maxTrust: medium",
		"mode: monitor",
		"- read",
		"- write",
		"name: list_invoices",
		"name: refund_invoice",
		"name: delete_invoice",
		"decision: deny",
		"requiredTrust: high",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}

	err = mgr.InitGrantManifest(accessManifestInitOptions{Name: "payments-globex-cursor", Namespace: "mcp-team-acme", Server: "payments", TeamID: "team-globex", Output: output})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("duplicate output error = %v, want --force guidance", err)
	}
}

func TestInitGrantManifestRejectsUnknownMode(t *testing.T) {
	_, err := buildAccessManifest(accessManifestInitOptions{Kind: "MCPAccessGrant", Name: "grant", Namespace: "servers", Server: "demo", HumanID: "human", Trust: "low", Mode: "observe"})
	if err == nil || !strings.Contains(err.Error(), "--mode must be enforce or monitor") {
		t.Fatalf("unknown mode error=%v", err)
	}
}

func TestInitGrantManifestRejectsDuplicateToolRule(t *testing.T) {
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name:        "payments-globex-cursor",
		Namespace:   "mcp-team-acme",
		Server:      "payments",
		TeamID:      "team-globex",
		SideEffects: []string{"read"},
		Tools:       []string{"list_invoices"},
		ToolRules:   []string{"list_invoices:deny:high"},
		Output:      filepath.Join(t.TempDir(), "grant.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate tool rule") {
		t.Fatalf("error = %v, want duplicate tool rule", err)
	}
}

func TestInitGrantManifestRejectsInvalidToolRule(t *testing.T) {
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name:        "payments-globex-cursor",
		Namespace:   "mcp-team-acme",
		Server:      "payments",
		TeamID:      "team-globex",
		SideEffects: []string{"read"},
		ToolRules:   []string{"refund_invoice:audit:high"},
		Output:      filepath.Join(t.TempDir(), "grant.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "allow or deny") {
		t.Fatalf("error = %v, want decision validation", err)
	}
}

func TestInitGrantManifestRejectsUnsupportedTrustAlias(t *testing.T) {
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name:        "payments-globex-cursor",
		Namespace:   "mcp-team-acme",
		Server:      "payments",
		TeamID:      "team-globex",
		Trust:       "mid",
		SideEffects: []string{"read"},
		Output:      filepath.Join(t.TempDir(), "grant.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "low|medium|high") {
		t.Fatalf("error = %v, want supported trust levels", err)
	}
}

func TestInitSessionManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "session.yaml")
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitSessionManifest(accessManifestInitOptions{
		Name:               "cursor-session",
		Namespace:          "mcp-team-acme",
		Server:             "payments",
		HumanID:            "user-1",
		AgentID:            "cursor",
		Trust:              "low",
		UpstreamSecretName: "upstream-token",
		UpstreamSecretKey:  "token",
		ExpiresAt:          expiresAt,
		Revoked:            true,
		Output:             output,
	})
	if err != nil {
		t.Fatalf("InitSessionManifest() error = %v", err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"kind: MCPAgentSession",
		"name: cursor-session",
		"namespace: mcp-team-acme",
		"humanID: user-1",
		"agentID: cursor",
		"consentedTrust: low",
		"expiresAt: \"" + expiresAt + "\"",
		"revoked: true",
		"upstreamTokenSecretRef:",
		"name: upstream-token",
		"key: token",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestInitGrantManifestExpiresIn(t *testing.T) {
	output := filepath.Join(t.TempDir(), "grant.yaml")
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	if err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name: "temporary-debug", Namespace: "mcp-team-acme", Server: "payments",
		TeamID: "team-globex", AgentID: "incident-helper", Tools: []string{"inspect_trace"},
		ExpiresIn: "4h", Output: output,
	}); err != nil {
		t.Fatalf("InitGrantManifest() error = %v", err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(body)
	for _, want := range []string{"kind: MCPAccessGrant", "teamID: team-globex", "inspect_trace", "expiresAt:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestNormalizeExpiryRejectsPastTimestamp(t *testing.T) {
	_, err := normalizeSessionExpiry(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "")
	if err == nil || !strings.Contains(err.Error(), "must be in the future") {
		t.Fatalf("normalizeSessionExpiry error = %v, want future-expiry validation", err)
	}
}

func TestInitSessionManifestExpiresIn(t *testing.T) {
	output := filepath.Join(t.TempDir(), "session.yaml")
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitSessionManifest(accessManifestInitOptions{
		Name:      "cursor-session",
		Namespace: "mcp-team-acme",
		Server:    "payments",
		AgentID:   "cursor",
		ExpiresIn: "1h",
		Output:    output,
	})
	if err != nil {
		t.Fatalf("InitSessionManifest() error = %v", err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "expiresAt:") {
		t.Fatalf("manifest missing expiresAt:\n%s", text)
	}
}

func TestInitSessionManifestRejectsConflictingExpiry(t *testing.T) {
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())

	err := mgr.InitSessionManifest(accessManifestInitOptions{
		Name:      "cursor-session",
		Namespace: "mcp-team-acme",
		Server:    "payments",
		AgentID:   "cursor",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		ExpiresIn: "1h",
		Output:    filepath.Join(t.TempDir(), "session.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "either --expires-at or --expires-in") {
		t.Fatalf("error = %v, want expiry conflict", err)
	}
}

func TestInitAccessManifestRequiresSubject(t *testing.T) {
	mgr := NewAccessManager(core.NewTestKubectlClient(&core.MockExecutor{}), zap.NewNop())
	err := mgr.InitGrantManifest(accessManifestInitOptions{
		Name:      "grant-a",
		Namespace: "mcp-team-acme",
		Server:    "payments",
		Output:    filepath.Join(t.TempDir(), "grant.yaml"),
	})
	if err == nil || !strings.Contains(err.Error(), "--human-id") {
		t.Fatalf("error = %v, want subject guidance", err)
	}
}

func TestAccessManager_ListAccessResources(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := newKubeTestAccessManager(kubectl)

	if err := mgr.ListAccessResources(GrantResource, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	for _, want := range []string{"get", GrantResource, "-A"} {
		if !contains(cmd.Args, want) {
			t.Fatalf("expected %q in args, got %v", want, cmd.Args)
		}
	}
}

func TestAccessManager_ModeSelection(t *testing.T) {
	t.Run("uses platform API by default when logged in", func(t *testing.T) {
		apiCalls := 0
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			if r.URL.Path != "/api/v1/runtime/grants" {
				t.Fatalf("unexpected platform path %q", r.URL.Path)
			}
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"grants":[{"name":"grant-a","namespace":"mcp-team-acme","serverRef":{"name":"demo"},"disabled":false}]}`))
		}))
		defer api.Close()
		t.Setenv(authfile.EnvAPIToken, "token-1")
		t.Setenv(authfile.EnvAPIURL, api.URL)
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		mock := &core.MockExecutor{}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := NewAccessManager(kubectl, zap.NewNop())

		out := captureStdout(t, func() error {
			return mgr.ListAccessResources(GrantResource, "", true)
		})
		if !strings.Contains(out, "grant-a") {
			t.Fatalf("platform access list output = %q, want grant name", out)
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
		mgr := NewAccessManager(kubectl, zap.NewNop())

		err := mgr.ListAccessResources(GrantResource, "", true)
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

	t.Run("explicit kube forbidden error explains admin boundary", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultRunErr: errors.New(`Error from server (Forbidden): mcpaccessgrants.mcpruntime.org is forbidden: User "alice" cannot list resource "mcpaccessgrants"`),
		}
		kubectl := core.NewTestKubectlClient(mock)
		mgr := newKubeTestAccessManager(kubectl)

		err := mgr.ListAccessResources(GrantResource, "", true)
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

func TestAccessManager_GetAccessResource(t *testing.T) {
	mock := &core.MockExecutor{}
	kubectl := core.NewTestKubectlClient(mock)
	mgr := newKubeTestAccessManager(kubectl)

	if err := mgr.GetAccessResource(GrantResource, "grant-a", "team-a"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	for _, want := range []string{"get", GrantResource, "grant-a", "-n", "team-a", "-o", "yaml"} {
		if !contains(cmd.Args, want) {
			t.Fatalf("expected %q in args, got %v", want, cmd.Args)
		}
	}
}

func TestAccessManager_ApplyAccessResource(t *testing.T) {
	var applyCmd *core.MockCommand
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			applyCmd = &core.MockCommand{Args: spec.Args}
			return applyCmd
		},
	}
	kubectl, err := core.NewKubectlClient(mock)
	if err != nil {
		t.Fatalf("NewKubectlClient() error = %v", err)
	}
	mgr := newKubeTestAccessManager(kubectl)

	tmpFile, err := os.CreateTemp("", "access-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString("apiVersion: v1\nkind: ConfigMap\n"); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	if err := mgr.ApplyAccessResource(tmpFile.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := mock.LastCommand()
	if !contains(cmd.Args, "apply") || !contains(cmd.Args, "-f") || !contains(cmd.Args, "-") {
		t.Fatalf("expected apply -f - args, got %v", cmd.Args)
	}
	captured, err := io.ReadAll(applyCmd.StdinR)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(captured) != "apiVersion: v1\nkind: ConfigMap\n" {
		t.Fatalf("unexpected stdin: %q", string(captured))
	}
}

func TestAccessManager_ToggleAccessResource(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		wantJSON string
	}{
		{name: "disable grant", resource: GrantResource, wantJSON: `"disabled":true`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &core.MockExecutor{}
			kubectl := core.NewTestKubectlClient(mock)
			mgr := newKubeTestAccessManager(kubectl)

			if err := mgr.ToggleAccessResource(tt.resource, "obj-a", "team-a", true); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			cmd := mock.LastCommand()
			for _, want := range []string{"patch", tt.resource, "obj-a", "-n", "team-a", "--type", "merge", "--patch"} {
				if !contains(cmd.Args, want) {
					t.Fatalf("expected %q in args, got %v", want, cmd.Args)
				}
			}
			patchIndex := -1
			for i, arg := range cmd.Args {
				if arg == "--patch" && i+1 < len(cmd.Args) {
					patchIndex = i + 1
					break
				}
			}
			if patchIndex == -1 {
				t.Fatalf("expected --patch argument, got %v", cmd.Args)
			}
			if !strings.Contains(cmd.Args[patchIndex], tt.wantJSON) {
				t.Fatalf("expected patch payload %q, got %q", tt.wantJSON, cmd.Args[patchIndex])
			}
		})
	}
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
