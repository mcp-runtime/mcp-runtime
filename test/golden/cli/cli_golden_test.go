package cli

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var (
	update     = flag.Bool("update", false, "update CLI golden files")
	binaryOnce sync.Once
	binaryPath string
)

func TestCLIHelpGoldens(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		golden string
	}{
		{name: "root_help", args: []string{"--help"}, golden: "mcp-runtime_help.golden"},
		{name: "access_help", args: []string{"access", "--help"}, golden: "mcp-runtime_access_help.golden"},
		{name: "access_grant_help", args: []string{"access", "grant", "--help"}, golden: "mcp-runtime_access_grant_help.golden"},
		{name: "access_grant_revoke_sessions_help", args: []string{"access", "grant", "revoke-sessions", "--help"}, golden: "mcp-runtime_access_grant_revoke_sessions_help.golden"},
		{name: "access_grant_init_help", args: []string{"access", "grant", "init", "--help"}, golden: "mcp-runtime_access_grant_init_help.golden"},
		{name: "access_session_help", args: []string{"access", "session", "--help"}, golden: "mcp-runtime_access_session_help.golden"},
		{name: "access_session_init_help", args: []string{"access", "session", "init", "--help"}, golden: "mcp-runtime_access_session_init_help.golden"},
		{name: "access_explain_help", args: []string{"access", "explain", "--help"}, golden: "mcp-runtime_access_explain_help.golden"},
		{name: "agent_help", args: []string{"agent", "--help"}, golden: "mcp-runtime_agent_help.golden"},
		{name: "agent_create_help", args: []string{"agent", "create", "--help"}, golden: "mcp-runtime_agent_create_help.golden"},
		{name: "agent_list_help", args: []string{"agent", "list", "--help"}, golden: "mcp-runtime_agent_list_help.golden"},
		{name: "agent_get_help", args: []string{"agent", "get", "--help"}, golden: "mcp-runtime_agent_get_help.golden"},
		{name: "agent_rename_help", args: []string{"agent", "rename", "--help"}, golden: "mcp-runtime_agent_rename_help.golden"},
		{name: "agent_deactivate_help", args: []string{"agent", "deactivate", "--help"}, golden: "mcp-runtime_agent_deactivate_help.golden"},
		{name: "agent_reactivate_help", args: []string{"agent", "reactivate", "--help"}, golden: "mcp-runtime_agent_reactivate_help.golden"},
		{name: "adapter_help", args: []string{"adapter", "--help"}, golden: "mcp-runtime_adapter_help.golden"},
		{name: "adapter_proxy_help", args: []string{"adapter", "proxy", "--help"}, golden: "mcp-runtime_adapter_proxy_help.golden"},
		{name: "adapter_stdio_help", args: []string{"adapter", "stdio", "--help"}, golden: "mcp-runtime_adapter_stdio_help.golden"},
		{name: "auth_help", args: []string{"auth", "--help"}, golden: "mcp-runtime_auth_help.golden"},
		{name: "auth_login_help", args: []string{"auth", "login", "--help"}, golden: "mcp-runtime_auth_login_help.golden"},
		{name: "auth_logout_help", args: []string{"auth", "logout", "--help"}, golden: "mcp-runtime_auth_logout_help.golden"},
		{name: "auth_status_help", args: []string{"auth", "status", "--help"}, golden: "mcp-runtime_auth_status_help.golden"},
		{name: "catalog_help", args: []string{"catalog", "--help"}, golden: "mcp-runtime_catalog_help.golden"},
		{name: "catalog_tools_help", args: []string{"catalog", "tools", "--help"}, golden: "mcp-runtime_catalog_tools_help.golden"},
		{name: "catalog_tool_help", args: []string{"catalog", "tool", "--help"}, golden: "mcp-runtime_catalog_tool_help.golden"},
		{name: "status_help", args: []string{"status", "--help"}, golden: "mcp-runtime_status_help.golden"},
		{name: "sentinel_help", args: []string{"sentinel", "--help"}, golden: "mcp-runtime_sentinel_help.golden"},
		{name: "sentinel_logs_help", args: []string{"sentinel", "logs", "--help"}, golden: "mcp-runtime_sentinel_logs_help.golden"},
		{name: "sentinel_port_forward_help", args: []string{"sentinel", "port-forward", "--help"}, golden: "mcp-runtime_sentinel_port_forward_help.golden"},
		{name: "server_help", args: []string{"server", "--help"}, golden: "mcp-runtime_server_help.golden"},
		{name: "server_apply_help", args: []string{"server", "apply", "--help"}, golden: "mcp-runtime_server_apply_help.golden"},
		{name: "server_init_help", args: []string{"server", "init", "--help"}, golden: "mcp-runtime_server_init_help.golden"},
		{name: "server_list_help", args: []string{"server", "list", "--help"}, golden: "mcp-runtime_server_list_help.golden"},
		{name: "server_get_help", args: []string{"server", "get", "--help"}, golden: "mcp-runtime_server_get_help.golden"},
		{name: "server_create_help", args: []string{"server", "create", "--help"}, golden: "mcp-runtime_server_create_help.golden"},
		{name: "server_connect_config_help", args: []string{"server", "connect-config", "--help"}, golden: "mcp-runtime_server_connect_config_help.golden"},
		{name: "server_generate_help", args: []string{"server", "generate", "--help"}, golden: "mcp-runtime_server_generate_help.golden"},
		{name: "server_delete_help", args: []string{"server", "delete", "--help"}, golden: "mcp-runtime_server_delete_help.golden"},
		{name: "server_export_help", args: []string{"server", "export", "--help"}, golden: "mcp-runtime_server_export_help.golden"},
		{name: "server_logs_help", args: []string{"server", "logs", "--help"}, golden: "mcp-runtime_server_logs_help.golden"},
		{name: "server_patch_help", args: []string{"server", "patch", "--help"}, golden: "mcp-runtime_server_patch_help.golden"},
		{name: "server_policy_help", args: []string{"server", "policy", "--help"}, golden: "mcp-runtime_server_policy_help.golden"},
		{name: "server_policy_inspect_help", args: []string{"server", "policy", "inspect", "--help"}, golden: "mcp-runtime_server_policy_inspect_help.golden"},
		{name: "server_status_help", args: []string{"server", "status", "--help"}, golden: "mcp-runtime_server_status_help.golden"},
		{name: "server_deploy_help", args: []string{"server", "deploy", "--help"}, golden: "mcp-runtime_server_deploy_help.golden"},
		{name: "server_build_help", args: []string{"server", "build", "--help"}, golden: "mcp-runtime_server_build_help.golden"},
		{name: "server_build_image_help", args: []string{"server", "build", "image", "--help"}, golden: "mcp-runtime_server_build_image_help.golden"},
		{name: "server_push_help", args: []string{"server", "push", "--help"}, golden: "mcp-runtime_server_push_help.golden"},
		{name: "registry_help", args: []string{"registry", "--help"}, golden: "mcp-runtime_registry_help.golden"},
		{name: "registry_status_help", args: []string{"registry", "status", "--help"}, golden: "mcp-runtime_registry_status_help.golden"},
		{name: "registry_info_help", args: []string{"registry", "info", "--help"}, golden: "mcp-runtime_registry_info_help.golden"},
		{name: "registry_provision_help", args: []string{"registry", "provision", "--help"}, golden: "mcp-runtime_registry_provision_help.golden"},
		{name: "bootstrap_help", args: []string{"bootstrap", "--help"}, golden: "mcp-runtime_bootstrap_help.golden"},
		{name: "setup_help", args: []string{"setup", "--help"}, golden: "mcp-runtime_setup_help.golden"},
		{name: "update_help", args: []string{"update", "--help"}, golden: "mcp-runtime_update_help.golden"},
		{name: "cluster_help", args: []string{"cluster", "--help"}, golden: "mcp-runtime_cluster_help.golden"},
		{name: "cluster_init_help", args: []string{"cluster", "init", "--help"}, golden: "mcp-runtime_cluster_init_help.golden"},
		{name: "cluster_status_help", args: []string{"cluster", "status", "--help"}, golden: "mcp-runtime_cluster_status_help.golden"},
		{name: "cluster_config_help", args: []string{"cluster", "config", "--help"}, golden: "mcp-runtime_cluster_config_help.golden"},
		{name: "cluster_provision_help", args: []string{"cluster", "provision", "--help"}, golden: "mcp-runtime_cluster_provision_help.golden"},
		{name: "cluster_cert_help", args: []string{"cluster", "cert", "--help"}, golden: "mcp-runtime_cluster_cert_help.golden"},
		{name: "cluster_cert_status_help", args: []string{"cluster", "cert", "status", "--help"}, golden: "mcp-runtime_cluster_cert_status_help.golden"},
		{name: "cluster_cert_apply_help", args: []string{"cluster", "cert", "apply", "--help"}, golden: "mcp-runtime_cluster_cert_apply_help.golden"},
		{name: "cluster_cert_wait_help", args: []string{"cluster", "cert", "wait", "--help"}, golden: "mcp-runtime_cluster_cert_wait_help.golden"},
		{name: "cluster_doctor_help", args: []string{"cluster", "doctor", "--help"}, golden: "mcp-runtime_cluster_doctor_help.golden"},
		{name: "cluster_diagnostics_help", args: []string{"cluster", "diagnostics", "--help"}, golden: "mcp-runtime_cluster_diagnostics_help.golden"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := runCLI(t, tc.args...)
			goldenPath := filepath.Join(testdataDir(t), tc.golden)

			if *update {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("failed to update golden %s: %v", tc.golden, err)
				}
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("failed to read golden %s: %v", tc.golden, err)
			}

			if diff := cmp.Diff(string(want), string(got)); diff != "" {
				t.Fatalf("CLI output mismatch for %s (-want +got):\n%s", tc.golden, diff)
			}
		})
	}
}

func runCLI(t *testing.T, args ...string) []byte {
	t.Helper()

	// Ensure binary is built once per test run
	binaryOnce.Do(func() {
		root := repoRoot(t)
		binaryPath = filepath.Join(root, "bin", "mcp-runtime")

		// Always build to ensure the binary matches the current GOOS/GOARCH.
		t.Logf("Building binary at %s", binaryPath)
		if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
			t.Fatalf("failed to create bin directory: %v", err)
		}

		// #nosec G204 -- test code with trusted paths
		buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/mcp-runtime")
		buildCmd.Dir = root
		buildCmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".gocache"))
		if err := buildCmd.Run(); err != nil {
			t.Fatalf("failed to build binary: %v", err)
		}
	})

	// Execute binary with args
	// #nosec G204 -- test code with trusted binary path
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI failed for args %v: %v\nOutput:\n%s", args, err, out)
	}

	return out
}

func testdataDir(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to determine caller")
	}

	return filepath.Join(filepath.Dir(filename), "testdata")
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to determine caller")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
