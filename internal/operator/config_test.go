package operator

import (
	"testing"
)

func TestHasProvisionedRegistry(t *testing.T) {
	tests := []struct {
		name string
		cfg  *OperatorConfig
		want bool
	}{
		{
			name: "has_provisioned_registry",
			cfg: &OperatorConfig{
				ProvisionedRegistryURL: "registry.example.com:5000",
			},
			want: true,
		},
		{
			name: "no_provisioned_registry",
			cfg:  &OperatorConfig{},
			want: false,
		},
		{
			name: "empty_url",
			cfg: &OperatorConfig{
				ProvisionedRegistryURL: "",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasProvisionedRegistry(); got != tt.want {
				t.Errorf("HasProvisionedRegistry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToRegistryConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *OperatorConfig
		wantNil bool
		wantURL string
	}{
		{
			name: "converts_when_registry_configured",
			cfg: &OperatorConfig{
				ProvisionedRegistryURL:        "registry.example.com:5000",
				ProvisionedRegistryUsername:   "user",
				ProvisionedRegistryPassword:   "pass",
				ProvisionedRegistrySecretName: "my-secret",
			},
			wantNil: false,
			wantURL: "registry.example.com:5000",
		},
		{
			name:    "returns_nil_when_no_registry",
			cfg:     &OperatorConfig{},
			wantNil: true,
		},
		{
			name: "returns_nil_when_empty_url",
			cfg: &OperatorConfig{
				ProvisionedRegistryURL: "",
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.ToRegistryConfig()
			if tt.wantNil {
				if got != nil {
					t.Errorf("ToRegistryConfig() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ToRegistryConfig() = nil, want non-nil")
			}
			if got.URL != tt.wantURL {
				t.Errorf("ToRegistryConfig().URL = %v, want %v", got.URL, tt.wantURL)
			}
			if got.Username != tt.cfg.ProvisionedRegistryUsername {
				t.Errorf("ToRegistryConfig().Username = %v, want %v", got.Username, tt.cfg.ProvisionedRegistryUsername)
			}
			if got.Password != tt.cfg.ProvisionedRegistryPassword {
				t.Errorf("ToRegistryConfig().Password = %v, want %v", got.Password, tt.cfg.ProvisionedRegistryPassword)
			}
			if got.SecretName != tt.cfg.ProvisionedRegistrySecretName {
				t.Errorf("ToRegistryConfig().SecretName = %v, want %v", got.SecretName, tt.cfg.ProvisionedRegistrySecretName)
			}
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		setEnv       bool
		want         string
	}{
		{
			name:         "returns_env_value_when_set",
			key:          "TEST_ENV_VAR_1",
			defaultValue: "default",
			envValue:     "custom",
			setEnv:       true,
			want:         "custom",
		},
		{
			name:         "returns_default_when_not_set",
			key:          "TEST_ENV_VAR_2",
			defaultValue: "default",
			setEnv:       false,
			want:         "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envValue)
			}
			if got := getEnvOrDefault(tt.key, tt.defaultValue); got != tt.want {
				t.Errorf("getEnvOrDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetEnvIntOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue int
		envValue     string
		setEnv       bool
		want         int
	}{
		{
			name:         "returns_env_int_when_valid",
			key:          "TEST_INT_VAR_1",
			defaultValue: 10,
			envValue:     "42",
			setEnv:       true,
			want:         42,
		},
		{
			name:         "returns_default_when_not_set",
			key:          "TEST_INT_VAR_2",
			defaultValue: 10,
			setEnv:       false,
			want:         10,
		},
		{
			name:         "returns_default_when_invalid_int",
			key:          "TEST_INT_VAR_3",
			defaultValue: 10,
			envValue:     "not-a-number",
			setEnv:       true,
			want:         10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envValue)
			}
			if got := getEnvIntOrDefault(tt.key, tt.defaultValue); got != tt.want {
				t.Errorf("getEnvIntOrDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadOperatorConfig(t *testing.T) {
	t.Setenv("MCP_CLUSTER_NAME", "prod-cluster")
	t.Setenv("DEFAULT_INGRESS_HOST", "mcp.example.com")
	t.Setenv("DEFAULT_INGRESS_CLASS", "nginx")
	t.Setenv("MCP_DEFAULT_INGRESS_ENTRYPOINTS", "websecure")
	t.Setenv("MCP_DEFAULT_INGRESS_TLS", "true")
	t.Setenv("MCP_INGRESS_READINESS_MODE", "permissive")
	t.Setenv("PROVISIONED_REGISTRY_URL", "registry.example.com:5000")
	t.Setenv("MCP_REGISTRY_ENDPOINT", "10.43.39.164:5000")
	t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
	t.Setenv("PROVISIONED_REGISTRY_USERNAME", "user")
	t.Setenv("PROVISIONED_REGISTRY_PASSWORD", "pass")
	t.Setenv("PROVISIONED_REGISTRY_SECRET_NAME", "registry-creds")
	t.Setenv("REQUEUE_DELAY_SECONDS", "45")
	t.Setenv("MCP_GATEWAY_PROXY_IMAGE", "example.com/mcp-gateway:latest")
	t.Setenv("MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.mcp-observability.svc.cluster.local:4318")
	t.Setenv("MCP_ANALYTICS_INGEST_URL", "http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events")

	cfg := LoadOperatorConfig()
	if cfg.DefaultIngressHost != "mcp.example.com" {
		t.Fatalf("expected ingress host override, got %q", cfg.DefaultIngressHost)
	}
	if cfg.DefaultIngressClass != "nginx" {
		t.Fatalf("expected ingress class override, got %q", cfg.DefaultIngressClass)
	}
	if cfg.DefaultIngressEntryPoints != "websecure" {
		t.Fatalf("expected ingress entrypoints override, got %q", cfg.DefaultIngressEntryPoints)
	}
	if !cfg.DefaultIngressTLS {
		t.Fatal("expected ingress TLS override")
	}
	if cfg.IngressReadinessMode != IngressReadinessModePermissive {
		t.Fatalf("expected ingress readiness mode override, got %q", cfg.IngressReadinessMode)
	}
	if cfg.ProvisionedRegistryURL != "registry.example.com:5000" {
		t.Fatalf("expected registry url override, got %q", cfg.ProvisionedRegistryURL)
	}
	if cfg.InternalRegistryEndpoint != "10.43.39.164:5000" {
		t.Fatalf("expected internal registry endpoint override, got %q", cfg.InternalRegistryEndpoint)
	}
	if cfg.RegistryPullHost != "10.43.39.164:5000" {
		t.Fatalf("expected registry pull host override, got %q", cfg.RegistryPullHost)
	}
	if cfg.ProvisionedRegistryUsername != "user" || cfg.ProvisionedRegistryPassword != "pass" {
		t.Fatalf("expected registry credentials, got %q/%q", cfg.ProvisionedRegistryUsername, cfg.ProvisionedRegistryPassword)
	}
	if cfg.ProvisionedRegistrySecretName != "registry-creds" {
		t.Fatalf("expected registry secret override, got %q", cfg.ProvisionedRegistrySecretName)
	}
	if cfg.RequeueDelaySeconds != 45 {
		t.Fatalf("expected requeue delay override, got %d", cfg.RequeueDelaySeconds)
	}
	if cfg.GatewayProxyImage != "example.com/mcp-gateway:latest" {
		t.Fatalf("expected gateway proxy image override, got %q", cfg.GatewayProxyImage)
	}
	if cfg.GatewayOTLPEndpoint != "http://otel-collector.mcp-observability.svc.cluster.local:4318" {
		t.Fatalf("expected gateway otel endpoint override, got %q", cfg.GatewayOTLPEndpoint)
	}
	if cfg.AnalyticsIngestURL != "http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events" {
		t.Fatalf("expected analytics ingest url override, got %q", cfg.AnalyticsIngestURL)
	}
	if cfg.ClusterName != "prod-cluster" {
		t.Fatalf("expected cluster name override, got %q", cfg.ClusterName)
	}
}

func TestLoadOperatorConfigPrefersMCPDefaultIngressHost(t *testing.T) {
	t.Setenv("MCP_DEFAULT_INGRESS_HOST", "mcp-default.example.com")
	t.Setenv("DEFAULT_INGRESS_HOST", "legacy-default.example.com")

	cfg := LoadOperatorConfig()
	if cfg.DefaultIngressHost != "mcp-default.example.com" {
		t.Fatalf("expected MCP_DEFAULT_INGRESS_HOST override, got %q", cfg.DefaultIngressHost)
	}
}

func TestNormalizeIngressReadinessMode(t *testing.T) {
	for _, tt := range []struct {
		name      string
		value     string
		wantMode  string
		wantValid bool
	}{
		{name: "empty defaults to strict", value: "", wantMode: IngressReadinessModeStrict, wantValid: true},
		{name: "strict", value: "strict", wantMode: IngressReadinessModeStrict, wantValid: true},
		{name: "permissive", value: "permissive", wantMode: IngressReadinessModePermissive, wantValid: true},
		{name: "trims and normalizes case", value: " Permissive ", wantMode: IngressReadinessModePermissive, wantValid: true},
		{name: "invalid falls back to strict", value: "dev", wantMode: IngressReadinessModeStrict, wantValid: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotValid := NormalizeIngressReadinessMode(tt.value)
			if gotMode != tt.wantMode || gotValid != tt.wantValid {
				t.Fatalf("NormalizeIngressReadinessMode(%q) = %q, %v; want %q, %v", tt.value, gotMode, gotValid, tt.wantMode, tt.wantValid)
			}
		})
	}
}
