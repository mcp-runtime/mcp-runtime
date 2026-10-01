package sentinel_test

import (
	"strings"
	"testing"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/sentinel"
)

func TestParseGrafanaProbe(t *testing.T) {
	cases := []struct {
		out  string
		want sentinel.GrafanaState
	}{
		{"health=200 auth=200\n", sentinel.GrafanaAuthOK},
		{"health=200 auth=401\n", sentinel.GrafanaCredentialDrift},
		{"health=200 auth=403\n", sentinel.GrafanaUnknown},
		{"health=200 auth=\n", sentinel.GrafanaUnknown},
		{"health= auth=\n", sentinel.GrafanaUnhealthy},
		{"health=503 auth=401\n", sentinel.GrafanaUnhealthy},
		{"", sentinel.GrafanaUnhealthy},
	}
	for _, tc := range cases {
		if got := sentinel.ParseGrafanaProbe(tc.out).State; got != tc.want {
			t.Errorf("ParseGrafanaProbe(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

// grafanaMock answers the probe with the given outputs in order and records
// which exec scripts ran. cluster-info calls get empty output.
func grafanaMock(probes ...string) (*core.MockExecutor, *[]string) {
	var scripts []string
	i := 0
	mock := &core.MockExecutor{}
	mock.CommandFunc = func(spec core.ExecSpec) *core.MockCommand {
		if len(spec.Args) > 0 && spec.Args[0] == "exec" {
			script := spec.Args[len(spec.Args)-1]
			scripts = append(scripts, script)
			switch {
			case strings.Contains(script, "api/health"):
				out := ""
				if i < len(probes) {
					out = probes[i]
				}
				i++
				return &core.MockCommand{OutputData: []byte(out)}
			case strings.Contains(script, "cp /var/lib/grafana/grafana.db"):
				return &core.MockCommand{OutputData: []byte("/var/lib/grafana/backups/grafana.db.1\n")}
			}
		}
		return &core.MockCommand{}
	}
	return mock, &scripts
}

func TestGrafanaCheckIsReadOnly(t *testing.T) {
	mock, scripts := grafanaMock("health=200 auth=401\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())

	if err := mgr.ShowGrafanaCheck(); err == nil {
		t.Fatal("expected error on credential drift")
	}
	if len(*scripts) != 1 {
		t.Fatalf("expected a single probe exec, got %d", len(*scripts))
	}
	for _, c := range mock.Commands {
		if len(c.Args) > 0 && (c.Args[0] == "apply" || c.Args[0] == "patch" || c.Args[0] == "delete") {
			t.Fatalf("check must not mutate: %v", c.Args)
		}
	}
}

func TestGrafanaCheckHealthy(t *testing.T) {
	mock, _ := grafanaMock("health=200 auth=200\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ShowGrafanaCheck(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGrafanaResetRequiresConfirmation(t *testing.T) {
	mock, scripts := grafanaMock("health=200 auth=401\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ResetGrafanaAdminPassword(false); err == nil {
		t.Fatal("expected confirmation error")
	}
	if len(*scripts) != 1 {
		t.Fatalf("no backup/reset expected without --yes, got %d execs", len(*scripts))
	}
}

func TestGrafanaResetSkipsWhenHealthy(t *testing.T) {
	mock, scripts := grafanaMock("health=200 auth=200\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ResetGrafanaAdminPassword(true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*scripts) != 1 {
		t.Fatalf("healthy credentials must not trigger reset, got %d execs", len(*scripts))
	}
}

func TestGrafanaResetRefusesWhenUnhealthy(t *testing.T) {
	mock, scripts := grafanaMock("health= auth=\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ResetGrafanaAdminPassword(true); err == nil {
		t.Fatal("expected refusal when Grafana is unhealthy")
	}
	if len(*scripts) != 1 {
		t.Fatalf("unhealthy Grafana must not be reset, got %d execs", len(*scripts))
	}
}

func TestGrafanaResetBacksUpThenResetsAndVerifies(t *testing.T) {
	mock, scripts := grafanaMock("health=200 auth=401\n", "health=200 auth=200\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ResetGrafanaAdminPassword(true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*scripts) != 4 {
		t.Fatalf("expected probe, backup, reset, verify; got %d execs", len(*scripts))
	}
	if !strings.Contains((*scripts)[1], "cp /var/lib/grafana/grafana.db") {
		t.Fatalf("second exec must be the backup: %q", (*scripts)[1])
	}
	reset := (*scripts)[2]
	if !strings.Contains(reset, "--password-from-stdin") || !strings.Contains(reset, "$GF_SECURITY_ADMIN_PASSWORD") {
		t.Fatalf("reset must pipe the pod-held password over stdin: %q", reset)
	}
	// The password value must never be placed in a CLI argument.
	for _, c := range mock.Commands {
		if strings.Contains(strings.Join(c.Args, " "), "changeme") {
			t.Fatalf("password leaked into args: %v", c.Args)
		}
	}
}

func TestGrafanaResetFailsWhenVerifyStillRejected(t *testing.T) {
	mock, _ := grafanaMock("health=200 auth=401\n", "health=200 auth=401\n")
	mgr := sentinel.NewSentinelManager(core.NewTestKubectlClient(mock), zap.NewNop())
	if err := mgr.ResetGrafanaAdminPassword(true); err == nil {
		t.Fatal("expected verification failure")
	}
}
