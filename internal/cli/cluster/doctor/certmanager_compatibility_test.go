package doctor

import (
	"fmt"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"net/url"
	"strings"
	"testing"
)

func TestCertManagerSupportMatrix(t *testing.T) {
	for _, tc := range []struct {
		image, kube string
		ok          bool
	}{
		{"quay.io/jetstack/cert-manager-controller:v1.16.2", "v1.36.4+k3s1", false},
		{"quay.io/jetstack/cert-manager-controller:v1.21.2", "v1.36.4+k3s1", true},
		{"quay.io/jetstack/cert-manager-controller:v1.20.3", "v1.36.4", false},
		{"quay.io/jetstack/cert-manager-controller:v1.20.3", "v1.32.0", true},
		{"quay.io/jetstack/cert-manager-controller:v1.21.2", "v1.32.0", false},
		{"quay.io/jetstack/cert-manager-controller:v1.21.2", "v1.37.0", false},
		{"quay.io/jetstack/cert-manager-controller:v1.22.0", "v1.36.4", false},
		{"quay.io/jetstack/cert-manager-controller:v1.21.0-alpha.1", "v1.36.4", false},
		{"registry:5000/controller@sha256:deadbeef", "v1.36.4", false},
		{"controller:latest", "v1.36.4", false},
		{"controller:v1.21.2", "unknown", false},
	} {
		t.Run(tc.image+tc.kube, func(t *testing.T) {
			_, err := certManagerVersionCompatible(tc.image, tc.kube)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want compatible=%v", err, tc.ok)
			}
		})
	}
}

func TestCertManagerCompatibilityChecksAllComponents(t *testing.T) {
	t.Setenv("MCP_ACME_EMAIL", "test@example.com")
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "version") {
					return &core.MockCommand{OutputData: []byte(`{"serverVersion":{"gitVersion":"v1.36.4+k3s1"}}`)}
				}
				tag := "v1.21.2"
				if mismatch && contains(spec.Args, "cert-manager-cainjector") {
					tag = "v1.21.1"
				}
				return &core.MockCommand{OutputData: []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"image":"quay.io/jetstack/controller:%s"}]}}}}`, tag))}
			}}
			check := checkCertManagerCompatibility(core.NewTestKubectlClient(mock))
			if check.OK == mismatch {
				t.Fatalf("check=%+v", check)
			}
		})
	}
}

func TestCertManagerInstallPinSupportsKubernetes136(t *testing.T) {
	u, err := url.Parse(certmanager.CertManagerInstallManifestURL())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(u.Path, "/")
	if u.Host != "github.com" || len(parts) < 2 {
		t.Fatalf("unexpected installer URL: %s", u)
	}
	if _, err := certManagerVersionCompatible("controller:"+parts[len(parts)-2], "v1.36.4+k3s1"); err != nil {
		t.Fatal(err)
	}
}
