package doctor

import (
	"fmt"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func gatewayDeploymentJSON(sa, audience string) string {
	return fmt.Sprintf(`{"items":[{
  "metadata":{"namespace":"mcp-servers","name":"cully"},
  "spec":{"template":{"spec":{
    "serviceAccountName":%q,
    "containers":[{"name":"mcp-gateway",
      "env":[
        {"name":"ANALYTICS_INGEST_URL","value":"http://ingest/events"},
        {"name":"ANALYTICS_TOKEN_FILE","value":"/var/run/mcp-analytics/token"}],
      "volumeMounts":[{"name":"analytics-token","mountPath":"/var/run/mcp-analytics"}]}],
    "volumes":[{"name":"analytics-token","projected":{"sources":[{"serviceAccountToken":{"audience":%q,"path":"token"}}]}}]
  }}}
}]}`, sa, audience)
}

func kubectlReturning(out string) core.KubectlRunner {
	return core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(core.ExecSpec) *core.MockCommand {
			return &core.MockCommand{OutputData: []byte(out)}
		},
	})
}

func TestCheckGatewayAnalyticsCredentialsProjectedToken(t *testing.T) {
	tests := []struct {
		name       string
		deployment string
		wantOK     bool
		wantDetail string
	}{
		{"valid projected token", gatewayDeploymentJSON("mcp-workload", "mcp-ingest"), true, "can authenticate"},
		{"wrong audience", gatewayDeploymentJSON("mcp-workload", "api"), false, `audience is "api"`},
		{"wrong service account", gatewayDeploymentJSON("default", "mcp-ingest"), false, `ServiceAccount "default"`},
		{"token file without mount", `{"items":[{"metadata":{"namespace":"a","name":"b"},"spec":{"template":{"spec":{"containers":[{"name":"mcp-gateway","env":[{"name":"ANALYTICS_INGEST_URL","value":"u"},{"name":"ANALYTICS_TOKEN_FILE","value":"/x/token"}]}]}}}}]}`, false, "not under a volume mount"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := checkGatewayAnalyticsCredentials(kubectlReturning(tc.deployment))
			if check.OK != tc.wantOK || !strings.Contains(check.Detail, tc.wantDetail) {
				t.Fatalf("ok=%v detail=%q, want ok=%v containing %q", check.OK, check.Detail, tc.wantOK, tc.wantDetail)
			}
		})
	}
}

func TestCheckGatewayAnalyticsDelivery(t *testing.T) {
	run := func(logs string) DoctorCheck {
		return checkGatewayAnalyticsDelivery(core.NewTestKubectlClient(&core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "logs") {
					return &core.MockCommand{OutputData: []byte(logs)}
				}
				return &core.MockCommand{OutputData: []byte(gatewayDeploymentJSON("mcp-workload", "mcp-ingest"))}
			},
		}))
	}
	t.Run("healthy logs pass", func(t *testing.T) {
		if check := run("gateway request ok\n"); !check.OK {
			t.Fatalf("expected ok, got %q", check.Detail)
		}
	})
	t.Run("401s fail with the offending server", func(t *testing.T) {
		check := run("2026/10/13 gateway analytics emission failed with status 401 to http://ingest/events\n")
		if check.OK || !strings.Contains(check.Detail, "mcp-servers/cully") || !strings.Contains(check.Detail, "unauthorized") {
			t.Fatalf("got ok=%v detail=%q", check.OK, check.Detail)
		}
	})
	t.Run("transport errors fail", func(t *testing.T) {
		check := run("failed to emit gateway analytics event to http://ingest/events: dial tcp: refused\n")
		if check.OK || !strings.Contains(check.Detail, "failed to deliver") {
			t.Fatalf("got ok=%v detail=%q", check.OK, check.Detail)
		}
	})
}

const ingestDeploymentJSON = `{"spec":{"template":{"spec":{
  "serviceAccountName":"%s","automountServiceAccountToken":%s,
  "containers":[{"name":"ingest","env":[{"name":"INGEST_SA_TOKEN_AUDIENCE","value":"%s"}]}]}}}}`

func TestCheckIngestServiceAccountAuth(t *testing.T) {
	run := func(sa, automount, audience, canI string, saExists bool) DoctorCheck {
		return checkIngestServiceAccountAuth(core.NewTestKubectlClient(&core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				switch {
				case contains(spec.Args, "auth"):
					return &core.MockCommand{OutputData: []byte(canI)}
				case contains(spec.Args, "deployment"):
					return &core.MockCommand{OutputData: []byte(fmt.Sprintf(ingestDeploymentJSON, sa, automount, audience))}
				case contains(spec.Args, "serviceaccount"):
					if !saExists {
						return &core.MockCommand{OutputData: []byte("not found"), OutputErr: fmt.Errorf("NotFound")}
					}
					return &core.MockCommand{OutputData: []byte("mcp-ingest")}
				default:
					return &core.MockCommand{OutputData: []byte("mcp-observability")}
				}
			},
		}))
	}
	t.Run("healthy", func(t *testing.T) {
		if check := run("mcp-ingest", "true", "mcp-ingest", "yes", true); !check.OK {
			t.Fatalf("expected ok, got %q", check.Detail)
		}
	})
	for _, tc := range []struct {
		name, sa, automount, audience, canI string
		saExists                            bool
		want                                string
	}{
		{"missing audience", "mcp-ingest", "true", "", "yes", true, "INGEST_SA_TOKEN_AUDIENCE"},
		{"wrong service account", "default", "true", "mcp-ingest", "yes", true, "expected"},
		{"automount disabled", "mcp-ingest", "false", "mcp-ingest", "yes", true, "automountServiceAccountToken=false"},
		{"service account missing", "mcp-ingest", "true", "mcp-ingest", "yes", false, "not found"},
		{"no tokenreview RBAC", "mcp-ingest", "true", "mcp-ingest", "no", true, "cannot create tokenreviews"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := run(tc.sa, tc.automount, tc.audience, tc.canI, tc.saExists)
			if check.OK || !strings.Contains(check.Detail, tc.want) {
				t.Fatalf("ok=%v detail=%q, want failure containing %q", check.OK, check.Detail, tc.want)
			}
		})
	}
}
