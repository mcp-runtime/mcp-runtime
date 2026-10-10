package doctor

import (
	"errors"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestCheckPlatformGrafanaAdminCredentials(t *testing.T) {
	type fixture struct {
		namespaceMissing bool
		replicas         string
		probe            string
		probeErr         error
	}
	run := func(f fixture) (DoctorCheck, []string) {
		var execArgs []string
		mock := &core.MockExecutor{
			CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				args := strings.Join(spec.Args, " ")
				switch {
				case strings.Contains(args, "get namespace"):
					if f.namespaceMissing {
						return &core.MockCommand{OutputData: []byte("not found"), OutputErr: errors.New("not found"), RunErr: errors.New("not found")}
					}
					return &core.MockCommand{OutputData: []byte("mcp-observability")}
				case strings.Contains(args, "get deployment grafana"):
					return &core.MockCommand{OutputData: []byte("grafana")}
				case strings.Contains(args, "get deploy -n"):
					return &core.MockCommand{OutputData: []byte(f.replicas)}
				case strings.HasPrefix(args, "exec "):
					execArgs = spec.Args
					return &core.MockCommand{OutputData: []byte(f.probe), OutputErr: f.probeErr}
				}
				return &core.MockCommand{}
			},
		}
		return checkPlatformGrafanaAdminCredentials(core.NewTestKubectlClient(mock)), execArgs
	}

	c, execArgs := run(fixture{replicas: "1/1", probe: "health=200 auth=200\n"})
	if !c.OK {
		t.Fatalf("expected OK for matching credentials, got %q", c.Detail)
	}
	joined := strings.Join(execArgs, " ")
	if !strings.Contains(joined, "deploy/grafana") || !strings.Contains(joined, "$GF_SECURITY_ADMIN_PASSWORD") {
		t.Fatalf("expected in-pod probe reading credentials from the pod environment, got %q", joined)
	}

	for name, tc := range map[string]struct {
		f      fixture
		ok     bool
		detail string
		remedy string
	}{
		"drift":             {fixture{replicas: "1/1", probe: "health=200 auth=401"}, false, "drifted", "reset-admin-password --yes"},
		"unhealthy":         {fixture{replicas: "1/1", probe: "health=503 auth="}, false, "not evaluated", "ops logs grafana"},
		"unexpected":        {fixture{replicas: "1/1", probe: "health=200 auth=500"}, false, "auth=500", "ops grafana check"},
		"probe error":       {fixture{replicas: "1/1", probeErr: errors.New("exit status 1")}, false, "could not run", "ops grafana check"},
		"not ready":         {fixture{replicas: "0/1"}, true, "not ready", ""},
		"namespace missing": {fixture{namespaceMissing: true}, true, "skipping", ""},
	} {
		got, _ := run(tc.f)
		if got.OK != tc.ok || !strings.Contains(got.Detail, tc.detail) || !strings.Contains(got.Remedy, tc.remedy) {
			t.Errorf("%s: got OK=%v detail=%q remedy=%q", name, got.OK, got.Detail, got.Remedy)
		}
	}
}
