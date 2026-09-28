package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
	t.Setenv("MCP_REGISTRY_HOST", "")

	tests := []struct {
		name     string
		filePath string
		want     *RegistryFile
		wantErr  bool
	}{
		{
			name:     "valid-yaml",
			filePath: "testdata/valid.yaml",
			want: &RegistryFile{
				Version: "v1",
				Servers: []ServerMetadata{
					{
						Name:      "test-server",
						Image:     "registry.local/test-server",
						ImageTag:  "latest",
						Route:     "/test-server/mcp",
						Port:      8088,
						Replicas:  int32Ptr(1),
						Namespace: "mcp-servers",
					},
					{
						Name:        "custom-server",
						Description: "Example server for testing metadata loading.",
						Image:       "custom/image",
						ImageTag:    "v1",
						Route:       "/custom-route",
						Port:        9090,
						Replicas:    int32Ptr(3),
						Namespace:   "custom-namespace",
						TeamID:      "team-custom",
						Gateway: &GatewayConfig{
							Enabled:     boolPtr(true),
							Image:       "example.com/mcp-gateway:latest",
							Port:        8091,
							UpstreamURL: "http://127.0.0.1:9090",
						},
						Auth: &AuthConfig{
							TokenHeader: "Authorization",
						},
						Policy: &PolicyConfig{
							Mode:            PolicyMode("allow-list"),
							DefaultDecision: PolicyDecision("deny"),
							EnforceOn:       "call_tool",
							PolicyVersion:   "v1",
						},
						Session: &SessionConfig{
							Required:            true,
							Store:               "kubernetes",
							MaxLifetime:         "24h",
							IdleTimeout:         "1h",
							UpstreamTokenHeader: "Authorization",
						},
						Tools: []ToolConfig{
							{Name: "list_tools", Description: "List tools exposed by the server.", RequiredTrust: TrustLevel("low"), SideEffect: ToolSideEffect("read")},
							{Name: "delete_user", Description: "Delete a user from the backing system.", RequiredTrust: TrustLevel("high"), SideEffect: ToolSideEffect("destructive")},
						},
						SecretEnvVars: []SecretEnvVar{
							{
								Name: "OPENAI_API_KEY",
								SecretKeyRef: &SecretKeyRef{
									Name: "provider-creds",
									Key:  "openai",
								},
							},
						},
						Analytics: &AnalyticsConfig{
							IngestURL: "http://analytics.custom-namespace.svc/api/events",
							Source:    "custom-server-gateway",
							EventType: "mcp.request",
							APIKeySecretRef: &SecretKeyRef{
								Name: "analytics-creds",
								Key:  "api-key",
							},
						},
						Rollout: &RolloutConfig{
							Strategy:       RolloutStrategy("Canary"),
							MaxUnavailable: "25%",
							MaxSurge:       "25%",
							CanaryReplicas: int32Ptr(1),
						},
					},
				},
			},
		},
		{
			name:     "invalid-yaml",
			filePath: "testdata/invalid.yaml",
			want:     nil,
			wantErr:  true,
		},
		{
			name:     "missing-file",
			filePath: "testdata/missing.yaml",
			want:     nil,
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := LoadFromFile(test.filePath)
			if test.wantErr {
				if err == nil {
					t.Fatalf("LoadFromFile(%q) expected error, got nil", test.filePath)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFromFile(%q) unexpected error: %v", test.filePath, err)
			}
			if registry.Version != test.want.Version {
				t.Fatalf("LoadFromFile(%q) version = %q, want %q", test.filePath, registry.Version, test.want.Version)
			}
			if len(registry.Servers) != len(test.want.Servers) {
				t.Fatalf("LoadFromFile(%q) servers length = %d, want %d", test.filePath, len(registry.Servers), len(test.want.Servers))
			}
			for i := range registry.Servers {
				got := registry.Servers[i]
				want := test.want.Servers[i]
				if got.Name != want.Name {
					t.Errorf("server[%d].Name = %q, want %q", i, got.Name, want.Name)
				}
				if got.Description != want.Description {
					t.Errorf("server[%d].Description = %q, want %q", i, got.Description, want.Description)
				}
				if got.Image != want.Image {
					t.Errorf("server[%d].Image = %q, want %q", i, got.Image, want.Image)
				}
				if got.ImageTag != want.ImageTag {
					t.Errorf("server[%d].ImageTag = %q, want %q", i, got.ImageTag, want.ImageTag)
				}
				if got.Route != want.Route {
					t.Errorf("server[%d].Route = %q, want %q", i, got.Route, want.Route)
				}
				if got.Port != want.Port {
					t.Errorf("server[%d].Port = %d, want %d", i, got.Port, want.Port)
				}
				if !int32PtrEqual(got.Replicas, want.Replicas) {
					t.Errorf("server[%d].Replicas = %v, want %v", i, got.Replicas, want.Replicas)
				}
				if got.Namespace != want.Namespace {
					t.Errorf("server[%d].Namespace = %q, want %q", i, got.Namespace, want.Namespace)
				}
				if got.Scope != want.Scope {
					t.Errorf("server[%d].Scope = %q, want %q", i, got.Scope, want.Scope)
				}
				if got.TeamID != want.TeamID {
					t.Errorf("server[%d].TeamID = %q, want %q", i, got.TeamID, want.TeamID)
				}
				if !gatewayConfigEqual(got.Gateway, want.Gateway) {
					t.Errorf("server[%d].Gateway = %#v, want %#v", i, got.Gateway, want.Gateway)
				}
				if !reflect.DeepEqual(got.Auth, want.Auth) {
					t.Errorf("server[%d].Auth = %#v, want %#v", i, got.Auth, want.Auth)
				}
				if !reflect.DeepEqual(got.Policy, want.Policy) {
					t.Errorf("server[%d].Policy = %#v, want %#v", i, got.Policy, want.Policy)
				}
				if !reflect.DeepEqual(got.Session, want.Session) {
					t.Errorf("server[%d].Session = %#v, want %#v", i, got.Session, want.Session)
				}
				if !reflect.DeepEqual(got.Tools, want.Tools) {
					t.Errorf("server[%d].Tools = %#v, want %#v", i, got.Tools, want.Tools)
				}
				if !reflect.DeepEqual(got.SecretEnvVars, want.SecretEnvVars) {
					t.Errorf("server[%d].SecretEnvVars = %#v, want %#v", i, got.SecretEnvVars, want.SecretEnvVars)
				}
				if !analyticsConfigEqual(got.Analytics, want.Analytics) {
					t.Errorf("server[%d].Analytics = %#v, want %#v", i, got.Analytics, want.Analytics)
				}
				if !reflect.DeepEqual(got.Rollout, want.Rollout) {
					t.Errorf("server[%d].Rollout = %#v, want %#v", i, got.Rollout, want.Rollout)
				}
			}
		})
	}
}

func TestSetDefaults(t *testing.T) {
	t.Setenv("MCP_PLATFORM_DOMAIN", "")
	t.Setenv("MCP_MCP_INGRESS_HOST", "")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
	t.Setenv("MCP_REGISTRY_HOST", "")

	tests := []struct {
		name   string
		server *ServerMetadata
		want   *ServerMetadata
	}{
		{
			name: "apply-defaults",
			server: &ServerMetadata{
				Name:  "test-server",
				Image: "test-image",
			},
			want: &ServerMetadata{
				Name:      "test-server",
				Image:     "test-image",
				ImageTag:  "latest",
				Route:     "/test-server/mcp",
				Port:      8088,
				Replicas:  int32Ptr(1),
				Namespace: "mcp-servers",
			},
		},
		{
			name: "test-server",
			server: &ServerMetadata{
				Name:      "test-server",
				Image:     "test-image",
				ImageTag:  "latest",
				Route:     "/test-server/mcp",
				Port:      8088,
				Replicas:  int32Ptr(1),
				Namespace: "mcp-servers",
			},
			want: &ServerMetadata{
				Name:      "test-server",
				Image:     "test-image",
				ImageTag:  "latest",
				Route:     "/test-server/mcp",
				Port:      8088,
				Replicas:  int32Ptr(1),
				Namespace: "mcp-servers",
			},
		},
		{
			name: "applies-gateway-and-analytics-defaults",
			server: &ServerMetadata{
				Name:  "gateway-server",
				Image: "test-image",
				Port:  9090,
				Gateway: &GatewayConfig{
					Enabled: boolPtr(true),
				},
				Auth:   &AuthConfig{},
				Policy: &PolicyConfig{},
				Session: &SessionConfig{
					Required: true,
				},
				Tools: []ToolConfig{
					{Name: "delete_user", RequiredTrust: TrustLevel("high"), SideEffect: ToolSideEffect("destructive")},
				},
				SecretEnvVars: []SecretEnvVar{
					{
						Name:         "OPENAI_API_KEY",
						SecretKeyRef: &SecretKeyRef{Name: "provider-creds", Key: "openai"},
					},
				},
				Analytics: &AnalyticsConfig{
					IngestURL: "http://analytics.default.svc/api/events",
				},
				Rollout: &RolloutConfig{
					Strategy: RolloutStrategy("Canary"),
				},
			},
			want: &ServerMetadata{
				Name:      "gateway-server",
				Image:     "test-image",
				ImageTag:  "latest",
				Route:     "/gateway-server/mcp",
				Port:      9090,
				Replicas:  int32Ptr(1),
				Namespace: "mcp-servers",
				Gateway: &GatewayConfig{
					Enabled:     boolPtr(true),
					Port:        8091,
					UpstreamURL: "http://127.0.0.1:9090",
				},
				Auth: &AuthConfig{
					TokenHeader: "Authorization",
				},
				Policy: &PolicyConfig{
					Mode:            PolicyModeAllowList,
					DefaultDecision: PolicyDecisionDeny,
					EnforceOn:       "call_tool",
					PolicyVersion:   "v1",
				},
				Session: &SessionConfig{
					Required:            true,
					Store:               "kubernetes",
					MaxLifetime:         "24h",
					IdleTimeout:         "1h",
					UpstreamTokenHeader: "Authorization",
				},
				Tools: []ToolConfig{
					{Name: "delete_user", RequiredTrust: TrustLevel("high"), SideEffect: ToolSideEffect("destructive")},
				},
				SecretEnvVars: []SecretEnvVar{
					{
						Name:         "OPENAI_API_KEY",
						SecretKeyRef: &SecretKeyRef{Name: "provider-creds", Key: "openai"},
					},
				},
				Analytics: &AnalyticsConfig{
					IngestURL: "http://analytics.default.svc/api/events",
					Source:    "gateway-server-gateway",
					EventType: "mcp.request",
				},
				Rollout: &RolloutConfig{
					Strategy:       RolloutStrategyCanary,
					MaxUnavailable: "25%",
					MaxSurge:       "25%",
				},
			},
		},
		{
			name: "applies-public-scope-defaults",
			server: &ServerMetadata{
				Name:  "public-server",
				Scope: PublishScope("Public"),
			},
			want: &ServerMetadata{
				Name:      "public-server",
				Scope:     PublishScopePublic,
				Image:     "registry.local/public/public-server",
				ImageTag:  "latest",
				Route:     "/public-server/mcp",
				Port:      8088,
				Replicas:  int32Ptr(1),
				Namespace: "mcp-servers-public",
			},
		},
	}
	for _, test := range tests {
		if err := setDefaults(test.server); err != nil {
			t.Fatalf("setDefaults(%q) unexpected error: %v", test.name, err)
		}
		if test.server.Name != test.want.Name {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.Name, test.server.Name, test.want.Name)
		}
		if test.server.Image != test.want.Image {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.Image, test.server.Image, test.want.Image)
		}
		if test.server.ImageTag != test.want.ImageTag {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.ImageTag, test.server.ImageTag, test.want.ImageTag)
		}
		if test.server.Route != test.want.Route {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.Route, test.server.Route, test.want.Route)
		}
		if test.server.Port != test.want.Port {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.Port, test.server.Port, test.want.Port)
		}
		if !int32PtrEqual(test.server.Replicas, test.want.Replicas) {
			t.Errorf("setDefaults Replicas = %v, want %v", test.server.Replicas, test.want.Replicas)
		}
		if test.server.Namespace != test.want.Namespace {
			t.Errorf("setDefaults(%q) = %q, want %q", test.server.Namespace, test.server.Namespace, test.want.Namespace)
		}
		if test.server.Scope != test.want.Scope {
			t.Errorf("setDefaults Scope = %q, want %q", test.server.Scope, test.want.Scope)
		}
		if !gatewayConfigEqual(test.server.Gateway, test.want.Gateway) {
			t.Errorf("setDefaults Gateway = %#v, want %#v", test.server.Gateway, test.want.Gateway)
		}
		if !reflect.DeepEqual(test.server.Auth, test.want.Auth) {
			t.Errorf("setDefaults Auth = %#v, want %#v", test.server.Auth, test.want.Auth)
		}
		if !reflect.DeepEqual(test.server.Policy, test.want.Policy) {
			t.Errorf("setDefaults Policy = %#v, want %#v", test.server.Policy, test.want.Policy)
		}
		if !reflect.DeepEqual(test.server.Session, test.want.Session) {
			t.Errorf("setDefaults Session = %#v, want %#v", test.server.Session, test.want.Session)
		}
		if !reflect.DeepEqual(test.server.Tools, test.want.Tools) {
			t.Errorf("setDefaults Tools = %#v, want %#v", test.server.Tools, test.want.Tools)
		}
		if !reflect.DeepEqual(test.server.SecretEnvVars, test.want.SecretEnvVars) {
			t.Errorf("setDefaults SecretEnvVars = %#v, want %#v", test.server.SecretEnvVars, test.want.SecretEnvVars)
		}
		if !analyticsConfigEqual(test.server.Analytics, test.want.Analytics) {
			t.Errorf("setDefaults Analytics = %#v, want %#v", test.server.Analytics, test.want.Analytics)
		}
		if !reflect.DeepEqual(test.server.Rollout, test.want.Rollout) {
			t.Errorf("setDefaults Rollout = %#v, want %#v", test.server.Rollout, test.want.Rollout)
		}
	}
}

func TestSetDefaultsImageResolution(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		input     *ServerMetadata
		wantImage string
	}{
		{
			name:      "default when no env",
			env:       nil,
			input:     &ServerMetadata{Name: "srv"},
			wantImage: "registry.local/srv",
		},
		{
			name:      "honors MCP_REGISTRY_INGRESS_HOST",
			env:       map[string]string{"MCP_REGISTRY_INGRESS_HOST": "registry.example.com"},
			input:     &ServerMetadata{Name: "srv"},
			wantImage: "registry.example.com/srv",
		},
		{
			name:      "falls back to legacy MCP_REGISTRY_HOST",
			env:       map[string]string{"MCP_REGISTRY_HOST": "legacy.example.com"},
			input:     &ServerMetadata{Name: "srv"},
			wantImage: "legacy.example.com/srv",
		},
		{
			name:      "honors MCP_PLATFORM_DOMAIN for registry host",
			env:       map[string]string{"MCP_PLATFORM_DOMAIN": "mcpruntime.com"},
			input:     &ServerMetadata{Name: "srv"},
			wantImage: "registry.mcpruntime.com/srv",
		},
		{
			name:      "does not overwrite explicit image",
			env:       map[string]string{"MCP_REGISTRY_INGRESS_HOST": "registry.example.com"},
			input:     &ServerMetadata{Name: "srv", Image: "custom/image"},
			wantImage: "custom/image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_PLATFORM_DOMAIN", "")
			t.Setenv("MCP_MCP_INGRESS_HOST", "")
			t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
			t.Setenv("MCP_REGISTRY_HOST", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if err := setDefaults(tc.input); err != nil {
				t.Fatalf("setDefaults() unexpected error: %v", err)
			}
			if tc.input.Image != tc.wantImage {
				t.Fatalf("Image = %q, want %q", tc.input.Image, tc.wantImage)
			}
		})
	}
}

func TestLoadFromDirectory(t *testing.T) {
	t.Run("loads_all_yaml_files_from_directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create test YAML files
		file1 := filepath.Join(tmpDir, "server1.yaml")
		file2 := filepath.Join(tmpDir, "server2.yml")

		yaml1 := `version: v1
servers:
  - name: server1
    image: img1
`
		yaml2 := `version: v1
servers:
  - name: server2
    image: img2
`
		if err := os.WriteFile(file1, []byte(yaml1), 0644); err != nil {
			t.Fatalf("failed to write file1: %v", err)
		}
		if err := os.WriteFile(file2, []byte(yaml2), 0644); err != nil {
			t.Fatalf("failed to write file2: %v", err)
		}

		registry, err := LoadFromDirectory(tmpDir)
		if err != nil {
			t.Fatalf("LoadFromDirectory() unexpected error: %v", err)
		}

		if len(registry.Servers) != 2 {
			t.Errorf("LoadFromDirectory() servers = %d, want 2", len(registry.Servers))
		}

		// Check both servers are loaded (order may vary)
		names := make(map[string]bool)
		for _, s := range registry.Servers {
			names[s.Name] = true
		}
		if !names["server1"] || !names["server2"] {
			t.Errorf("LoadFromDirectory() missing servers, got names: %v", names)
		}
	})

	t.Run("returns_empty_registry_for_empty_directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		registry, err := LoadFromDirectory(tmpDir)
		if err != nil {
			t.Fatalf("LoadFromDirectory() unexpected error: %v", err)
		}

		if len(registry.Servers) != 0 {
			t.Errorf("LoadFromDirectory() servers = %d, want 0", len(registry.Servers))
		}
		if registry.Version != "v1" {
			t.Errorf("LoadFromDirectory() version = %q, want v1", registry.Version)
		}
	})

	t.Run("returns_error_for_invalid_yaml_in_directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		invalidFile := filepath.Join(tmpDir, "invalid.yaml")
		if err := os.WriteFile(invalidFile, []byte("{{invalid yaml"), 0644); err != nil {
			t.Fatalf("failed to write invalid file: %v", err)
		}

		_, err := LoadFromDirectory(tmpDir)
		if err == nil {
			t.Fatal("LoadFromDirectory() expected error for invalid YAML, got nil")
		}
	})

	t.Run("ignores_non_yaml_files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a YAML file and a non-YAML file
		yamlFile := filepath.Join(tmpDir, "server.yaml")
		txtFile := filepath.Join(tmpDir, "readme.txt")

		yaml := `version: v1
servers:
  - name: test-server
`
		if err := os.WriteFile(yamlFile, []byte(yaml), 0644); err != nil {
			t.Fatalf("failed to write yaml file: %v", err)
		}
		if err := os.WriteFile(txtFile, []byte("not yaml"), 0644); err != nil {
			t.Fatalf("failed to write txt file: %v", err)
		}

		registry, err := LoadFromDirectory(tmpDir)
		if err != nil {
			t.Fatalf("LoadFromDirectory() unexpected error: %v", err)
		}

		if len(registry.Servers) != 1 {
			t.Errorf("LoadFromDirectory() servers = %d, want 1", len(registry.Servers))
		}
	})
}

func TestLoadFromFileRejectsInvalidScope(t *testing.T) {
	tmpDir := t.TempDir()
	metadataFile := filepath.Join(tmpDir, "servers.yaml")
	if err := os.WriteFile(metadataFile, []byte(`version: v1
servers:
  - name: bad-scope
    scope: internet
`), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	if _, err := LoadFromFile(metadataFile); err == nil {
		t.Fatal("expected invalid scope error")
	} else if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("error = %v, want scope validation", err)
	}
}

func TestSetDefaultsRejectsInvalidScope(t *testing.T) {
	err := setDefaults(&ServerMetadata{Name: "bad-scope", Scope: PublishScope("internet")})
	if err == nil {
		t.Fatal("expected invalid scope error")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("error = %v, want scope validation", err)
	}
}

func int32Ptr(i int32) *int32 {
	return &i
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func gatewayConfigEqual(a, b *GatewayConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return GatewayIsEnabled(a) == GatewayIsEnabled(b) &&
		a.Image == b.Image &&
		a.Port == b.Port &&
		a.UpstreamURL == b.UpstreamURL &&
		a.StripPrefix == b.StripPrefix
}

func analyticsConfigEqual(a, b *AnalyticsConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Disabled == b.Disabled &&
		a.IngestURL == b.IngestURL &&
		a.Source == b.Source &&
		a.EventType == b.EventType &&
		secretKeyRefEqual(a.APIKeySecretRef, b.APIKeySecretRef)
}

func secretKeyRefEqual(a, b *SecretKeyRef) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Key == b.Key
}
