package doctor

import (
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestIngressRouteProbeUsesHTTPSAndPublicSNI(t *testing.T) {
	for _, tc := range []struct {
		name, route, ports string
		wantOK             bool
	}{
		{name: "TLS annotation with default certificate", route: "buddy|mcp.example.com|/buddy/mcp|true|", ports: "web:80:0\nwebsecure:443:0", wantOK: true},
		{name: "TLS secret", route: "buddy|mcp.example.com|/buddy/mcp||mcp-tls", ports: "web:8000:0\nwebsecure:8443:0", wantOK: true},
		{name: "missing secure port", route: "buddy|mcp.example.com|/buddy/mcp|true|", ports: "web:80:0"},
		{name: "missing public host", route: "buddy||/buddy/mcp|true|", ports: "web:80:0\nwebsecure:443:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runArgs []string
			deleted := false
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "get") && contains(spec.Args, "ingress"):
					return &core.MockCommand{OutputData: []byte(tc.route)}
				case contains(spec.Args, "get") && contains(spec.Args, "svc"):
					return &core.MockCommand{OutputData: []byte(tc.ports)}
				case contains(spec.Args, "run"):
					runArgs = spec.Args
					return &core.MockCommand{OutputData: []byte("pod created")}
				case contains(spec.Args, "get") && contains(spec.Args, "pod"):
					return &core.MockCommand{OutputData: []byte("Succeeded")}
				case contains(spec.Args, "logs"):
					return &core.MockCommand{OutputData: []byte("401")}
				case contains(spec.Args, "delete"):
					deleted = true
				}
				return &core.MockCommand{}
			}}
			check := checkIngressRouteProbe(core.NewTestKubectlClient(mock), "mcp-servers", DistroK3s)
			if check.OK != tc.wantOK {
				t.Fatalf("OK=%v want %v; %s", check.OK, tc.wantOK, check.Detail)
			}
			if !tc.wantOK {
				if len(runArgs) > 0 {
					t.Fatal("invalid TLS route should not create a helper pod")
				}
				return
			}
			overrides := argValueWithPrefix(runArgs, "--overrides=")
			securePort := "443"
			if strings.Contains(tc.ports, "8443") {
				securePort = "8443"
			}
			for _, expected := range []string{"https://mcp.example.com/buddy/mcp", "--connect-to", "mcp.example.com:443:traefik.kube-system.svc.cluster.local:" + securePort} {
				if !strings.Contains(overrides, expected) {
					t.Fatalf("missing %q in TLS probe: %s", expected, overrides)
				}
			}
			if strings.Contains(overrides, "--insecure") || !deleted || !strings.Contains(check.Detail, "HTTP 401") {
				t.Fatalf("TLS verification, helper cleanup, or auth status missing: %s; %s", overrides, check.Detail)
			}
		})
	}
}
