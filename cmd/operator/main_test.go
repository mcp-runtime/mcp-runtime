package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"mcp-runtime/internal/operator"
)

func TestRegistryConfigFromEnv(t *testing.T) {
	t.Run("missing_url_returns_nil", func(t *testing.T) {
		getenv := func(string) string { return "" }
		if got := registryConfigFromEnv(getenv); got != nil {
			t.Fatalf("expected nil config when url is missing")
		}
	})

	t.Run("builds_config_from_env", func(t *testing.T) {
		env := map[string]string{
			"PROVISIONED_REGISTRY_URL":         "registry.example.com",
			"PROVISIONED_REGISTRY_USERNAME":    "user",
			"PROVISIONED_REGISTRY_PASSWORD":    "pass",
			"PROVISIONED_REGISTRY_SECRET_NAME": "secret",
		}
		getenv := func(key string) string { return env[key] }

		got := registryConfigFromEnv(getenv)
		if got == nil {
			t.Fatalf("expected config")
		}

		want := &operator.RegistryConfig{
			URL:        "registry.example.com",
			Username:   "user",
			Password:   "pass",
			SecretName: "secret",
		}

		if *got != *want {
			t.Fatalf("unexpected config: got %+v want %+v", *got, *want)
		}
	})
}

func TestGatewayProxyImageFromEnv(t *testing.T) {
	t.Run("returns empty when unset", func(t *testing.T) {
		getenv := func(string) string { return "" }
		if got := gatewayProxyImageFromEnv(getenv); got != "" {
			t.Fatalf("expected empty gateway proxy image, got %q", got)
		}
	})

	t.Run("returns configured image", func(t *testing.T) {
		env := map[string]string{
			"MCP_GATEWAY_PROXY_IMAGE": "example.com/mcp-gateway:latest",
		}
		getenv := func(key string) string { return env[key] }
		if got := gatewayProxyImageFromEnv(getenv); got != "example.com/mcp-gateway:latest" {
			t.Fatalf("unexpected gateway proxy image: %q", got)
		}
	})
}

func TestGatewayOTLPEndpointFromEnv(t *testing.T) {
	t.Run("returns empty when unset", func(t *testing.T) {
		getenv := func(string) string { return "" }
		if got := gatewayOTLPEndpointFromEnv(getenv); got != "" {
			t.Fatalf("expected empty gateway otel endpoint, got %q", got)
		}
	})

	t.Run("returns configured endpoint", func(t *testing.T) {
		env := map[string]string{
			"MCP_GATEWAY_OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector.mcp-sentinel.svc.cluster.local:4318",
		}
		getenv := func(key string) string { return env[key] }
		if got := gatewayOTLPEndpointFromEnv(getenv); got != "http://otel-collector.mcp-sentinel.svc.cluster.local:4318" {
			t.Fatalf("unexpected gateway otel endpoint: %q", got)
		}
	})
}

func TestAnalyticsIngestURLFromEnv(t *testing.T) {
	t.Run("returns empty when unset", func(t *testing.T) {
		getenv := func(string) string { return "" }
		if got := analyticsIngestURLFromEnv(getenv); got != "" {
			t.Fatalf("expected empty analytics ingest url, got %q", got)
		}
	})

	t.Run("returns configured ingest url", func(t *testing.T) {
		env := map[string]string{
			"MCP_SENTINEL_INGEST_URL": "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events",
		}
		getenv := func(key string) string { return env[key] }
		if got := analyticsIngestURLFromEnv(getenv); got != "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events" {
			t.Fatalf("unexpected analytics ingest url: %q", got)
		}
	})

	t.Run("falls back to legacy analytics env", func(t *testing.T) {
		env := map[string]string{
			"MCP_ANALYTICS_INGEST_URL": "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events",
		}
		getenv := func(key string) string { return env[key] }
		if got := analyticsIngestURLFromEnv(getenv); got != "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events" {
			t.Fatalf("unexpected analytics ingest url from legacy env: %q", got)
		}
	})
}

func TestIngressReadinessModeFromEnv(t *testing.T) {
	t.Run("defaults to strict when unset", func(t *testing.T) {
		getenv := func(string) string { return "" }
		got, valid := ingressReadinessModeFromEnv(getenv)
		if got != operator.IngressReadinessModeStrict || !valid {
			t.Fatalf("unexpected readiness mode: %q valid=%v", got, valid)
		}
	})

	t.Run("returns permissive when configured", func(t *testing.T) {
		env := map[string]string{
			"MCP_INGRESS_READINESS_MODE": "permissive",
		}
		getenv := func(key string) string { return env[key] }
		got, valid := ingressReadinessModeFromEnv(getenv)
		if got != operator.IngressReadinessModePermissive || !valid {
			t.Fatalf("unexpected readiness mode: %q valid=%v", got, valid)
		}
	})

	t.Run("invalid falls back to strict", func(t *testing.T) {
		env := map[string]string{
			"MCP_INGRESS_READINESS_MODE": "dev",
		}
		getenv := func(key string) string { return env[key] }
		got, valid := ingressReadinessModeFromEnv(getenv)
		if got != operator.IngressReadinessModeStrict || valid {
			t.Fatalf("unexpected readiness mode: %q valid=%v", got, valid)
		}
	})
}

func TestBoolFromEnv(t *testing.T) {
	if !boolFromEnv(" true ") {
		t.Fatal("expected true value")
	}
	if boolFromEnv("nope") {
		t.Fatal("expected invalid value to be false")
	}
}

func TestIngressControllerPodLabelsFromEnv(t *testing.T) {
	t.Run("unset labels", func(t *testing.T) {
		labels, err := ingressControllerPodLabelsFromEnv(func(string) string { return "" })
		if err != nil || labels != nil {
			t.Fatalf("labels,error = %v,%v; want nil,nil", labels, err)
		}
	})

	t.Run("valid selector", func(t *testing.T) {
		labels, err := ingressControllerPodLabelsFromEnv(func(string) string { return "app.kubernetes.io/name=traefik" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if labels["app.kubernetes.io/name"] != "traefik" {
			t.Fatalf("labels = %v; want app.kubernetes.io/name=traefik", labels)
		}
	})

	t.Run("malformed selector is an error", func(t *testing.T) {
		labels, err := ingressControllerPodLabelsFromEnv(func(string) string { return "app in (traefik)" })
		if err == nil || labels != nil {
			t.Fatalf("labels,error = %v,%v; want nil and a configuration error", labels, err)
		}
		if !strings.Contains(err.Error(), "MCP_INGRESS_CONTROLLER_POD_LABELS") {
			t.Fatalf("error = %q; want actionable environment variable name", err)
		}
	})
}

func TestParseConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)

		cfg, err := parseConfig(fs, nil)
		if err != nil {
			t.Fatalf("parseConfig() error: %v", err)
		}

		if cfg.metricsAddr != ":8080" {
			t.Fatalf("unexpected metricsAddr: %q", cfg.metricsAddr)
		}
		if cfg.probeAddr != ":8081" {
			t.Fatalf("unexpected probeAddr: %q", cfg.probeAddr)
		}
		if cfg.enableLeaderElection {
			t.Fatalf("expected leader election disabled by default")
		}
		if !cfg.zapOptions.Development {
			t.Fatalf("expected development logging default")
		}
	})

	t.Run("overrides", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)

		args := []string{
			"--metrics-bind-address=localhost:9090",
			"--health-probe-bind-address=localhost:9091",
			"--leader-elect",
		}
		cfg, err := parseConfig(fs, args)
		if err != nil {
			t.Fatalf("parseConfig() error: %v", err)
		}

		if cfg.metricsAddr != "localhost:9090" {
			t.Fatalf("unexpected metricsAddr: %q", cfg.metricsAddr)
		}
		if cfg.probeAddr != "localhost:9091" {
			t.Fatalf("unexpected probeAddr: %q", cfg.probeAddr)
		}
		if !cfg.enableLeaderElection {
			t.Fatalf("expected leader election enabled")
		}
	})
}

func TestNewManagerOptions(t *testing.T) {
	cfg := &operatorConfig{
		metricsAddr:          "localhost:9999",
		probeAddr:            "localhost:9998",
		enableLeaderElection: true,
	}

	opts := newManagerOptions(cfg)

	if opts.Scheme != scheme {
		t.Fatalf("expected scheme to be set")
	}
	if opts.Metrics.BindAddress != "localhost:9999" {
		t.Fatalf("unexpected metrics bind address: %q", opts.Metrics.BindAddress)
	}
	if opts.HealthProbeBindAddress != "localhost:9998" {
		t.Fatalf("unexpected probe addr: %q", opts.HealthProbeBindAddress)
	}
	if !opts.LeaderElection {
		t.Fatalf("expected leader election enabled")
	}
	if opts.LeaderElectionID != "mcp-runtime-operator.mcpruntime.org" {
		t.Fatalf("unexpected leader election id: %q", opts.LeaderElectionID)
	}
}
