package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/authfile"
)

func TestBuildImage(t *testing.T) {
	logger := zap.NewNop()

	t.Run("builds_image_successfully", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: test-server
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "test-server", "Dockerfile", metadataFile, ".", "test-registry", "test-tag", "", ".")
		if err != nil {
			t.Fatalf("failed to build image: %v", err)
		}

		if !mock.HasCommand("docker") {
			t.Error("expected docker command to be executed")
		}

		last := mock.LastCommand()
		if last.Name != "docker" {
			t.Errorf("expected docker command, got %q", last.Name)
		}

		expectedArgs := []string{"build", "--platform", defaultDockerBuildPlatform, "-f", "Dockerfile", "-t", "test-registry/test-server:test-tag", "."}
		if !equalStringSlices(last.Args, expectedArgs) {
			t.Errorf("docker args = %v, want %v", last.Args, expectedArgs)
		}
	})

	t.Run("returns_error_after_build_when_metadata_missing", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		err := buildImage(context.Background(), logger, "missing-server", "Dockerfile", "", tmp, "test-registry", "test-tag", "", ".")
		if err == nil {
			t.Fatal("expected error when metadata file not found for server name")
		}
		if !errors.Is(err, core.ErrMetadataFileNotFound) {
			t.Fatalf("expected ErrMetadataFileNotFound, got %v", err)
		}
	})

	t.Run("returns_error_on_build_failure", func(t *testing.T) {
		mock := &core.MockExecutor{
			DefaultRunErr: errors.New("docker build failed"),
		}
		defer core.SwapExecExecutor(mock)()

		err := buildImage(context.Background(), logger, "test-server", "Dockerfile", "", ".", "test-registry", "test-tag", "", ".")
		if err == nil {
			t.Error("expected error when docker build fails")
		}
	})

	t.Run("uses_git_tag_when_tag_empty", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if spec.Name == "git" {
					return &core.MockCommand{OutputData: []byte("abc1234\n")}
				}
				return &core.MockCommand{}
			},
		}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: my-server
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "my-server", "Dockerfile", metadataFile, ".", "registry.io", "", "", ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				found := false
				for _, arg := range cmd.Args {
					if arg == "registry.io/my-server:abc1234" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected image tag with git SHA, got args: %v", cmd.Args)
				}
			}
		}
	})

	t.Run("uses_metadata_scope_in_image_repository", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: public-server
    scope: public
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "public-server", "Dockerfile", metadataFile, ".", "registry.example.com", "v1", "", ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				if !contains(cmd.Args, "registry.example.com/public/public-server:v1") {
					t.Fatalf("docker args = %v, want public-scoped tag", cmd.Args)
				}
				return
			}
		}
		t.Fatal("expected docker command")
	})

	t.Run("uses_explicit_docker_platform", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: arm-server
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "arm-server", "Dockerfile", metadataFile, ".", "registry.example.com", "v1", "linux/arm64", ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				if !contains(cmd.Args, "--platform") || !contains(cmd.Args, "linux/arm64") {
					t.Fatalf("docker args = %v, want explicit platform", cmd.Args)
				}
				return
			}
		}
		t.Fatal("expected docker command")
	})

	t.Run("uses_tenant_scope_in_image_repository", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/auth/me" {
				t.Fatalf("unexpected platform path %q", r.URL.Path)
			}
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true,"principal":{"role":"user","namespace":"mcp-team-acme","teams":[{"slug":"acme","namespace":"mcp-team-acme"}]}}`))
		}))
		defer api.Close()
		t.Setenv("MCP_PLATFORM_API_TOKEN", "token-1")
		t.Setenv("MCP_PLATFORM_API_URL", api.URL)
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: tenant-server
    scope: tenant
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "tenant-server", "Dockerfile", metadataFile, ".", "registry.example.com", "v1", "", ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				if !contains(cmd.Args, "registry.example.com/acme/tenant-server:v1") {
					t.Fatalf("docker args = %v, want tenant-scoped tag", cmd.Args)
				}
				return
			}
		}
		t.Fatal("expected docker command")
	})

	t.Run("returns_error_before_build_when_explicit_metadata_invalid", func(t *testing.T) {
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: public-server
    scope: unsupported
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "public-server", "Dockerfile", metadataFile, ".", "registry.example.com", "v1", "", ".")
		if err == nil {
			t.Fatal("expected metadata load error")
		}
		if !errors.Is(err, core.ErrLoadMetadataFailed) {
			t.Fatalf("expected ErrLoadMetadataFailed, got %v", err)
		}
		if mock.HasCommand("docker") {
			t.Fatal("docker should not run when explicit metadata file is invalid")
		}
	})

	// Quickstart laptop user: saved auth login profile, no MCP_* env, no
	// reachable kubeconfig. The tag and metadata must name the profile's
	// public registry, not the in-cluster registry DNS name.
	t.Run("uses_saved_login_registry_host_without_env", func(t *testing.T) {
		for _, key := range []string{"MCP_REGISTRY_INGRESS_HOST", "MCP_REGISTRY_HOST", "MCP_REGISTRY_ENDPOINT", "MCP_PLATFORM_DOMAIN", "MCP_PLATFORM_API_URL", "MCP_PLATFORM_API_TOKEN", "MCP_RUNTIME_TEST_MODE"} {
			t.Setenv(key, "")
		}
		origConfig := core.DefaultCLIConfig
		defer func() { core.DefaultCLIConfig = origConfig }()
		core.DefaultCLIConfig = &core.CLIConfig{RegistryEndpoint: core.DefaultRegistryEndpoint, RegistryIngressHost: core.DefaultRegistryIngressHost, RegistryPort: 5000}
		kubectlMock := &core.MockExecutor{
			CommandFunc: func(core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("no kubeconfig")}
			},
		}
		defer core.SwapDefaultKubectlClient(core.NewTestKubectlClient(kubectlMock))()
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true,"principal":{"role":"user","namespace":"mcp-team-acme","teams":[{"slug":"acme","namespace":"mcp-team-acme"}]}}`))
		}))
		defer api.Close()
		configDir := t.TempDir()
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", configDir)
		if err := authfile.SaveProfile(filepath.Join(configDir, "config.json"), "me", authfile.CredentialAccount{
			APIBaseURL:   api.URL,
			Token:        "token-1",
			RegistryHost: "registry.example.org",
		}); err != nil {
			t.Fatalf("save profile: %v", err)
		}

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: oauth-example-go-2025-11-25
    route: /oauth-example-go-2025-11-25/mcp
  - name: workspace-demo
    image: workspace-demo
    scope: tenant
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		if err := buildImage(context.Background(), logger, "workspace-demo", "Dockerfile", metadataFile, ".", "", "v1", "", "."); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		const want = "registry.example.org/acme/workspace-demo:v1"
		var dockerArgs []string
		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				dockerArgs = cmd.Args
			}
		}
		if !contains(dockerArgs, want) {
			t.Fatalf("docker args = %v, want tag %s", dockerArgs, want)
		}
		data, err := os.ReadFile(metadataFile)
		if err != nil {
			t.Fatalf("read metadata: %v", err)
		}
		if !strings.Contains(string(data), "image: registry.example.org/acme/workspace-demo") {
			t.Fatalf("metadata image not updated to the saved registry host:\n%s", data)
		}
		if strings.Contains(string(data), "registry.registry.svc.cluster.local") || strings.Contains(string(data), "registry.local/") {
			t.Fatalf("metadata leaked an in-cluster or placeholder registry host:\n%s", data)
		}
	})

	t.Run("uses_platform_registry_when_registry_empty", func(t *testing.T) {
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
		origConfig := core.DefaultCLIConfig
		defer func() { core.DefaultCLIConfig = origConfig }()
		core.DefaultCLIConfig = &core.CLIConfig{RegistryEndpoint: "", RegistryIngressHost: "", RegistryPort: 5000}

		kubectlMock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				for _, arg := range spec.Args {
					if arg == "jsonpath={.spec.ports[0].port}" {
						return &core.MockCommand{OutputData: []byte("5000")}
					}
				}
				return &core.MockCommand{}
			},
		}
		defer core.SwapDefaultKubectlClient(core.NewTestKubectlClient(kubectlMock))()
		mock := &core.MockExecutor{}
		defer core.SwapExecExecutor(mock)()

		tmp := t.TempDir()
		metadataFile := filepath.Join(tmp, "servers.yaml")
		if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: my-server
`), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := buildImage(context.Background(), logger, "my-server", "Dockerfile", metadataFile, ".", "", "v1.0", "", ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, cmd := range mock.Commands {
			if cmd.Name == "docker" {
				found := false
				for _, arg := range cmd.Args {
					if arg == "registry.registry.svc.cluster.local:5000/my-server:v1.0" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected platform registry in image tag, got args: %v", cmd.Args)
				}
			}
		}
	})

	t.Run("returns_error_when_command_validator_fails", func(t *testing.T) {
		failingExecutor := &validatorFailingExecutor{err: errors.New("validator failed")}
		defer core.SwapExecExecutor(failingExecutor)()

		err := buildImage(context.Background(), logger, "test-server", "Dockerfile", "", ".", "registry", "tag", "", ".")
		if err == nil {
			t.Error("expected error when command validator fails")
		}
		if err.Error() != "validator failed" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestBuildImageTenantAuthenticationFailureDoesNotBuildOrRewrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"expired_profile", http.StatusUnauthorized, `{"error":"authentication required"}`, "401"},
		{"admin_without_team", http.StatusOK, `{"authenticated":true,"principal":{"role":"admin"}}`, "no team membership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{}
			defer core.SwapExecExecutor(mock)()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/auth/me" {
					t.Errorf("unexpected platform path %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer api.Close()
			t.Setenv("MCP_PLATFORM_API_TOKEN", "test-token")
			t.Setenv("MCP_PLATFORM_API_URL", api.URL)
			t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
			file := filepath.Join(t.TempDir(), "servers.yaml")
			const original = "version: v1\nservers:\n  - name: example\n    scope: tenant\n    image: old-image\n    imageTag: old-tag\n"
			if err := os.WriteFile(file, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			err := buildImage(context.Background(), zap.NewNop(), "example", "Dockerfile", file, "", "registry.example.org", "new-tag", "linux/amd64", ".")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("build error = %v, want %q", err, tc.want)
			}
			if mock.HasCommand("docker") {
				t.Fatal("Docker must not run when tenant identity cannot be resolved")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != original {
				t.Fatalf("failed build changed metadata: %s", data)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type validatorFailingExecutor struct {
	err error
}

func (v *validatorFailingExecutor) Command(name string, args []string, validators ...core.ExecValidator) (core.Command, error) {
	return nil, v.err
}

func TestUpdateMetadataImage(t *testing.T) {
	t.Run("updates_with_explicit_metadata_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "servers.yaml")

		initialContent := `version: "1"
servers:
  - name: my-server
    image: old-registry/my-server
    imageTag: old-tag
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write initial metadata: %v", err)
		}

		err := updateMetadataImage("my-server", "new-registry/my-server", "new-tag", metadataFile, "")
		if err != nil {
			t.Fatalf("updateMetadataImage failed: %v", err)
		}

		content, err := os.ReadFile(metadataFile)
		if err != nil {
			t.Fatalf("failed to read updated metadata: %v", err)
		}

		if !strings.Contains(string(content), "new-registry/my-server") {
			t.Errorf("expected new image in metadata, got: %s", content)
		}
		if !strings.Contains(string(content), "new-tag") {
			t.Errorf("expected new tag in metadata, got: %s", content)
		}
	})

	t.Run("preserves_tool_side_effects", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "servers.yaml")

		initialContent := `version: v1
servers:
  - name: my-server
    tools:
      - name: add
        requiredTrust: low
        sideEffect: read
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write initial metadata: %v", err)
		}

		if err := updateMetadataImage("my-server", "new-image", "v1.0", metadataFile, ""); err != nil {
			t.Fatalf("updateMetadataImage failed: %v", err)
		}

		content, err := os.ReadFile(metadataFile)
		if err != nil {
			t.Fatalf("failed to read updated metadata: %v", err)
		}
		if !strings.Contains(string(content), "sideEffect: read") {
			t.Fatalf("expected sideEffect to be preserved, got:\n%s", content)
		}
	})

	t.Run("finds_metadata_in_directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataDir := filepath.Join(tmpDir, ".mcp")
		if err := os.MkdirAll(metadataDir, 0o755); err != nil {
			t.Fatalf("failed to create metadata dir: %v", err)
		}

		metadataFile := filepath.Join(metadataDir, "servers.yaml")
		initialContent := `version: "1"
servers:
  - name: discovered-server
    image: old-image
    imageTag: old
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}

		err := updateMetadataImage("discovered-server", "new-image", "v2.0", "", metadataDir)
		if err != nil {
			t.Fatalf("updateMetadataImage failed: %v", err)
		}

		content, err := os.ReadFile(metadataFile)
		if err != nil {
			t.Fatalf("failed to read metadata: %v", err)
		}

		if !strings.Contains(string(content), "new-image") {
			t.Errorf("expected new image, got: %s", content)
		}
	})

	t.Run("finds_yml_files", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataDir := filepath.Join(tmpDir, ".mcp")
		if err := os.MkdirAll(metadataDir, 0o755); err != nil {
			t.Fatalf("failed to create metadata dir: %v", err)
		}

		metadataFile := filepath.Join(metadataDir, "servers.yml")
		initialContent := `version: "1"
servers:
  - name: yml-server
    image: old-image
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}

		err := updateMetadataImage("yml-server", "new-image", "v1.0", "", metadataDir)
		if err != nil {
			t.Fatalf("updateMetadataImage failed: %v", err)
		}
	})

	t.Run("returns_error_when_file_not_found", func(t *testing.T) {
		tmpDir := t.TempDir()

		err := updateMetadataImage("nonexistent-server", "image", "tag", "", tmpDir)
		if err == nil {
			t.Error("expected error when metadata file not found")
		}
		if !strings.Contains(err.Error(), "metadata file not found") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("returns_error_when_server_not_in_metadata", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "servers.yaml")

		initialContent := `version: "1"
servers:
  - name: other-server
    image: some-image
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}

		err := updateMetadataImage("missing-server", "image", "tag", metadataFile, "")
		if err == nil {
			t.Error("expected error when server not found in metadata")
		}
		if !strings.Contains(err.Error(), "not found in metadata") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("returns_error_when_metadata_file_invalid", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "invalid.yaml")

		if err := os.WriteFile(metadataFile, []byte("not: valid: yaml: content:::"), 0o600); err != nil {
			t.Fatalf("failed to write invalid metadata: %v", err)
		}

		err := updateMetadataImage("server", "image", "tag", metadataFile, "")
		if err == nil {
			t.Error("expected error when metadata file is invalid")
		}
	})

	t.Run("skips_invalid_files_in_directory_search", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataDir := filepath.Join(tmpDir, ".mcp")
		if err := os.MkdirAll(metadataDir, 0o755); err != nil {
			t.Fatalf("failed to create metadata dir: %v", err)
		}

		invalidFile := filepath.Join(metadataDir, "invalid.yaml")
		if err := os.WriteFile(invalidFile, []byte("not: valid: yaml:::"), 0o600); err != nil {
			t.Fatalf("failed to write invalid file: %v", err)
		}

		validFile := filepath.Join(metadataDir, "valid.yaml")
		validContent := `version: "1"
servers:
  - name: target-server
    image: old-image
`
		if err := os.WriteFile(validFile, []byte(validContent), 0o600); err != nil {
			t.Fatalf("failed to write valid file: %v", err)
		}

		err := updateMetadataImage("target-server", "new-image", "v1.0", "", metadataDir)
		if err != nil {
			t.Fatalf("updateMetadataImage should skip invalid files: %v", err)
		}
	})

	t.Run("returns_error_when_file_write_fails", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can bypass read-only file mode semantics in this environment")
		}

		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "servers.yaml")

		initialContent := `version: "1"
servers:
  - name: my-server
    image: old-image
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}

		if err := os.Chmod(metadataFile, 0o400); err != nil {
			t.Fatalf("failed to chmod file: %v", err)
		}
		defer func() { _ = os.Chmod(metadataFile, 0o600) }()

		err := updateMetadataImage("my-server", "new-image", "v1.0", metadataFile, "")
		if err == nil {
			t.Error("expected error when file write fails")
		}
		if !strings.Contains(err.Error(), "failed to write metadata") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("returns_error_when_yaml_marshal_fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataFile := filepath.Join(tmpDir, "servers.yaml")

		initialContent := `version: "1"
servers:
  - name: my-server
    image: old-image
`
		if err := os.WriteFile(metadataFile, []byte(initialContent), 0o600); err != nil {
			t.Fatalf("failed to write metadata: %v", err)
		}

		originalMarshal := yamlMarshal
		defer func() { yamlMarshal = originalMarshal }()

		yamlMarshal = func(v interface{}) ([]byte, error) {
			return nil, errors.New("marshal failed")
		}

		err := updateMetadataImage("my-server", "new-image", "v1.0", metadataFile, "")
		if err == nil {
			t.Error("expected error when yaml marshal fails")
		}
		if !strings.Contains(err.Error(), "failed to marshal metadata") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
