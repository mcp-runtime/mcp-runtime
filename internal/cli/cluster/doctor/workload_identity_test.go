package doctor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
)

func TestCheckWorkloadIdentity(t *testing.T) {
	now := time.Now().UTC()
	caPEM := testIdentityCAPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour), "workload root")
	otherCAPEM := testIdentityCAPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour), "other root")

	makeCheck := func(t *testing.T, fixture workloadIdentityFixture) (DoctorCheck, []string) {
		t.Helper()
		var calls []string
		mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			args := strings.Join(spec.Args, " ")
			calls = append(calls, args)
			switch {
			case strings.Contains(args, "get deployment mcp-runtime-operator-controller-manager"):
				switch {
				case strings.Contains(args, "MCP_ADAPTER_CERTIFICATES"):
					return &core.MockCommand{OutputData: []byte(fixture.enabled)}
				case strings.Contains(args, "MCP_MTLS_CLUSTER_ISSUER"):
					return &core.MockCommand{OutputData: []byte(fixture.operatorIssuer)}
				case strings.Contains(args, "MCP_TRUST_DOMAIN"):
					return &core.MockCommand{OutputData: []byte(fixture.operatorTrustDomain)}
				case strings.Contains(args, "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE"):
					return &core.MockCommand{OutputData: []byte(fixture.tlsNamespace)}
				}
			case strings.Contains(args, "get configmap mcp-shared-config"):
				if strings.Contains(args, "MCP_MTLS_CLUSTER_ISSUER") {
					return &core.MockCommand{OutputData: []byte(fixture.apiIssuer)}
				}
				return &core.MockCommand{OutputData: []byte(fixture.apiTrustDomain)}
			case strings.Contains(args, "get clusterissuer"):
				return &core.MockCommand{OutputData: []byte(fixture.issuerReady)}
			case strings.Contains(args, "get secret workload-server-gateway-mtls"):
				return &core.MockCommand{OutputData: []byte(base64.StdEncoding.EncodeToString([]byte(fixture.caPEM)))}
			case strings.Contains(args, "get ingressroute.traefik.io workload-server"):
				return &core.MockCommand{OutputData: []byte("workload-server")}
			case strings.Contains(args, "get tlsoption.traefik.io default"):
				if strings.Contains(args, "clientAuthType") {
					return &core.MockCommand{OutputData: []byte("VerifyClientCertIfGiven")}
				}
				return &core.MockCommand{OutputData: []byte("mcp-adapter-client-ca")}
			case strings.Contains(args, "get secret mcp-adapter-client-ca"):
				return &core.MockCommand{OutputData: []byte(base64.StdEncoding.EncodeToString([]byte(fixture.bundlePEM)))}
			default:
				return &core.MockCommand{}
			}
			return &core.MockCommand{}
		}}
		return CheckWorkloadIdentity(core.NewTestKubectlClient(mock), "mcp-servers", "workload-server"), calls
	}

	base := workloadIdentityFixture{
		enabled: "true", operatorIssuer: "workload-ca", apiIssuer: "workload-ca",
		operatorTrustDomain: "example.org", apiTrustDomain: "example.org", tlsNamespace: "traefik",
		issuerReady: "True", caPEM: caPEM, bundlePEM: caPEM,
	}

	t.Run("reports ready configuration and public metadata without reading private keys", func(t *testing.T) {
		check, calls := makeCheck(t, base)
		if !check.OK {
			t.Fatalf("expected healthy identity, got: %+v", check)
		}
		for _, want := range []string{"issuer \"workload-ca\" Ready", "subject=", "SHA-256=", "expires=", "trusted bundle SHA-256:"} {
			if !strings.Contains(check.Detail, want) {
				t.Errorf("detail %q does not contain %q", check.Detail, want)
			}
		}
		for _, call := range calls {
			if strings.Contains(call, "tls.key") || strings.Contains(call, "secret -o json") {
				t.Fatalf("diagnostic requested secret material: %s", call)
			}
		}
	})

	t.Run("disabled by choice is healthy and skips issuer and route reads", func(t *testing.T) {
		fixture := base
		fixture.enabled = "false"
		fixture.operatorIssuer = ""
		fixture.apiIssuer = ""
		check, calls := makeCheck(t, fixture)
		if !check.OK || !strings.Contains(check.Detail, "off by operator choice") {
			t.Fatalf("expected off-by-choice result, got: %+v", check)
		}
		for _, call := range calls {
			if strings.Contains(call, "get clusterissuer") || strings.Contains(call, "get secret") {
				t.Fatalf("disabled diagnostic should skip issuer and certificate reads: %s", call)
			}
		}
	})

	t.Run("reports configuration drift even when adapter certificates are disabled", func(t *testing.T) {
		fixture := base
		fixture.enabled = "false"
		fixture.apiIssuer = "different-ca"
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "configuration drift") {
			t.Fatalf("expected config drift failure, got: %+v", check)
		}
	})

	t.Run("detects operator and runtime API drift", func(t *testing.T) {
		fixture := base
		fixture.apiTrustDomain = "other.example"
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "configuration drift") {
			t.Fatalf("expected config drift failure, got: %+v", check)
		}
	})

	t.Run("detects issuer not ready", func(t *testing.T) {
		fixture := base
		fixture.issuerReady = "False"
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "not Ready") {
			t.Fatalf("expected issuer readiness failure, got: %+v", check)
		}
	})

	t.Run("detects CA and trusted bundle mismatch", func(t *testing.T) {
		fixture := base
		fixture.bundlePEM = otherCAPEM
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "fingerprints differ") {
			t.Fatalf("expected bundle mismatch, got: %+v", check)
		}
	})

	t.Run("warns before CA expiry", func(t *testing.T) {
		fixture := base
		fixture.caPEM = testIdentityCAPEM(t, now.Add(-time.Hour), now.Add(10*24*time.Hour), "soon to expire")
		fixture.bundlePEM = fixture.caPEM
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "expires within 30 days") {
			t.Fatalf("expected expiry warning, got: %+v", check)
		}
	})
}

type workloadIdentityFixture struct {
	enabled, operatorIssuer, apiIssuer, operatorTrustDomain, apiTrustDomain string
	tlsNamespace, issuerReady, caPEM, bundlePEM                             string
}

func testIdentityCAPEM(t *testing.T, notBefore, notAfter time.Time, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestParseAdapterCertificatesEnabled(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
		bad   bool
	}{{"", false, false}, {"false", false, false}, {"0", false, false}, {"true", true, false}, {"1", true, false}, {"sometimes", false, true}} {
		got, err := parseAdapterCertificatesEnabled(tc.value)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("parseAdapterCertificatesEnabled(%q) = (%v, %v), want (%v, bad=%v)", tc.value, got, err, tc.want, tc.bad)
		}
	}
}
