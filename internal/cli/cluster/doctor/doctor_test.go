package doctor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/kubeworkload"
)

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func TestDetectDistribution(t *testing.T) {
	cases := []struct {
		name    string
		kubelet string // stdout for `get nodes -o jsonpath=.status.nodeInfo.kubeletVersion`
		names   string // stdout for `get nodes -o jsonpath=.metadata.name`
		context string // stdout for `config current-context`
		want    Distribution
	}{
		{
			name:    "k3s from kubelet version",
			kubelet: "v1.34.6+k3s1",
			want:    DistroK3s,
		},
		{
			name:  "kind from node name",
			names: "kind-control-plane",
			want:  DistroKind,
		},
		{
			name:  "does not treat generic control-plane names as kind",
			names: "prod-control-plane",
			want:  DistroGeneric,
		},
		{
			name:  "minikube from node name",
			names: "minikube",
			want:  DistroMinikube,
		},
		{
			name:  "docker-desktop from node name",
			names: "docker-desktop",
			want:  DistroDockerDesktop,
		},
		{
			name:    "minikube from context fallback",
			context: "minikube",
			want:    DistroMinikube,
		},
		{
			name: "unknown returns generic",
			want: DistroGeneric,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{
				CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
					switch {
					case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
						return &core.MockCommand{OutputData: []byte(tc.kubelet)}
					case contains(spec.Args, "jsonpath={.items[*].metadata.name}"):
						return &core.MockCommand{OutputData: []byte(tc.names)}
					case contains(spec.Args, "current-context"):
						return &core.MockCommand{OutputData: []byte(tc.context)}
					}
					return &core.MockCommand{}
				},
			}
			kubectl := core.NewTestKubectlClient(mock)
			got := DetectDistribution(kubectl)
			if got != tc.want {
				t.Fatalf("DetectDistribution() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckRegistryService(t *testing.T) {
	t.Run("ok with nodeport", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("32000")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryService(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "32000") {
			t.Fatalf("detail should mention the NodePort, got %q", check.Detail)
		}
	})

	t.Run("fails when service missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("not found")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryService(kubectl)
		if check.OK {
			t.Fatal("expected failure when service missing")
		}
		if check.Remedy == "" {
			t.Fatal("expected a remedy hint")
		}
	})
}

func TestCheckNamespaceExists(t *testing.T) {
	t.Run("ok when namespace exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("mcp-servers")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNamespaceExists(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when namespace missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("not found")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNamespaceExists(kubectl, "mcp-servers")
		if check.OK {
			t.Fatal("expected failure when namespace is missing")
		}
	})
}

func TestCheckMCPServerCRD(t *testing.T) {
	t.Run("ok when CRD exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("mcpservers.mcpruntime.org")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServerCRD(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when CRD missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("not found")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServerCRD(kubectl)
		if check.OK {
			t.Fatal("expected failure when CRD is missing")
		}
	})
}

func TestCheckOperatorReady(t *testing.T) {
	t.Run("ok when desired replicas are ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("1/1")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkOperatorReady(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when not enough replicas are ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("0/1")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkOperatorReady(kubectl)
		if check.OK {
			t.Fatal("expected failure for 0/1 ready replicas")
		}
	})
}

func testOperatorWebhookServingCertB64(t *testing.T, notAfter time.Time) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "webhook-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCheckOperatorWebhookCertExpiry(t *testing.T) {
	t.Run("ok when webhook secret is absent", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("")}
			},
		}
		check := checkOperatorWebhookCertExpiry(core.NewTestKubectlClient(mock))
		if !check.OK || !strings.Contains(check.Detail, "not present") {
			t.Fatalf("unexpected check: %+v", check)
		}
	})

	t.Run("warns inside renewal window", func(t *testing.T) {
		certB64 := testOperatorWebhookServingCertB64(t, time.Now().UTC().Add(10*24*time.Hour))
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(certB64)}
			},
		}
		check := checkOperatorWebhookCertExpiry(core.NewTestKubectlClient(mock))
		if !check.OK || !strings.Contains(check.Detail, "warning:") {
			t.Fatalf("expected renewal warning, got %+v", check)
		}
	})
}

func TestCheckGatewayAnalyticsCredentials(t *testing.T) {
	t.Run("fails when gateway has ingest URL without api key", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(`{
  "items": [{
    "metadata": {"namespace": "mcp-team-acme", "name": "acme-tools"},
    "spec": {"template": {"spec": {"containers": [{
      "name": "mcp-gateway",
      "env": [{"name": "ANALYTICS_INGEST_URL", "value": "http://ingest/events"}]
    }]}}}
  }]
}`)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkGatewayAnalyticsCredentials(kubectl)
		if check.OK {
			t.Fatal("expected missing analytics key to fail")
		}
		if !strings.Contains(check.Detail, "missing ANALYTICS_API_KEY") {
			t.Fatalf("detail = %q, want missing key message", check.Detail)
		}
	})

	t.Run("passes when secret key exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "secret") {
					return &core.MockCommand{OutputData: []byte(fmt.Sprintf(`{"data":{"api-key":%q}}`, base64.StdEncoding.EncodeToString([]byte("ingest-key"))))}
				}
				return &core.MockCommand{OutputData: []byte(`{
  "items": [{
    "metadata": {"namespace": "mcp-team-acme", "name": "acme-tools"},
    "spec": {"template": {"spec": {"containers": [{
      "name": "mcp-gateway",
      "env": [
        {"name": "ANALYTICS_INGEST_URL", "value": "http://ingest/events"},
        {"name": "ANALYTICS_API_KEY", "valueFrom": {"secretKeyRef": {"name": "acme-tools-analytics-creds", "key": "api-key"}}}
      ]
    }]}}}
  }]
}`)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkGatewayAnalyticsCredentials(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckTraefikIngressClass(t *testing.T) {
	t.Run("ok when ingressClass exists", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("traefik")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikIngressClass(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when ingressClass is missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("not found")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikIngressClass(kubectl)
		if check.OK {
			t.Fatal("expected failure when ingressClass missing")
		}
	})
}

func TestCheckTraefikWebEntrypoint(t *testing.T) {
	t.Run("ok when service exposes named web entrypoint", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("web:8000:32080\nwebsecure:8443:32443\n")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikWebEntrypoint(kubectl, DistroGeneric)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("ok with k3s bundled traefik service", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "kube-system"):
					return &core.MockCommand{OutputData: []byte("web:80:0\nwebsecure:443:0\n")}
				case contains(spec.Args, "traefik"):
					return &core.MockCommand{OutputErr: errors.New("not found")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikWebEntrypoint(kubectl, DistroK3s)
		if !check.OK {
			t.Fatalf("expected OK for k3s bundled Traefik, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "k3s bundled Traefik") {
			t.Fatalf("detail should mention k3s bundled Traefik, got %q", check.Detail)
		}
	})

	t.Run("fails when service does not expose web entrypoint", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("admin:9000:32090\n")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikWebEntrypoint(kubectl, DistroGeneric)
		if check.OK {
			t.Fatal("expected failure when web entrypoint is not exposed")
		}
	})
}

func TestReadTraefikServicePortsWrapsCommandArgsError(t *testing.T) {
	cause := errors.New("validator rejected command")
	kubectl := core.NewTestKubectlClientWithValidators(
		&core.MockExecutor{},
		[]core.ExecValidator{
			func(core.ExecSpec) error {
				return cause
			},
		},
	)

	_, err := readTraefikServicePorts(kubectl, doctorTraefikEndpoint{Namespace: "traefik", Name: "traefik"})
	if !errors.Is(err, cause) {
		t.Fatalf("readTraefikServicePorts() error = %v, want errors.Is(..., cause)", err)
	}
}

func TestCheckPublicIngressHostConfig(t *testing.T) {
	t.Run("ok when all three hosts resolve from platform domain", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "mcpruntime.org")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
		t.Setenv("MCP_MCP_INGRESS_HOST", "")
		check := checkPublicIngressHostConfig()
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "platform=platform.mcpruntime.org") {
			t.Fatalf("expected derived platform host in detail, got %q", check.Detail)
		}
	})

	t.Run("fails when only some hosts are configured", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "platform.mcpruntime.org")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
		t.Setenv("MCP_MCP_INGRESS_HOST", "")
		check := checkPublicIngressHostConfig()
		if check.OK {
			t.Fatal("expected failure when MCP host is missing")
		}
		if !strings.Contains(check.Detail, "missing mcp") {
			t.Fatalf("expected missing MCP host detail, got %q", check.Detail)
		}
	})

	t.Run("skips default registry host when no public host env is configured", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
		t.Setenv("MCP_MCP_INGRESS_HOST", "")
		t.Setenv("MCP_REGISTRY_HOST", "")
		t.Setenv("MCP_REGISTRY_ENDPOINT", "")
		check := checkPublicIngressHostConfig()
		if !check.OK {
			t.Fatalf("expected skip/OK, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "skipping host-specific preflight") {
			t.Fatalf("expected skip detail, got %q", check.Detail)
		}
	})
}

func TestCheckPublicIngressDNS(t *testing.T) {
	origLookup := doctorLookupHost
	t.Cleanup(func() { doctorLookupHost = origLookup })

	t.Run("ok when hosts resolve", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "platform.mcpruntime.org")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
		t.Setenv("MCP_MCP_INGRESS_HOST", "mcp.mcpruntime.org")
		doctorLookupHost = func(_ context.Context, host string) ([]string, error) {
			return []string{"1.2.3.4"}, nil
		}
		check := checkPublicIngressDNS()
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "platform.mcpruntime.org -> 1.2.3.4") {
			t.Fatalf("expected resolved host in detail, got %q", check.Detail)
		}
	})

	t.Run("fails when host does not resolve", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "platform.mcpruntime.org")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
		t.Setenv("MCP_MCP_INGRESS_HOST", "mcp.mcpruntime.org")
		doctorLookupHost = func(_ context.Context, host string) ([]string, error) {
			if host == "registry.mcpruntime.org" {
				return nil, errors.New("no such host")
			}
			return []string{"1.2.3.4"}, nil
		}
		check := checkPublicIngressDNS()
		if check.OK {
			t.Fatal("expected failure when a host does not resolve")
		}
		if !strings.Contains(check.Detail, "registry.mcpruntime.org") {
			t.Fatalf("expected failing host in detail, got %q", check.Detail)
		}
	})

	t.Run("uses bounded lookup context", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "platform.mcpruntime.org")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "registry.mcpruntime.org")
		t.Setenv("MCP_MCP_INGRESS_HOST", "mcp.mcpruntime.org")
		doctorLookupHost = func(ctx context.Context, host string) ([]string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("expected lookup context deadline for %s", host)
			}
			if time.Until(deadline) <= 0 {
				t.Fatalf("expected future deadline for %s", host)
			}
			return []string{"1.2.3.4"}, nil
		}
		check := checkPublicIngressDNS()
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})
}

func TestCheckCertManagerReadiness(t *testing.T) {
	t.Run("skips when TLS preflight is not requested", func(t *testing.T) {
		t.Setenv("MCP_PLATFORM_DOMAIN", "")
		t.Setenv("MCP_PLATFORM_INGRESS_HOST", "")
		t.Setenv("MCP_REGISTRY_INGRESS_HOST", "")
		t.Setenv("MCP_MCP_INGRESS_HOST", "")
		t.Setenv("MCP_ACME_EMAIL", "")
		t.Setenv("MCP_TLS_CLUSTER_ISSUER", "")
		kubectl := core.NewTestKubectlClient(&core.MockExecutor{})
		check := checkCertManagerReadiness(kubectl)
		if !check.OK {
			t.Fatalf("expected skip/OK, got detail=%q", check.Detail)
		}
	})

	t.Run("ok when deployments are ready", func(t *testing.T) {
		t.Setenv("MCP_ACME_EMAIL", "test@example.com")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("1/1")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkCertManagerReadiness(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when a deployment is not ready", func(t *testing.T) {
		t.Setenv("MCP_ACME_EMAIL", "test@example.com")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "cert-manager-webhook") {
					return &core.MockCommand{OutputData: []byte("0/1")}
				}
				return &core.MockCommand{OutputData: []byte("1/1")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkCertManagerReadiness(kubectl)
		if check.OK {
			t.Fatal("expected failure when webhook is not ready")
		}
		if !strings.Contains(check.Detail, "cert-manager-webhook") {
			t.Fatalf("expected failing deployment in detail, got %q", check.Detail)
		}
	})
}

func TestCheckDoctorACMEHTTP01Exposure(t *testing.T) {
	t.Run("skips when ACME email is not set", func(t *testing.T) {
		t.Setenv("MCP_ACME_EMAIL", "")
		kubectl := core.NewTestKubectlClient(&core.MockExecutor{})
		check := checkDoctorACMEHTTP01Exposure(kubectl, DistroGeneric)
		if !check.OK {
			t.Fatalf("expected skip/OK, got detail=%q", check.Detail)
		}
	})

	t.Run("ok when k3s traefik exposes port 80", func(t *testing.T) {
		t.Setenv("MCP_ACME_EMAIL", "test@example.com")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "jsonpath={range .spec.ports[*]}{.name}:{.port}:{.nodePort}{\"\\n\"}{end}") && contains(spec.Args, "kube-system"):
					return &core.MockCommand{OutputData: []byte("web:80:0\nwebsecure:443:0\n")}
				case contains(spec.Args, "jsonpath={.spec.type}|{.status.loadBalancer.ingress[0].ip}|{.status.loadBalancer.ingress[0].hostname}|{range .spec.ports[*]}{.name}:{.port}:{.nodePort}{\",\"}{end}") && contains(spec.Args, "kube-system"):
					return &core.MockCommand{OutputData: []byte("LoadBalancer|1.2.3.4||web:80:0,websecure:443:0,")}
				}
				return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkDoctorACMEHTTP01Exposure(kubectl, DistroK3s)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when service web port is not 80", func(t *testing.T) {
		t.Setenv("MCP_ACME_EMAIL", "test@example.com")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("web:8000:32080\nwebsecure:8443:32443\n")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkDoctorACMEHTTP01Exposure(kubectl, DistroGeneric)
		if check.OK {
			t.Fatal("expected failure when web service port is not 80")
		}
		if !strings.Contains(check.Detail, "service port 8000") {
			t.Fatalf("expected port detail, got %q", check.Detail)
		}
	})
}

func TestCheckRegistryReachableFromCluster(t *testing.T) {
	t.Run("ok on HTTP 200", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case len(spec.Args) > 0 && spec.Args[0] == "logs":
					return &core.MockCommand{OutputData: []byte("HTTP/1.1 200 OK\nDocker-Distribution-Api-Version: registry/2.0\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryReachableFromCluster(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("reads completed pod logs instead of attach output", func(t *testing.T) {
		var runArgs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case len(spec.Args) > 0 && spec.Args[0] == "run":
					runArgs = append([]string(nil), spec.Args...)
					return &core.MockCommand{}
				case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case len(spec.Args) > 0 && spec.Args[0] == "logs":
					return &core.MockCommand{OutputData: []byte("HTTP/1.1 200 OK\n")}
				}
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 200 OK\nDocker-Distribution-Api-Version: registry/2.0\n")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryReachableFromCluster(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		for _, notWant := range []string{"--attach", "--rm"} {
			if contains(runArgs, notWant) {
				t.Fatalf("registry reachability probe should read completed pod logs instead of using %s, got args=%v", notWant, runArgs)
			}
		}
		overrides := argValueWithPrefix(runArgs, "--overrides=")
		if overrides == "" {
			t.Fatalf("registry reachability probe should use restricted-compliant overrides, got args=%v", runArgs)
		}
		if !strings.Contains(overrides, "registry.registry.svc.cluster.local:5000/v2/") {
			t.Fatalf("registry reachability override missing registry URL: %s", overrides)
		}
	})

	t.Run("fails on non-200", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case len(spec.Args) > 0 && spec.Args[0] == "logs":
					return &core.MockCommand{OutputData: []byte("HTTP/1.1 503 Service Unavailable\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryReachableFromCluster(kubectl)
		if check.OK {
			t.Fatal("expected failure for non-200")
		}
	})

	t.Run("does not false-pass when body includes non-status 200", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case len(spec.Args) > 0 && spec.Args[0] == "logs":
					return &core.MockCommand{OutputData: []byte("diagnostic: 200 retries\nHTTP/1.1 503 Service Unavailable\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryReachableFromCluster(kubectl)
		if check.OK {
			t.Fatal("expected failure when HTTP status line is not 200")
		}
	})

	t.Run("fails when helper pod errors", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("pod failed"), RunErr: errors.New("pod failed")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryReachableFromCluster(kubectl)
		if check.OK {
			t.Fatal("expected failure when helper pod errors")
		}
	})
}

func TestParseImagePullCandidates(t *testing.T) {
	sep := imagePullListSep
	out := strings.Join([]string{
		"mcp-sentinel|mcp-sentinel-api-abc|10.96.64.95:5000/mcp-sentinel-api:latest" + sep + "|ImagePullBackOff" + sep + "|",
		"mcp-runtime|operator-abc|registry.registry.svc.cluster.local:5000/mcp-runtime-operator:latest" + sep + "|Running" + sep + "|",
		"registry|registry-abc|registry.local/distribution:latest" + sep + "|ErrImagePull" + sep + "|",
		"mcp-servers|demo-init-abc|registry.local/bootstrap:latest" + sep + "registry.local/demo:latest" + sep + "|ImagePullBackOff" + sep + "|",
	}, "\n")

	candidates := parseImagePullCandidates(out)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 image pull candidates, got %d", len(candidates))
	}
	if candidates[0].Namespace != "mcp-sentinel" || candidates[0].Name != "mcp-sentinel-api-abc" {
		t.Fatalf("unexpected first candidate: %#v", candidates[0])
	}
	if candidates[0].Images[0] != "10.96.64.95:5000/mcp-sentinel-api:latest" {
		t.Fatalf("expected ClusterIP registry image, got %q", candidates[0].Images[0])
	}
	if candidates[1].Images[0] != "registry.local/distribution:latest" {
		t.Fatalf("expected host registry image, got %q", candidates[1].Images[0])
	}
	if candidates[2].Images[0] != "registry.local/bootstrap:latest" || candidates[2].Reasons[0] != "ImagePullBackOff" {
		t.Fatalf("expected init-container pull candidate, got %#v", candidates[2])
	}
}

func TestParseImagePullCandidatesPreservesCommasInMessages(t *testing.T) {
	// Real kubelet messages contain commas; the unit-separator delimiter
	// must keep them intact rather than fragmenting the message.
	sep := imagePullListSep
	msg := `failed to pull image "registry.local/demo:latest": rpc error: code = Unknown, desc = failed to do request: ` + registryHTTPPullMismatch
	line := "mcp-servers|demo-abc|registry.local/demo:latest" + sep + "|ImagePullBackOff" + sep + "|" + msg + sep
	candidates := parseImagePullCandidates(line)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if len(candidates[0].Messages) != 1 || candidates[0].Messages[0] != msg {
		t.Fatalf("expected message preserved verbatim, got %#v", candidates[0].Messages)
	}
	if !hasRegistryHTTPPullMismatchMessage(candidates[0].Messages) {
		t.Fatal("expected mismatch substring detected in unsplit message")
	}
}

func TestCheckRegistryServiceIPImageRefs(t *testing.T) {
	t.Run("fails when MCPServer image uses registry Service IP", func(t *testing.T) {
		servers := strings.Join([]string{
			"mcp-team-acme|acme-tools|10.43.174.51:5000/acme/acme-tools",
			"mcp-servers|ok|registry.mcpruntime.org/public/ok",
		}, "\n")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "svc") && contains(spec.Args, "registry"):
					return &core.MockCommand{OutputData: []byte("10.43.174.51")}
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte(servers)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryServiceIPImageRefs(kubectl)
		if check.OK {
			t.Fatal("expected Service IP image reference to fail")
		}
		for _, want := range []string{"mcp-team-acme/acme-tools", "10.43.174.51:5000/acme/acme-tools", "ClusterIP"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
		if !strings.Contains(check.Remedy, "registry.<domain>") {
			t.Fatalf("remedy should mention pullable registry host, got %q", check.Remedy)
		}
	})

	t.Run("passes when image refs use pullable hosts", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "svc") && contains(spec.Args, "registry"):
					return &core.MockCommand{OutputData: []byte("10.43.174.51")}
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("mcp-servers|ok|registry.mcpruntime.org/public/ok\n")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryServiceIPImageRefs(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckMCPServerImagePullSecrets(t *testing.T) {
	t.Run("jsonpath emits secret names", func(t *testing.T) {
		path := buildMCPServerPullSecretJSONPath()
		if !strings.Contains(path, "{.name}") {
			t.Fatalf("expected imagePullSecrets jsonpath to emit names, got %q", path)
		}
		if strings.Contains(path, "{@}") {
			t.Fatalf("imagePullSecrets jsonpath should not emit full object values: %q", path)
		}
	})

	t.Run("fails when referenced secret is missing", func(t *testing.T) {
		servers := "mcp-team-acme|acme-tools|registry-pull-admin" + imagePullListSep + "\n"
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte(servers)}
				case contains(spec.Args, "secret") && contains(spec.Args, "registry-pull-admin"):
					return &core.MockCommand{OutputErr: errors.New("not found")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkMCPServerImagePullSecrets(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected missing imagePullSecret to fail")
		}
		for _, want := range []string{"mcp-team-acme/acme-tools", "registry-pull-admin"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
	})

	t.Run("passes when referenced secrets exist", func(t *testing.T) {
		servers := "mcp-team-acme|acme-tools|registry-pull-admin" + imagePullListSep + "\n"
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte(servers)}
				case contains(spec.Args, "secret") && contains(spec.Args, "registry-pull-admin"):
					return &core.MockCommand{OutputData: []byte("registry-pull-admin")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkMCPServerImagePullSecrets(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckManagedTeamRegistryPullSecrets(t *testing.T) {
	dockerCfg := base64.StdEncoding.EncodeToString([]byte(`{"auths":{"registry.mcpruntime.org":{"auth":"abc"}}}`))

	t.Run("fails when managed team secret is missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespaces"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "secret") && contains(spec.Args, doctorManagedTeamRegistryPullSecret):
					return &core.MockCommand{OutputErr: errors.New("not found")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkManagedTeamRegistryPullSecrets(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected missing pull secret to fail")
		}
		if !strings.Contains(check.Detail, "mcp-team-acme") || !strings.Contains(check.Detail, doctorManagedTeamRegistryPullSecret) {
			t.Fatalf("detail = %q, want namespace and secret name", check.Detail)
		}
	})

	t.Run("passes when managed team secret is valid dockerconfigjson", func(t *testing.T) {
		secretJSON := fmt.Sprintf(`{"type":"kubernetes.io/dockerconfigjson","data":{".dockerconfigjson":%q}}`, dockerCfg)
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespaces"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\nmcp-team-globex\n")}
				case contains(spec.Args, "secret") && contains(spec.Args, doctorManagedTeamRegistryPullSecret):
					return &core.MockCommand{OutputData: []byte(secretJSON)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkManagedTeamRegistryPullSecrets(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckManagedTeamWorkloadServiceAccounts(t *testing.T) {
	t.Run("fails when serviceaccount is missing pull secret", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespaces"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "serviceaccount"):
					return &core.MockCommand{OutputData: []byte(`{"imagePullSecrets":[{"name":"something-else"}]}`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkManagedTeamWorkloadServiceAccounts(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected missing serviceaccount pull secret to fail")
		}
		if !strings.Contains(check.Detail, kubeworkload.DefaultServiceAccountName) || !strings.Contains(check.Detail, doctorManagedTeamRegistryPullSecret) {
			t.Fatalf("detail = %q, want serviceaccount and secret names", check.Detail)
		}
	})

	t.Run("passes when serviceaccount is wired correctly", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespaces"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "serviceaccount"):
					return &core.MockCommand{OutputData: []byte(`{"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]}`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkManagedTeamWorkloadServiceAccounts(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckOperatorRegistryEndpoint(t *testing.T) {
	t.Run("passes when endpoint matches ingress host", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(`{
  "spec":{"template":{"spec":{"containers":[{"env":[
    {"name":"MCP_REGISTRY_ENDPOINT","value":"registry.mcpruntime.org"},
    {"name":"MCP_REGISTRY_INGRESS_HOST","value":"registry.mcpruntime.org"}
  ]}]}}}
}`)}
			},
		}
		check := checkOperatorRegistryEndpoint(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected public hostname endpoint to pass, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
		if !strings.Contains(check.Detail, "matches MCP_REGISTRY_INGRESS_HOST") {
			t.Fatalf("detail = %q, want ingress host hint", check.Detail)
		}
	})

	t.Run("passes when endpoint is internal", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(`{
  "spec":{"template":{"spec":{"containers":[{"env":[
    {"name":"MCP_REGISTRY_ENDPOINT","value":"10.43.69.247:5000"},
    {"name":"MCP_REGISTRY_INGRESS_HOST","value":"registry.mcpruntime.org"}
  ]}]}}}
}`)}
			},
		}
		check := checkOperatorRegistryEndpoint(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckSentinelPipelineReadiness(t *testing.T) {
	t.Run("kafka fails when statefulset is not ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-sentinel")}
				case contains(spec.Args, "statefulset"):
					return &core.MockCommand{OutputData: []byte("0/1")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkSentinelKafkaReadiness(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected kafka readiness failure")
		}
		if !strings.Contains(check.Detail, "0/1") {
			t.Fatalf("detail = %q, want replica status", check.Detail)
		}
	})

	t.Run("ingest passes when deployment is ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-sentinel")}
				case contains(spec.Args, "mcp-sentinel-ingest"):
					return &core.MockCommand{OutputData: []byte("1/1")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkSentinelIngestReadiness(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckRuntimeAPIImageDisplayRefs(t *testing.T) {
	uiKeyB64 := base64.StdEncoding.EncodeToString([]byte("ui-key"))

	t.Run("fails when runtime API leaks internal registry refs", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-sentinel")}
				case contains(spec.Args, "secret") && contains(spec.Args, "mcp-sentinel-secrets"):
					return &core.MockCommand{OutputData: []byte(uiKeyB64)}
				case contains(spec.Args, "run"):
					return &core.MockCommand{OutputData: []byte("pod created")}
				case contains(spec.Args, "get") && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case contains(spec.Args, "logs"):
					return &core.MockCommand{OutputData: []byte(`{"items":[{"image":"10.43.69.247:5000/core/go-example:latest"}]}
HTTP_STATUS=200`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkRuntimeAPIImageDisplayRefs(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected internal image leak to fail")
		}
		if !strings.Contains(check.Detail, "internal registry reference") {
			t.Fatalf("detail = %q, want leak detail", check.Detail)
		}
	})

	t.Run("passes when runtime API only shows public refs", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-sentinel")}
				case contains(spec.Args, "secret") && contains(spec.Args, "mcp-sentinel-secrets"):
					return &core.MockCommand{OutputData: []byte(uiKeyB64)}
				case contains(spec.Args, "run"):
					return &core.MockCommand{OutputData: []byte("pod created")}
				case contains(spec.Args, "get") && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case contains(spec.Args, "logs"):
					return &core.MockCommand{OutputData: []byte(`{"items":[{"image":"registry.mcpruntime.org/core/go-example:latest"}]}
HTTP_STATUS=200`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkRuntimeAPIImageDisplayRefs(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
	})
}

func TestCheckRegistryHTTPPullMismatch(t *testing.T) {
	sep := imagePullListSep

	t.Run("reports HTTP registry mismatch from describe pod events", func(t *testing.T) {
		pods := "mcp-sentinel|mcp-sentinel-api-abc|10.96.64.95:5000/mcp-sentinel-api:latest" + sep + "|ImagePullBackOff" + sep + "|\n"
		describe := `Name:             mcp-sentinel-api-abc
Namespace:        mcp-sentinel
Events:
  Type     Reason     Age   From               Message
  ----     ------     ----  ----               -------
  Warning  Failed     31s   kubelet            Failed to pull image "10.96.64.95:5000/mcp-sentinel-api:latest": failed to resolve reference "10.96.64.95:5000/mcp-sentinel-api:latest": failed to do request: Head "https://10.96.64.95:5000/v2/mcp-sentinel-api/manifests/latest": http: server gave HTTP response to HTTPS client
`
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "pods") && contains(spec.Args, "-A"):
					return &core.MockCommand{OutputData: []byte(pods)}
				case contains(spec.Args, "describe"):
					return &core.MockCommand{OutputData: []byte(describe)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if check.OK {
			t.Fatal("expected registry HTTP mismatch to fail")
		}
		for _, want := range []string{"mcp-sentinel/mcp-sentinel-api-abc", "10.96.64.95:5000/mcp-sentinel-api:latest", "(ImagePullBackOff)", registryHTTPPullMismatch} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
		for _, want := range []string{"insecure registry", "container runtime", "exact image host"} {
			if !strings.Contains(check.Remedy, want) {
				t.Fatalf("remedy should contain %q, got %q", want, check.Remedy)
			}
		}
	})

	t.Run("reports HTTP registry mismatch from init container pull events", func(t *testing.T) {
		pods := "mcp-servers|demo-init-abc|registry.local/bootstrap:latest" + sep + "registry.local/demo:latest" + sep + "|Init:ImagePullBackOff" + sep + "|\n"
		describe := `Name:             demo-init-abc
Namespace:        mcp-servers
Init Containers:
  bootstrap:
    Image:          registry.local/bootstrap:latest
Events:
  Type     Reason     Age   From               Message
  ----     ------     ----  ----               -------
  Warning  Failed     31s   kubelet            Failed to pull image "registry.local/bootstrap:latest": failed to do request: Head "https://registry.local/v2/bootstrap/manifests/latest": http: server gave HTTP response to HTTPS client
`
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "pods") && contains(spec.Args, "-A"):
					return &core.MockCommand{OutputData: []byte(pods)}
				case contains(spec.Args, "describe"):
					return &core.MockCommand{OutputData: []byte(describe)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if check.OK {
			t.Fatal("expected registry HTTP mismatch for init container to fail")
		}
		for _, want := range []string{"mcp-servers/demo-init-abc", "registry.local/bootstrap:latest", "(Init:ImagePullBackOff)", registryHTTPPullMismatch} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
	})

	t.Run("reports HTTP registry mismatch from waiting status message", func(t *testing.T) {
		// Reasons field is empty here; only the waiting message identifies
		// the candidate. Detail should still surface the mismatch but won't
		// include a reason in parentheses.
		msg := `failed to pull image "registry.local/bootstrap:latest": failed to do request: Head "https://registry.local/v2/bootstrap/manifests/latest": http: server gave HTTP response to HTTPS client`
		pods := "mcp-servers|demo-status-abc|registry.local/bootstrap:latest" + sep + "registry.local/demo:latest" + sep + "||" + msg + sep
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					t.Fatalf("did not expect describe call when waiting status message has mismatch")
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if check.OK {
			t.Fatal("expected registry HTTP mismatch from status message to fail")
		}
		for _, want := range []string{"mcp-servers/demo-status-abc", "registry.local/bootstrap:latest", registryHTTPPullMismatch} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
		if strings.Contains(check.Detail, "()") {
			t.Fatalf("detail should omit empty reason parentheses, got %q", check.Detail)
		}
	})

	t.Run("passes when pull failures do not include HTTP mismatch event", func(t *testing.T) {
		pods := "mcp-servers|demo-abc|registry.local/demo:latest" + sep + "|ErrImagePull" + sep + "|\n"
		describe := `Events:
  Warning  Failed  kubelet  Failed to pull image "registry.local/demo:latest": not found
`
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					return &core.MockCommand{OutputData: []byte(describe)}
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if !check.OK {
			t.Fatalf("expected OK when no HTTP mismatch event exists, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when pod inspection fails for pull candidates", func(t *testing.T) {
		pods := strings.Join([]string{
			"mcp-servers|demo-a|registry.local/demo-a:latest" + sep + "|ErrImagePull" + sep + "|",
			"mcp-servers|demo-b|registry.local/demo-b:latest" + sep + "|ImagePullBackOff" + sep + "|",
		}, "\n")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					return &core.MockCommand{OutputErr: errors.New("pods is forbidden")}
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if check.OK {
			t.Fatal("expected registry HTTP mismatch check to fail when pod inspection fails")
		}
		for _, want := range []string{"pod inspection failed", "2/2", "mcp-servers/demo-a", "pods is forbidden"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
		if !strings.Contains(check.Remedy, "kubectl describe pod") {
			t.Fatalf("remedy should mention manual describe, got %q", check.Remedy)
		}
	})

	t.Run("caps describe fallback when many ImagePullBackOff pods exist", func(t *testing.T) {
		// Build imagePullDescribeLimit + 5 candidates whose waiting messages
		// don't match. Only the first imagePullDescribeLimit should trigger
		// describe calls.
		var lines []string
		for i := range imagePullDescribeLimit + 5 {
			lines = append(lines, fmt.Sprintf("ns%d|pod%d|registry.local/x:latest"+sep+"|ImagePullBackOff"+sep+"|", i, i))
		}
		pods := strings.Join(lines, "\n")
		describeCalls := 0
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					describeCalls++
					return &core.MockCommand{OutputData: []byte("Events:\n  Warning Failed kubelet Failed to pull image: not found\n")}
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkRegistryHTTPPullMismatch(kubectl)
		if !check.OK {
			t.Fatalf("expected OK when no candidates have HTTP mismatch, got detail=%q", check.Detail)
		}
		if describeCalls != imagePullDescribeLimit {
			t.Fatalf("expected describe calls capped at %d, got %d", imagePullDescribeLimit, describeCalls)
		}
		if !strings.Contains(check.Detail, fmt.Sprintf("inspected first %d", imagePullDescribeLimit)) {
			t.Fatalf("detail should mention capped inspection, got %q", check.Detail)
		}
	})
}

func TestCheckRegistryImagePullDiagnostics(t *testing.T) {
	sep := imagePullListSep

	t.Run("reports TLS SAN pull failures from waiting message", func(t *testing.T) {
		msg := `failed to pull image "10.43.174.51:5000/acme/acme-tools:v0.1.0": Head "https://10.43.174.51:5000/v2/acme/acme-tools/manifests/v0.1.0": x509: cannot validate certificate for 10.43.174.51 because it doesn't contain any IP SANs`
		pods := "mcp-team-acme|acme-tools-abc|10.43.174.51:5000/acme/acme-tools:v0.1.0" + sep + "|ImagePullBackOff" + sep + "|" + msg + sep
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					t.Fatal("did not expect describe when waiting message has diagnostic")
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		check := checkRegistryImagePullDiagnostics(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected TLS diagnostic to fail")
		}
		for _, want := range []string{"TLS", "10.43.174.51:5000/acme/acme-tools:v0.1.0", "IP SANs"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
		if !strings.Contains(check.Remedy, "public registry hostname") {
			t.Fatalf("remedy should mention public registry hostname, got %q", check.Remedy)
		}
	})

	t.Run("reports auth pull failures from describe events", func(t *testing.T) {
		pods := "mcp-team-acme|acme-tools-abc|registry.mcpruntime.org/acme/acme-tools:v0.1.0" + sep + "|ErrImagePull" + sep + "|\n"
		describe := `Events:
  Warning  Failed  kubelet  Failed to pull image "registry.mcpruntime.org/acme/acme-tools:v0.1.0": no basic auth credentials
`
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					return &core.MockCommand{OutputData: []byte(describe)}
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		check := checkRegistryImagePullDiagnostics(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected auth diagnostic to fail")
		}
		for _, want := range []string{"auth", "no basic auth credentials", "imagePullSecrets"} {
			if !strings.Contains(check.Detail+check.Remedy, want) {
				t.Fatalf("result should contain %q, detail=%q remedy=%q", want, check.Detail, check.Remedy)
			}
		}
	})

	t.Run("passes when no known diagnostic matches", func(t *testing.T) {
		pods := "mcp-team-acme|acme-tools-abc|registry.mcpruntime.org/acme/acme-tools:v0.1.0" + sep + "|ErrImagePull" + sep + "|\n"
		describe := `Events:
  Warning  Failed  kubelet  Failed to pull image "registry.mcpruntime.org/acme/acme-tools:v0.1.0": context deadline exceeded
`
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "describe") {
					return &core.MockCommand{OutputData: []byte(describe)}
				}
				return &core.MockCommand{OutputData: []byte(pods)}
			},
		}
		check := checkRegistryImagePullDiagnostics(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK when no known diagnostic matches, got detail=%q", check.Detail)
		}
	})
}

func TestDoctorPostSetupCheckSpecsAreOperationalBasics(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{}
		},
	})
	specs := doctorPostSetupCheckSpecs(kubectl)
	want := []string{
		"Kubernetes nodes ready",
		"persistent volume claims",
		"pending pods",
		"sentinel secrets",
		"sentinel Postgres credential drift",
		"sentinel platform API readiness",
		"sentinel workload rollout health",
		"sentinel API auth probe",
	}
	if len(specs) != len(want) {
		t.Fatalf("post-setup smoke check count = %d, want %d", len(specs), len(want))
	}
	for i, name := range want {
		if specs[i].Name != name {
			t.Fatalf("post-setup smoke check[%d] = %q, want %q", i, specs[i].Name, name)
		}
	}
}

func TestRunDoctorAggregates(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
				return &core.MockCommand{OutputData: []byte("v1.34.6+k3s1")}
			case contains(spec.Args, "namespace mcp-servers"):
				return &core.MockCommand{OutputData: []byte("mcp-servers")}
			case contains(spec.Args, "crd mcpservers.mcpruntime.org"):
				return &core.MockCommand{OutputData: []byte("mcpservers.mcpruntime.org")}
			case contains(spec.Args, "mcp-runtime-operator-controller-manager"):
				return &core.MockCommand{OutputData: []byte("1/1")}
			case contains(spec.Args, "ingressclass traefik"):
				return &core.MockCommand{OutputData: []byte("traefik")}
			case contains(spec.Args, "svc -n traefik traefik"):
				return &core.MockCommand{OutputData: []byte("web:8000:32080\n")}
			case contains(spec.Args, "jsonpath={.spec.ports[0].nodePort}"):
				return &core.MockCommand{OutputData: []byte("32000")}
			case contains(spec.Args, "get") && contains(spec.Args, "pod") && argContains(spec.Args, "imageID"):
				return &core.MockCommand{OutputData: []byte("docker-pullable://registry.k8s.io/pause@sha256:test")}
			case contains(spec.Args, "get") && contains(spec.Args, "pods") && argContains(spec.Args, "spec.nodeName"):
				return &core.MockCommand{OutputData: []byte("node-a")}
			case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "jsonpath={.status.phase}"):
				return &core.MockCommand{OutputData: []byte("Succeeded")}
			case len(spec.Args) > 0 && spec.Args[0] == "logs":
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 503 Service Unavailable\n")}
			case contains(spec.Args, "curl"):
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 503 Service Unavailable\n")}
			}
			return &core.MockCommand{}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	report := RunDoctor(kubectl)
	if report.Distribution != DistroK3s {
		t.Fatalf("expected DistroK3s, got %q", report.Distribution)
	}
	if report.AllOK() {
		t.Fatal("expected at least one failing check (registry reachability 503)")
	}
	if len(report.Checks) < 7 {
		t.Fatalf("expected multiple checks, got %d", len(report.Checks))
	}
}

func TestRunDoctorWithProgressReportsEachCheck(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
				return &core.MockCommand{OutputData: []byte("v1.34.6+k3s1")}
			case contains(spec.Args, "namespace mcp-servers"):
				return &core.MockCommand{OutputData: []byte("mcp-servers")}
			case contains(spec.Args, "crd mcpservers.mcpruntime.org"):
				return &core.MockCommand{OutputData: []byte("mcpservers.mcpruntime.org")}
			case contains(spec.Args, "mcp-runtime-operator-controller-manager"):
				return &core.MockCommand{OutputData: []byte("1/1")}
			case contains(spec.Args, "ingressclass traefik"):
				return &core.MockCommand{OutputData: []byte("traefik")}
			case contains(spec.Args, "svc -n traefik traefik"):
				return &core.MockCommand{OutputData: []byte("web:8000:32080\n")}
			case contains(spec.Args, "jsonpath={.spec.ports[0].nodePort}"):
				return &core.MockCommand{OutputData: []byte("32000")}
			case contains(spec.Args, "get") && contains(spec.Args, "pod") && argContains(spec.Args, "imageID"):
				return &core.MockCommand{OutputData: []byte("docker-pullable://registry.k8s.io/pause@sha256:test")}
			case contains(spec.Args, "get") && contains(spec.Args, "pods") && argContains(spec.Args, "spec.nodeName"):
				return &core.MockCommand{OutputData: []byte("node-a")}
			case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "jsonpath={.status.phase}"):
				return &core.MockCommand{OutputData: []byte("Succeeded")}
			case len(spec.Args) > 0 && spec.Args[0] == "logs":
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 503 Service Unavailable\n")}
			case contains(spec.Args, "curl"):
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 503 Service Unavailable\n")}
			}
			return &core.MockCommand{}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	var events []string
	report := RunDoctorWithProgress(kubectl, func(event DoctorCheckProgressEvent) func(DoctorCheck) {
		if event.Index <= 0 || event.Total <= 0 {
			t.Fatalf("progress event has invalid position: %+v", event)
		}
		if event.Detail == "" {
			t.Fatalf("progress event for %q should describe what the check is doing", event.Name)
		}
		events = append(events, "start:"+event.Name)
		return func(check DoctorCheck) {
			events = append(events, "finish:"+check.Name)
		}
	})

	if len(report.Checks) == 0 {
		t.Fatal("expected checks")
	}
	if len(events) != len(report.Checks)*2 {
		t.Fatalf("got %d progress events for %d checks", len(events), len(report.Checks))
	}
	for i, check := range report.Checks {
		start := events[i*2]
		finish := events[i*2+1]
		if !strings.HasPrefix(start, "start:") {
			t.Fatalf("event %d = %q, want start event", i*2, start)
		}
		if finish != "finish:"+check.Name {
			t.Fatalf("finish event for check %d = %q, want %q", i, finish, "finish:"+check.Name)
		}
	}
}

func TestDoctorCurlProbesPassPathValidator(t *testing.T) {
	validators := []core.ExecValidator{core.NoControlChars(), core.PathUnder("/workspace")}

	t.Run("ingress route probe", func(t *testing.T) {
		var probeArgs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case strings.Contains(strings.Join(spec.Args, "\x00"), "{.metadata.name}|{.spec.rules[0].host}|{.spec.rules[0].http.paths[0].path}"):
					return &core.MockCommand{OutputData: []byte("doctor-smoke-old||/doctor-smoke-old/mcp\ndemo||/demo/mcp\n")}
				case contains(spec.Args, "get") && contains(spec.Args, "svc"):
					return &core.MockCommand{OutputData: []byte("web:8000:32080\n")}
				case len(spec.Args) > 0 && spec.Args[0] == "run":
					probeArgs = append([]string(nil), spec.Args...)
					if strings.Contains(strings.Join(spec.Args, "\x00"), "/dev/null") {
						t.Fatal("doctor curl helper should not pass /dev/null through kubectl validators")
					}
					return &core.MockCommand{OutputData: []byte("pod created")}
				case contains(spec.Args, "get") && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case contains(spec.Args, "logs"):
					return &core.MockCommand{OutputData: []byte("200")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClientWithValidators(mock, validators)
		check := checkIngressRouteProbe(kubectl, "mcp-servers", DistroGeneric)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		for _, arg := range probeArgs {
			if arg == "--attach" || arg == "--rm" {
				t.Fatalf("ingress probe should read completed pod logs, got args=%v", probeArgs)
			}
		}
		overrides := argValueWithPrefix(probeArgs, "--overrides=")
		if overrides == "" {
			t.Fatalf("ingress route probe should use restricted-compliant overrides, got args=%v", probeArgs)
		}
		for _, want := range []string{
			`"allowPrivilegeEscalation":false`,
			`"runAsNonRoot":true`,
			`"runAsUser":65532`,
			`"seccompProfile":{"type":"RuntimeDefault"}`,
			`"command":["curl"]`,
		} {
			if !strings.Contains(overrides, want) {
				t.Fatalf("ingress route probe overrides missing %s: %s", want, overrides)
			}
		}
	})

	t.Run("sentinel API auth probe", func(t *testing.T) {
		var probeArgs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte(doctorSentinelNamespace)}
				case contains(spec.Args, "jsonpath={.data.UI_API_KEY}"):
					return &core.MockCommand{OutputData: []byte("dGVzdA==")}
				case len(spec.Args) > 0 && spec.Args[0] == "run":
					probeArgs = append([]string(nil), spec.Args...)
					if contains(spec.Args, "/dev/null") {
						t.Fatal("doctor curl helper should not pass /dev/null through kubectl validators")
					}
					return &core.MockCommand{OutputData: []byte("pod/doctor-sentinel-probe created\n")}
				case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "jsonpath={.status.phase}"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case len(spec.Args) > 0 && spec.Args[0] == "logs":
					return &core.MockCommand{OutputData: []byte("200")}
				case len(spec.Args) > 0 && spec.Args[0] == "delete":
					return &core.MockCommand{}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClientWithValidators(mock, validators)
		check := checkSentinelAPIAuthProbe(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		overrides := argValueWithPrefix(probeArgs, "--overrides=")
		if overrides == "" {
			t.Fatalf("sentinel auth probe should use restricted-compliant overrides, got args=%v", probeArgs)
		}
		for _, notWant := range []string{"--attach", "--rm"} {
			if contains(probeArgs, notWant) {
				t.Fatalf("sentinel auth probe should read completed pod logs instead of using %s, got args=%v", notWant, probeArgs)
			}
		}
	})
}

func TestCheckSentinelSecretsReportsInvalidBase64(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "namespace"):
				return &core.MockCommand{OutputData: []byte(doctorSentinelNamespace)}
			case contains(spec.Args, "jsonpath={.data.API_KEYS}"):
				return &core.MockCommand{OutputData: []byte("not-base64")}
			case contains(spec.Args, "jsonpath={.data.ADMIN_API_KEYS}"):
				return &core.MockCommand{OutputData: []byte("dGVzdA==")}
			case contains(spec.Args, "jsonpath={.data.INGEST_API_KEYS}"):
				return &core.MockCommand{OutputData: []byte("dGVzdA==")}
			case contains(spec.Args, "jsonpath={.data.UI_API_KEY}"):
				return &core.MockCommand{OutputData: []byte("dGVzdA==")}
			default:
				return &core.MockCommand{}
			}
		},
	}
	check := checkSentinelSecrets(core.NewTestKubectlClient(mock))
	if check.OK {
		t.Fatalf("expected invalid base64 to fail, got detail=%q", check.Detail)
	}
	if !strings.Contains(check.Detail, "API_KEYS") || !strings.Contains(check.Detail, "not valid base64") {
		t.Fatalf("expected invalid base64 detail to name the key, got %q", check.Detail)
	}
}

func TestCheckRuntimeAPIKubernetesAPIEgressUsesLiveEndpointPort(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "namespace"):
				return &core.MockCommand{OutputData: []byte(doctorSentinelNamespace)}
			case contains(spec.Args, "endpoints"):
				return &core.MockCommand{OutputData: []byte("9443\n")}
			case contains(spec.Args, "networkpolicy"):
				return &core.MockCommand{OutputData: []byte(`{"spec":{"egress":[{"to":[{"ipBlock":{"cidr":"0.0.0.0/0"}}],"ports":[{"port":443},{"port":9443}]}]}}`)}
			default:
				return &core.MockCommand{}
			}
		},
	}
	check := checkRuntimeAPIKubernetesAPIEgress(core.NewTestKubectlClient(mock))
	if !check.OK {
		t.Fatalf("expected non-standard live API port to pass, got detail=%q", check.Detail)
	}
	if !strings.Contains(check.Detail, "9443") {
		t.Fatalf("expected detail to name detected endpoint port, got %q", check.Detail)
	}
}

func TestCheckSentinelRuntimeCatalogProbeChecksServersAndTools(t *testing.T) {
	var runs int
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "namespace"):
				return &core.MockCommand{OutputData: []byte(doctorSentinelNamespace)}
			case contains(spec.Args, "jsonpath={.data.ADMIN_API_KEYS}"):
				return &core.MockCommand{OutputData: []byte("YWRtaW4=")}
			case contains(spec.Args, "jsonpath={.data.API_KEYS}"):
				return &core.MockCommand{OutputData: []byte("YWRtaW4=")}
			case len(spec.Args) > 0 && spec.Args[0] == "run":
				runs++
				return &core.MockCommand{OutputData: []byte("pod/doctor-sentinel-catalog created\n")}
			case contains(spec.Args, "jsonpath={.status.phase}"):
				return &core.MockCommand{OutputData: []byte("Succeeded")}
			case len(spec.Args) > 0 && spec.Args[0] == "logs":
				return &core.MockCommand{OutputData: []byte("200")}
			default:
				return &core.MockCommand{}
			}
		},
	}
	check := checkSentinelRuntimeCatalogProbe(core.NewTestKubectlClient(mock))
	if !check.OK {
		t.Fatalf("expected catalog probe to pass, got detail=%q", check.Detail)
	}
	if runs != 2 {
		t.Fatalf("expected servers and tools probes, got %d run pods", runs)
	}
}

func TestRemediationHintPerDistro(t *testing.T) {
	for _, d := range []Distribution{DistroK3s, DistroKind, DistroMinikube, DistroDockerDesktop, DistroGeneric} {
		hint := remediationHint(d)
		if hint == "" {
			t.Errorf("no remediation hint for %q", d)
		}
	}
}

func TestReportHasRegistryOrPullFailure(t *testing.T) {
	if reportHasRegistryOrPullFailure(DoctorReport{Checks: []DoctorCheck{{Name: "sentinel secrets", OK: false}}}) {
		t.Fatal("sentinel-only failures should not print registry remediation")
	}
	if !reportHasRegistryOrPullFailure(DoctorReport{Checks: []DoctorCheck{{Name: "registry HTTP pull mismatch", OK: false}}}) {
		t.Fatal("registry pull failures should print registry remediation")
	}
}

func TestCheckNamespacePodAdmission(t *testing.T) {
	t.Run("ok on dry-run success", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("pod/doctor-admission created (dry run)")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNamespacePodAdmission(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when admission rejects", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{
					OutputData: []byte("pods \"doctor-admission\" is forbidden: exceeds quota"),
					OutputErr:  errors.New("exit status 1"),
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNamespacePodAdmission(kubectl, "mcp-servers")
		if check.OK {
			t.Fatalf("expected failure when dry-run rejected; detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "forbidden") {
			t.Fatalf("expected server error passthrough in detail, got %q", check.Detail)
		}
		if check.Remedy == "" {
			t.Fatal("expected remedy hint")
		}
	})
}

func TestCheckTraefikDeploymentReady(t *testing.T) {
	cases := []struct {
		name   string
		output string
		outErr error
		wantOK bool
	}{
		{name: "ready 2/2", output: "2/2", wantOK: true},
		{name: "partially ready 1/3", output: "1/3", wantOK: false},
		{name: "zero desired", output: "0/0", wantOK: false},
		{name: "empty output", output: "", wantOK: false},
		{name: "malformed pair", output: "ready", wantOK: false},
		{name: "non-numeric ready", output: "x/2", wantOK: false},
		{name: "kubectl error", output: "", outErr: errors.New("not found"), wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{
				CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
					return &core.MockCommand{OutputData: []byte(tc.output), OutputErr: tc.outErr}
				},
			}
			kubectl := core.NewTestKubectlClient(mock)
			check := checkTraefikDeploymentReady(kubectl, DistroGeneric)
			if check.OK != tc.wantOK {
				t.Fatalf("OK=%v want %v; detail=%q", check.OK, tc.wantOK, check.Detail)
			}
		})
	}

	t.Run("ok with k3s bundled traefik deployment", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "kube-system"):
					return &core.MockCommand{OutputData: []byte("1/1")}
				case contains(spec.Args, "traefik"):
					return &core.MockCommand{OutputErr: errors.New("not found")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikDeploymentReady(kubectl, DistroK3s)
		if !check.OK {
			t.Fatalf("expected OK for k3s bundled Traefik, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "k3s bundled Traefik") {
			t.Fatalf("detail should mention k3s bundled Traefik, got %q", check.Detail)
		}
	})
}

func TestCheckTraefikServiceExposure(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		wantOK     bool
		wantDetail string
	}{
		{
			name:       "LoadBalancer with IP",
			output:     "LoadBalancer|10.1.2.3||8000:0,",
			wantOK:     true,
			wantDetail: "10.1.2.3",
		},
		{
			name:       "LoadBalancer with hostname only",
			output:     "LoadBalancer||lb.example.com|8000:0,",
			wantOK:     true,
			wantDetail: "lb.example.com",
		},
		{
			name:   "NodePort exposes web port",
			output: "NodePort|||8000:32080,8443:32443,",
			wantOK: true,
		},
		{
			name:   "LoadBalancer pending, no NodePort for web",
			output: "LoadBalancer|||9999:32099,",
			wantOK: false,
		},
		{
			name:   "ClusterIP only",
			output: "ClusterIP|||9999:0,",
			wantOK: false,
		},
		{
			name:   "malformed payload",
			output: "LoadBalancer",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{
				CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
					return &core.MockCommand{OutputData: []byte(tc.output)}
				},
			}
			kubectl := core.NewTestKubectlClient(mock)
			check := checkTraefikServiceExposure(kubectl, DistroGeneric)
			if check.OK != tc.wantOK {
				t.Fatalf("OK=%v want %v; detail=%q", check.OK, tc.wantOK, check.Detail)
			}
			if tc.wantDetail != "" && !strings.Contains(check.Detail, tc.wantDetail) {
				t.Fatalf("detail=%q does not contain %q", check.Detail, tc.wantDetail)
			}
		})
	}

	t.Run("ok on kind when LoadBalancer pending but traefik deployment ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				joined := strings.Join(spec.Args, " ")
				switch {
				case strings.Contains(joined, "jsonpath={.spec.type}"):
					return &core.MockCommand{OutputData: []byte("LoadBalancer|||web:8000:,websecure:8443:,")}
				case strings.Contains(joined, "jsonpath={.status.readyReplicas}"):
					return &core.MockCommand{OutputData: []byte("1/1")}
				default:
					return &core.MockCommand{}
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikServiceExposure(kubectl, DistroKind)
		if !check.OK {
			t.Fatalf("expected OK for kind pending LoadBalancer, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "port-forward") {
			t.Fatalf("detail should mention port-forward, got %q", check.Detail)
		}
	})

	t.Run("ok with k3s bundled traefik service", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "kube-system"):
					return &core.MockCommand{OutputData: []byte("LoadBalancer|10.1.2.3||web:80:0,websecure:443:0,")}
				case contains(spec.Args, "traefik"):
					return &core.MockCommand{OutputErr: errors.New("not found")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkTraefikServiceExposure(kubectl, DistroK3s)
		if !check.OK {
			t.Fatalf("expected OK for k3s bundled Traefik, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "k3s bundled Traefik") {
			t.Fatalf("detail should mention k3s bundled Traefik, got %q", check.Detail)
		}
	})
}

func TestCheckIngressLoadBalancerStatus(t *testing.T) {
	t.Run("fails when host based runtime ingress has no load balancer status", func(t *testing.T) {
		out := strings.Join([]string{
			"registry|registry|registry.mcpruntime.org,||",
			"mcp-sentinel|mcp-sentinel-platform-ui|platform.mcpruntime.org,|1.2.3.4|",
			"default|unrelated|example.com,||",
		}, "\n")
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(out)}
			},
		}
		check := checkIngressLoadBalancerStatus(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected missing registry ingress status to fail")
		}
		for _, want := range []string{"registry/registry", "registry.mcpruntime.org", "empty status.loadBalancer"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
	})

	t.Run("passes when host based runtime ingress status is published", func(t *testing.T) {
		out := "registry|registry|registry.mcpruntime.org,|1.2.3.4|\n"
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(out)}
			},
		}
		check := checkIngressLoadBalancerStatus(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("passes in permissive readiness mode for dev NodePort clusters", func(t *testing.T) {
		t.Setenv(doctorEnvIngressReadinessMode, doctorIngressReadinessPermissive)
		out := "registry|registry|registry.local,||\n"
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte(out)}
			},
		}
		check := checkIngressLoadBalancerStatus(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected permissive mode to pass, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "permissive ingress readiness mode") {
			t.Fatalf("detail should mention permissive mode, got %q", check.Detail)
		}
	})
}

func TestCheckPlatformAPILiveInventoryNetworkPolicy(t *testing.T) {
	t.Run("fails when team policy blocks platform API ingress", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "networkpolicy"):
					return &core.MockCommand{OutputData: []byte(`{
						"spec": {
							"ingress": [
								{"from": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}}}]}
							]
						}
					}`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkPlatformAPILiveInventoryNetworkPolicy(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected blocked platform API ingress to fail")
		}
		for _, want := range []string{"mcp-team-acme/platform-default-deny", "mcp-sentinel"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
	})

	t.Run("passes when team policy allows platform API ingress", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\nmcp-team-acme\nmcp-servers\n")}
				case contains(spec.Args, "networkpolicy"):
					return &core.MockCommand{OutputData: []byte(`{
						"spec": {
							"ingress": [
								{"from": [{
									"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "mcp-sentinel"}}
								}]}
							]
						}
					}`)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkPlatformAPILiveInventoryNetworkPolicy(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("passes when team policy is not present", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "networkpolicy"):
					return &core.MockCommand{
						OutputData: []byte(`Error from server (NotFound): networkpolicies.networking.k8s.io "platform-default-deny" not found`),
						OutputErr:  errors.New("exit status 1"),
					}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkPlatformAPILiveInventoryNetworkPolicy(core.NewTestKubectlClient(mock))
		if !check.OK {
			t.Fatalf("expected missing optional NetworkPolicy to pass, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when team policy cannot be read", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("mcp-team-acme\n")}
				case contains(spec.Args, "networkpolicy"):
					return &core.MockCommand{
						OutputData: []byte(`Error from server (Forbidden): networkpolicies.networking.k8s.io "platform-default-deny" is forbidden`),
						OutputErr:  errors.New("exit status 1"),
					}
				default:
					return &core.MockCommand{}
				}
			},
		}
		check := checkPlatformAPILiveInventoryNetworkPolicy(core.NewTestKubectlClient(mock))
		if check.OK {
			t.Fatal("expected unreadable NetworkPolicy to fail")
		}
		for _, want := range []string{"mcp-team-acme/platform-default-deny", "Forbidden"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should contain %q, got %q", want, check.Detail)
			}
		}
	})
}

func TestCheckOperatorRecentReconcileErrors(t *testing.T) {
	cases := []struct {
		name   string
		logs   string
		outErr error
		wantOK bool
	}{
		{name: "clean logs", logs: "started reconciler\nresource synced\n", wantOK: true},
		{name: "reconciler error pattern", logs: "ERROR Reconciler error: something broke\n", wantOK: false},
		{name: "failed to reconcile pattern", logs: "msg=\"failed to reconcile\" server=foo\n", wantOK: false},
		{name: "error syncing pattern", logs: "level=error error syncing mcpserver/foo\n", wantOK: false},
		{name: "case-insensitive match", logs: "FAILED TO RECONCILE\n", wantOK: false},
		{name: "ignores doctor smoke transient errors", logs: "ERROR Reconciler error mcpserver=doctor-smoke-123\n", wantOK: true},
		{name: "no logs, OK", logs: "", wantOK: true},
		{name: "kubectl error surfaces", outErr: errors.New("no such deploy"), wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{
				CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
					return &core.MockCommand{OutputData: []byte(tc.logs), OutputErr: tc.outErr}
				},
			}
			kubectl := core.NewTestKubectlClient(mock)
			check := checkOperatorRecentReconcileErrors(kubectl)
			if check.OK != tc.wantOK {
				t.Fatalf("OK=%v want %v; detail=%q", check.OK, tc.wantOK, check.Detail)
			}
		})
	}
}

func TestCheckOperatorClusterRoleRules(t *testing.T) {
	t.Run("accepts split verbs with matching API groups", func(t *testing.T) {
		check := checkOperatorClusterRoleRulesFromJSON(t, `{
			"rules": [
				{"apiGroups":[""],"resources":["serviceaccounts","configmaps","services"],"verbs":["get"]},
				{"apiGroups":[""],"resources":["serviceaccounts","configmaps","services"],"verbs":["list"]},
				{"apiGroups":[""],"resources":["serviceaccounts","configmaps","services"],"verbs":["watch"]},
				{"apiGroups":["apps"],"resources":["deployments"],"verbs":["get","list"]},
				{"apiGroups":["apps"],"resources":["deployments"],"verbs":["watch"]},
				{"apiGroups":["networking.k8s.io"],"resources":["ingresses"],"verbs":["get"]},
				{"apiGroups":["networking.k8s.io"],"resources":["ingresses"],"verbs":["list","watch"]}
			]
		}`)
		if !check.OK {
			t.Fatalf("expected OK for additive RBAC rules, got detail=%q", check.Detail)
		}
	})

	t.Run("requires the expected API group", func(t *testing.T) {
		check := checkOperatorClusterRoleRulesFromJSON(t, `{
			"rules": [
				{"apiGroups":[""],"resources":["serviceaccounts","configmaps","services"],"verbs":["get","list","watch"]},
				{"apiGroups":["extensions"],"resources":["deployments","ingresses"],"verbs":["get","list","watch"]}
			]
		}`)
		if check.OK {
			t.Fatal("expected failure for permissions granted on the wrong API groups")
		}
		for _, want := range []string{"deployments.apps", "ingresses.networking.k8s.io"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail should mention %q, got %q", want, check.Detail)
			}
		}
	})

	t.Run("accepts wildcard grants", func(t *testing.T) {
		check := checkOperatorClusterRoleRulesFromJSON(t, `{
			"rules": [
				{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}
			]
		}`)
		if !check.OK {
			t.Fatalf("expected OK for wildcard RBAC rule, got detail=%q", check.Detail)
		}
	})
}

func checkOperatorClusterRoleRulesFromJSON(t *testing.T, body string) DoctorCheck {
	t.Helper()
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if !contains(spec.Args, "mcp-runtime-operator-role") {
				t.Fatalf("unexpected command args: %v", spec.Args)
			}
			return &core.MockCommand{OutputData: []byte(body)}
		},
	}
	return checkOperatorClusterRoleRules(core.NewTestKubectlClient(mock))
}

func TestCheckMCPServerReconcileSmoke(t *testing.T) {
	t.Run("waits for deployment rollout readiness", func(t *testing.T) {
		sawRollout := false
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "get") && contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("go-example\n")}
				case argContains(spec.Args, "readyReplicas") && argContains(spec.Args, "containerPort"):
					return &core.MockCommand{OutputData: []byte("go-example|1|registry.local/go-example:dev|8088\n")}
				case contains(spec.Args, "apply"):
					return &core.MockCommand{}
				case contains(spec.Args, "rollout"):
					sawRollout = true
					if !contains(spec.Args, "--timeout=2m30s") {
						t.Fatalf("rollout status args %v missing timeout", spec.Args)
					}
					return &core.MockCommand{}
				case contains(spec.Args, "get"):
					return &core.MockCommand{OutputData: []byte("doctor-smoke")}
				case contains(spec.Args, "delete"):
					return &core.MockCommand{}
				}
				return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)

		check := checkMCPServerReconcileSmoke(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
		if !sawRollout {
			t.Fatal("expected smoke check to wait for deployment rollout readiness")
		}
		if !strings.Contains(check.Detail, "ready deployment/service/ingress") {
			t.Fatalf("detail should mention ready deployment resources, got %q", check.Detail)
		}
	})

	t.Run("fails when deployment rollout does not become ready", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "get") && contains(spec.Args, "mcpservers"):
					return &core.MockCommand{OutputData: []byte("go-example\n")}
				case argContains(spec.Args, "readyReplicas") && argContains(spec.Args, "containerPort"):
					return &core.MockCommand{OutputData: []byte("go-example|1|registry.local/go-example:dev|8088\n")}
				case contains(spec.Args, "apply"):
					return &core.MockCommand{}
				case contains(spec.Args, "rollout"):
					return &core.MockCommand{
						OutputData: []byte("deployment \"doctor-smoke\" exceeded its progress deadline"),
						OutputErr:  errors.New("rollout timed out"),
					}
				case contains(spec.Args, "get"):
					return &core.MockCommand{OutputData: []byte("doctor-smoke")}
				case contains(spec.Args, "delete"):
					return &core.MockCommand{}
				}
				return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)

		check := checkMCPServerReconcileSmoke(kubectl, "mcp-servers")
		if check.OK {
			t.Fatalf("expected failure when rollout fails; detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "deployment did not become ready") {
			t.Fatalf("detail should describe rollout readiness failure, got %q", check.Detail)
		}
		if !strings.Contains(check.Detail, "exceeded its progress deadline") {
			t.Fatalf("detail should include rollout output, got %q", check.Detail)
		}
	})

	t.Run("skips impossible rollout wait for fallback pause image", func(t *testing.T) {
		sawRollout := false
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "get") && contains(spec.Args, "mcpservers"):
					return &core.MockCommand{}
				case contains(spec.Args, "configmap"):
					return &core.MockCommand{}
				case argContains(spec.Args, "readyReplicas") && argContains(spec.Args, "containerPort"):
					return &core.MockCommand{OutputData: []byte("oauth-issuer|1|docker.io/library/python:3.12-alpine|8080\n")}
				case contains(spec.Args, "apply"):
					return &core.MockCommand{}
				case contains(spec.Args, "rollout"):
					sawRollout = true
					return &core.MockCommand{OutputErr: errors.New("rollout should not be called")}
				case contains(spec.Args, "get") && contains(spec.Args, "pods"):
					return &core.MockCommand{OutputData: []byte("node-a")}
				case contains(spec.Args, "get"):
					return &core.MockCommand{OutputData: []byte("doctor-smoke")}
				case contains(spec.Args, "delete"):
					return &core.MockCommand{}
				}
				return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)

		check := checkMCPServerReconcileSmoke(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q remedy=%q", check.Detail, check.Remedy)
		}
		if sawRollout {
			t.Fatal("fallback pause smoke should not wait for rollout readiness")
		}
		if !strings.Contains(check.Detail, "skipped readiness") {
			t.Fatalf("detail should mention skipped readiness, got %q", check.Detail)
		}
	})
}

func argContains(args []string, value string) bool {
	for _, arg := range args {
		if strings.Contains(arg, value) {
			return true
		}
	}
	return false
}

func TestCheckNodeCapacity(t *testing.T) {
	t.Run("metrics-server healthy", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "top") {
					return &core.MockCommand{OutputData: []byte("node-a  200m  10%  1Gi  20%\nnode-b  400m  20%  2Gi  40%\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNodeCapacity(kubectl)
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "2 node") {
			t.Fatalf("detail should mention node count, got %q", check.Detail)
		}
	})

	t.Run("flags hot node at CPU>=95%%", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "top") {
					return &core.MockCommand{OutputData: []byte("node-a  3800m  96%  7Gi  80%\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNodeCapacity(kubectl)
		if check.OK {
			t.Fatal("expected failure for 96% CPU")
		}
		if !strings.Contains(check.Detail, "node-a") {
			t.Fatalf("detail should name the hot node, got %q", check.Detail)
		}
	})

	t.Run("flags hot node at memory>=95%%", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "top") {
					return &core.MockCommand{OutputData: []byte("node-a  100m  10%  8Gi  97%\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNodeCapacity(kubectl)
		if check.OK {
			t.Fatal("expected failure for 97% memory")
		}
	})

	t.Run("falls back to allocatable when metrics-server missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "top"):
					return &core.MockCommand{OutputData: []byte("error: Metrics API not available"), OutputErr: errors.New("exit status 1")}
				case contains(spec.Args, "nodes"):
					return &core.MockCommand{OutputData: []byte("node-a  4  16Gi\nnode-b  4  16Gi\n")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNodeCapacity(kubectl)
		if !check.OK {
			t.Fatalf("expected OK fallback, got detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "metrics-server unavailable") {
			t.Fatalf("detail should note metrics-server fallback, got %q", check.Detail)
		}
	})

	t.Run("fails when both metrics and allocatable are unavailable", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("cluster unreachable")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkNodeCapacity(kubectl)
		if check.OK {
			t.Fatal("expected failure when both paths fail")
		}
	})
}

func TestCheckMCPServersImagePullSecrets(t *testing.T) {
	t.Run("ok when no imagePullSecrets configured", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputData: []byte("")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK when no secrets configured, got detail=%q", check.Detail)
		}
	})

	t.Run("ok when all referenced secrets exist", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "serviceaccount"):
					return &core.MockCommand{OutputData: []byte("reg-creds\ngcr-creds\n")}
				case contains(spec.Args, "secret"):
					// both secret lookups succeed
					return &core.MockCommand{OutputData: []byte("reg-creds")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when a referenced secret is missing", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "serviceaccount"):
					return &core.MockCommand{OutputData: []byte("reg-creds\nmissing-creds\n")}
				case contains(spec.Args, "missing-creds"):
					return &core.MockCommand{OutputErr: errors.New("secrets \"missing-creds\" not found")}
				case contains(spec.Args, "secret"):
					return &core.MockCommand{OutputData: []byte("reg-creds")}
				}
				return &core.MockCommand{}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if check.OK {
			t.Fatalf("expected failure when a pull secret is missing; detail=%q", check.Detail)
		}
		if !strings.Contains(check.Detail, "missing-creds") {
			t.Fatalf("detail should name the missing secret, got %q", check.Detail)
		}
	})

	t.Run("ok when workload serviceaccount is not provisioned yet", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New(`Error from server (NotFound): serviceaccounts "mcp-workload" not found`)}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK when serviceaccount is missing before deploy, got detail=%q", check.Detail)
		}
	})

	t.Run("ok when kubectl notfound is only on combined output", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{
					OutputData: []byte(`Error from server (NotFound): serviceaccounts "mcp-workload" not found`),
					OutputErr:  errors.New("exit status 1"),
				}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if !check.OK {
			t.Fatalf("expected OK when notfound appears only on combined output, got detail=%q", check.Detail)
		}
	})

	t.Run("fails when serviceaccount lookup errors", func(t *testing.T) {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				return &core.MockCommand{OutputErr: errors.New("serviceaccount lookup forbidden")}
			},
		}
		kubectl := core.NewTestKubectlClient(mock)
		check := checkMCPServersImagePullSecrets(kubectl, "mcp-servers")
		if check.OK {
			t.Fatal("expected failure when serviceaccount lookup fails")
		}
	})
}

func TestCheckMCPServersImagePullSmokeUsesRestrictedCompliantPodSpec(t *testing.T) {
	var smokeRunArgs []string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "get") && contains(spec.Args, "mcpservers"):
				return &core.MockCommand{}
			case contains(spec.Args, "get") && contains(spec.Args, "deploy"):
				return &core.MockCommand{OutputData: []byte("")}
			case len(spec.Args) > 0 && spec.Args[0] == "run":
				smokeRunArgs = append([]string(nil), spec.Args...)
				return &core.MockCommand{}
			case contains(spec.Args, "get") && argContains(spec.Args, "imageID"):
				return &core.MockCommand{OutputData: []byte("docker-pullable://registry.k8s.io/pause@sha256:test")}
			case contains(spec.Args, "delete"):
				return &core.MockCommand{}
			default:
				return &core.MockCommand{}
			}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	check := checkMCPServersImagePullSmoke(kubectl, "mcp-servers")
	if !check.OK {
		t.Fatalf("expected image pull smoke to pass, got detail=%q", check.Detail)
	}
	if len(smokeRunArgs) == 0 {
		t.Fatal("expected image pull smoke to create a pod")
	}
	overrides := argValueWithPrefix(smokeRunArgs, "--overrides=")
	if overrides == "" {
		t.Fatalf("image pull smoke should use restricted-compliant overrides, got args=%v", smokeRunArgs)
	}
	for _, want := range []string{
		`"automountServiceAccountToken":false`,
		`"allowPrivilegeEscalation":false`,
		`"runAsNonRoot":true`,
		`"runAsUser":65532`,
		`"seccompProfile":{"type":"RuntimeDefault"}`,
		`"capabilities":{"drop":["ALL"]}`,
	} {
		if !strings.Contains(overrides, want) {
			t.Fatalf("image pull smoke overrides missing %s: %s", want, overrides)
		}
	}
	for _, notWant := range []string{`"command":`, `"args":`} {
		if strings.Contains(overrides, notWant) {
			t.Fatalf("image pull smoke overrides should not set %s: %s", notWant, overrides)
		}
	}
}

func TestCheckMCPServersDNSAndNetworkReadsCompletedPodLogs(t *testing.T) {
	var runArgs []string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case len(spec.Args) > 0 && spec.Args[0] == "run":
				runArgs = append([]string(nil), spec.Args...)
				return &core.MockCommand{OutputData: []byte("pod/mcp-runtime-doctor-dns created\n")}
			case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "jsonpath={.status.phase}"):
				return &core.MockCommand{OutputData: []byte("Succeeded")}
			case len(spec.Args) > 0 && spec.Args[0] == "logs":
				return &core.MockCommand{OutputData: []byte("HTTP/1.1 200 OK\r\n")}
			case len(spec.Args) > 0 && spec.Args[0] == "delete":
				return &core.MockCommand{}
			}
			return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)
	check := checkMCPServersDNSAndNetwork(kubectl)
	if !check.OK {
		t.Fatalf("expected DNS/network probe to pass, got detail=%q", check.Detail)
	}
	if len(runArgs) == 0 {
		t.Fatal("expected DNS/network probe to create a curl pod")
	}
	overrides := argValueWithPrefix(runArgs, "--overrides=")
	if overrides == "" {
		t.Fatalf("DNS/network probe should use restricted-compliant overrides, got args=%v", runArgs)
	}
	for _, notWant := range []string{"--attach", "--rm"} {
		if contains(runArgs, notWant) {
			t.Fatalf("DNS/network probe should read completed pod logs instead of using %s, got args=%v", notWant, runArgs)
		}
	}
}

func TestRegistryReachabilityUsesHTTPSForInternalTLSRegistry(t *testing.T) {
	var runArgs []string
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "registry-internal-tls"):
				return &core.MockCommand{OutputData: []byte("registry-internal-tls")}
			case len(spec.Args) > 0 && spec.Args[0] == "run":
				runArgs = append([]string(nil), spec.Args...)
				return &core.MockCommand{}
			case len(spec.Args) > 0 && spec.Args[0] == "get" && contains(spec.Args, "pod"):
				return &core.MockCommand{OutputData: []byte("Succeeded")}
			case len(spec.Args) > 0 && spec.Args[0] == "logs":
				return &core.MockCommand{OutputData: []byte("HTTP/2 200\r\n")}
			}
			return &core.MockCommand{OutputErr: fmt.Errorf("unexpected command: %v", spec.Args)}
		},
	}
	kubectl := core.NewTestKubectlClient(mock)

	check := checkRegistryReachableFromCluster(kubectl)
	if !check.OK {
		t.Fatalf("expected registry probe to pass, got detail=%q", check.Detail)
	}
	if !argContains(runArgs, "https://registry.registry.svc.cluster.local:5000/v2/") {
		t.Fatalf("registry probe should use HTTPS when registry-internal-tls exists, got args=%v", runArgs)
	}
	if !argContains(runArgs, "-skI") {
		t.Fatalf("registry probe should allow the internal CA probe with -k, got args=%v", runArgs)
	}
}

func TestRestrictedRunOverridesUsesNumericNonRootUser(t *testing.T) {
	overrides := restrictedRunOverrides("probe", "curlimages/curl:8.7.1", "curl", "-sSI", "http://example.test")
	for _, want := range []string{
		`"runAsNonRoot":true`,
		`"runAsUser":65532`,
		`"workingDir":"/tmp"`,
		`"command":["curl"]`,
		`"args":["-sSI","http://example.test"]`,
	} {
		if !strings.Contains(overrides, want) {
			t.Fatalf("restricted overrides missing %s: %s", want, overrides)
		}
	}
}

func TestCheckStorageClassReadinessDiscoversDefault(t *testing.T) {
	t.Setenv("MCP_STORAGE_CLASS", "")
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "-o") && contains(spec.Args, "json") {
				return &core.MockCommand{OutputData: []byte(`{"items":[{"metadata":{"name":"standard","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}]}`)}
			}
			return &core.MockCommand{OutputData: []byte("standard")}
		},
	}
	check := checkStorageClassReadiness(core.NewTestKubectlClient(mock))
	if !check.OK || !strings.Contains(check.Detail, `StorageClass "standard"`) {
		t.Fatalf("expected discovered default StorageClass, got %+v", check)
	}
}

func TestCheckSentinelOIDCConfigurationSkipsTestMode(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputData: []byte(`{"data":{"PLATFORM_MODE":"tenant","MCP_RUNTIME_TEST_MODE":"1"}}`)}
		},
	}
	check := checkSentinelOIDCConfiguration(core.NewTestKubectlClient(mock))
	if !check.OK || !strings.Contains(check.Detail, "test mode") {
		t.Fatalf("expected test-mode OIDC check to pass, got %+v", check)
	}
}

func TestCheckSentinelTelemetryPipelineSkipsMissingNamespace(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputErr: errors.New("not found")}
		},
	}
	check := checkSentinelTelemetryPipeline(core.NewTestKubectlClient(mock))
	if !check.OK || !strings.Contains(check.Detail, "skipping") {
		t.Fatalf("expected missing optional telemetry stack to be skipped, got %+v", check)
	}
}

func TestCheckPersistentVolumeClaimsReportsPendingClaims(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputData: []byte(`{"items":[{"metadata":{"namespace":"mcp-sentinel","name":"kafka-data-0"},"status":{"phase":"Pending"}}]}`)}
		},
	}
	check := checkPersistentVolumeClaims(core.NewTestKubectlClient(mock))
	if check.OK || !strings.Contains(check.Detail, "kafka-data-0") {
		t.Fatalf("expected pending PVC to fail, got %+v", check)
	}
}

func TestCheckClusterNodesReadyReportsNotReadyNodes(t *testing.T) {
	mock := &core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputData: []byte(`{"items":[{"metadata":{"name":"worker-1"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`)}
		},
	}
	check := checkClusterNodesReady(core.NewTestKubectlClient(mock))
	if check.OK || !strings.Contains(check.Detail, "worker-1") {
		t.Fatalf("expected not-ready node to fail, got %+v", check)
	}
}

func argValueWithPrefix(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

func TestCheckSentinelStalePods(t *testing.T) {
	run := func(podsJSON string) DoctorCheck {
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "namespace"):
					return &core.MockCommand{OutputData: []byte("mcp-sentinel")}
				case contains(spec.Args, "pods"):
					return &core.MockCommand{OutputData: []byte(podsJSON)}
				default:
					return &core.MockCommand{}
				}
			},
		}
		return checkSentinelStalePods(core.NewTestKubectlClient(mock))
	}

	t.Run("fails on evicted and orphaned completed pods", func(t *testing.T) {
		check := run(`{"items":[
{"metadata":{"name":"kafka-0"},"status":{"phase":"Running"}},
{"metadata":{"name":"ingest-old"},"status":{"phase":"Failed","reason":"Evicted"}},
{"metadata":{"name":"orphan"},"status":{"phase":"Succeeded"}},
{"metadata":{"name":"kafka-topic-init-x","ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Succeeded"}}
]}`)
		if check.OK {
			t.Fatal("expected stale pod failure")
		}
		for _, want := range []string{"2 stale", "pod/ingest-old Evicted", "pod/orphan Completed"} {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf("detail = %q, want %q", check.Detail, want)
			}
		}
		if strings.Contains(check.Detail, "kafka-topic-init-x") {
			t.Fatalf("job-owned completed pod must not be reported: %q", check.Detail)
		}
	})

	t.Run("passes with only running and job-owned pods", func(t *testing.T) {
		check := run(`{"items":[
{"metadata":{"name":"kafka-0"},"status":{"phase":"Running"}},
{"metadata":{"name":"job-pod","ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Succeeded"}}
]}`)
		if !check.OK {
			t.Fatalf("expected OK, got %q", check.Detail)
		}
	})
}
