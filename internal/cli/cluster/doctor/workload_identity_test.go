package doctor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/core"
)

func TestCheckWorkloadIdentity(t *testing.T) {
	now := time.Now().UTC()
	caPEM := testIdentityCAPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour), "workload root")
	otherCAPEM := testIdentityCAPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour), "other root")

	makeCheck := func(t *testing.T, fixture workloadIdentityFixture) (DoctorCheck, []string) {
		t.Helper()
		var calls []string
		jsonOutput := func(v any) *core.MockCommand {
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			return &core.MockCommand{OutputData: data}
		}
		mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			args := strings.Join(spec.Args, " ")
			calls = append(calls, args)
			switch {
			case strings.Contains(args, "get deployment mcp-runtime-operator-controller-manager"):
				enabled := corev1.EnvVar{Name: "MCP_ADAPTER_CERTIFICATES", Value: fixture.enabled}
				if fixture.flagConfigMap {
					enabled.Value = ""
					enabled.ValueFrom = &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "identity-choice"}, Key: "enabled"}}
				}
				if fixture.flagSecret {
					enabled.Value = ""
					enabled.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "private-choice"}, Key: "enabled"}}
				}
				return jsonOutput(appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "mcp-runtime"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{enabled, {Name: "MCP_MTLS_CLUSTER_ISSUER", Value: fixture.operatorIssuer}, {Name: "MCP_TRUST_DOMAIN", Value: fixture.operatorTrustDomain}, {Name: "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", Value: fixture.tlsNamespace}}}}}}}})
			case strings.Contains(args, "get deployment mcp-runtime-api"):
				return jsonOutput(appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "mcp-platform"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "runtime-api", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "actual-api-config"}}}}}}}}}})
			case strings.Contains(args, "get configmap identity-choice"):
				return jsonOutput(corev1.ConfigMap{Data: map[string]string{"enabled": fixture.enabled}})
			case strings.Contains(args, "get configmap actual-api-config"):
				return jsonOutput(corev1.ConfigMap{Data: map[string]string{"MCP_MTLS_CLUSTER_ISSUER": fixture.apiIssuer, "MCP_TRUST_DOMAIN": fixture.apiTrustDomain}})
			case strings.Contains(args, "get clusterissuer"):
				return &core.MockCommand{OutputData: []byte(fixture.issuerReady)}
			case strings.Contains(args, "get secret workload-server-gateway-mtls"):
				return &core.MockCommand{OutputData: []byte(base64.StdEncoding.EncodeToString([]byte(fixture.caPEM)))}
			case strings.Contains(args, "get ingressroute.traefik.io workload-server"):
				route := map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "IngressRoute", "metadata": map[string]any{"name": "workload-server", "namespace": "mcp-servers"}, "spec": map[string]any{"tls": map[string]any{}}}
				if fixture.explicitOption != "" {
					route["spec"] = map[string]any{"tls": map[string]any{"options": map[string]any{"name": fixture.explicitOption, "namespace": "traefik"}}}
				}
				if fixture.plainRoute {
					route["spec"] = map[string]any{}
				}
				return jsonOutput(route)
			case strings.Contains(args, "get tlsoption.traefik.io"):
				mode := "VerifyClientCertIfGiven"
				if fixture.explicitOption == "no-client-auth" {
					mode = "NoClientCert"
				}
				return jsonOutput(map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "TLSOption", "spec": map[string]any{"clientAuth": map[string]any{"clientAuthType": mode, "secretNames": []string{"mcp-adapter-client-ca"}}}})
			case strings.Contains(args, "get secret mcp-adapter-client-ca"):
				return &core.MockCommand{OutputData: []byte(base64.StdEncoding.EncodeToString([]byte(fixture.bundlePEM)))}
			default:
				return &core.MockCommand{}
			}
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

	t.Run("ConfigMap enabled flag proceeds into issuer validation", func(t *testing.T) {
		fixture := base
		fixture.flagConfigMap, fixture.issuerReady = true, "False"
		check, calls := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "not Ready") {
			t.Fatalf("ConfigMap-backed true flag skipped issuer: %+v (calls=%v)", check, calls)
		}
	})

	t.Run("Secret backed flag is unreadable instead of disabled", func(t *testing.T) {
		fixture := base
		fixture.flagSecret = true
		check, calls := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "cannot read") {
			t.Fatalf("unreadable source reported off by choice: %+v", check)
		}
		for _, call := range calls {
			if strings.Contains(call, "get secret") {
				t.Fatalf("read a private source: %s", call)
			}
		}
	})

	t.Run("rejects actual route override disabling client auth", func(t *testing.T) {
		fixture := base
		fixture.explicitOption = "no-client-auth"
		check, calls := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "traefik/no-client-auth") {
			t.Fatalf("unsafe route reported healthy: %+v (calls=%v)", check, calls)
		}
	})

	t.Run("rejects plaintext route despite healthy default TLSOption", func(t *testing.T) {
		fixture := base
		fixture.plainRoute = true
		check, _ := makeCheck(t, fixture)
		if check.OK || !strings.Contains(check.Detail, "no TLS") {
			t.Fatalf("plaintext route reported healthy: %+v", check)
		}
	})

	t.Run("follows valid explicit option", func(t *testing.T) {
		fixture := base
		fixture.explicitOption = "actual-client-auth"
		check, _ := makeCheck(t, fixture)
		if !check.OK || !strings.Contains(check.Detail, "traefik/actual-client-auth") {
			t.Fatalf("actual route not verified: %+v", check)
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
	flagConfigMap, flagSecret, plainRoute                                   bool
	explicitOption                                                          string
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
