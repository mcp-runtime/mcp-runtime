package doctor

import (
	"errors"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func traefikEgressDoctorKubectl(output string, err error) core.KubectlRunner {
	return core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputData: []byte(output), OutputErr: err}
		},
	})
}

func TestCheckMCPServerTraefikEgress(t *testing.T) {
	t.Run("fails when a server reports TraefikEgressReady=False", func(t *testing.T) {
		servers := `{"items":[
			{"metadata":{"namespace":"mcp-servers","name":"ok"},"status":{"conditions":[{"type":"TraefikEgressReady","status":"True","reason":"PolicyApplied"}]}},
			{"metadata":{"namespace":"mcp-servers","name":"bad"},"status":{"conditions":[{"type":"TraefikEgressReady","status":"False","reason":"TraefikPodsNotFound","message":"no Traefik pods match app=traefik in namespace traefik"}]}}
		]}`
		check := checkMCPServerTraefikEgress(traefikEgressDoctorKubectl(servers, nil))
		if check.OK {
			t.Fatal("expected failure for TraefikEgressReady=False")
		}
		if !strings.Contains(check.Detail, "mcp-servers/bad TraefikPodsNotFound") || strings.Contains(check.Detail, "mcp-servers/ok") {
			t.Fatalf("detail = %q", check.Detail)
		}
		if !strings.Contains(check.Remedy, "PLATFORM_TRAEFIK_NAMESPACE") {
			t.Fatalf("remedy = %q, want the Traefik namespace setting", check.Remedy)
		}
	})

	t.Run("passes when no server reports a failure", func(t *testing.T) {
		servers := `{"items":[{"metadata":{"namespace":"mcp-servers","name":"ok"},"status":{"conditions":[{"type":"TraefikEgressReady","status":"True","reason":"EgressUnrestricted"}]}}]}`
		if check := checkMCPServerTraefikEgress(traefikEgressDoctorKubectl(servers, nil)); !check.OK {
			t.Fatalf("expected ok, got detail=%q", check.Detail)
		}
	})

	t.Run("passes before the CRD is installed", func(t *testing.T) {
		if check := checkMCPServerTraefikEgress(traefikEgressDoctorKubectl("", errors.New("no matches for kind"))); !check.OK {
			t.Fatalf("expected ok without the CRD, got detail=%q", check.Detail)
		}
	})
}
