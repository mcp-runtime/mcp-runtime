package doctor

import (
	"errors"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestOperatorEffectivePermissions(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		err         error
		want        bool
	}{
		{name: "all required grants", reply: `{"status":{"allowed":true}}`, want: true},
		{name: "missing binding", reply: `{"status":{"allowed":false}}`},
		{name: "authorization error", reply: `{"status":{"allowed":true,"evaluationError":"authorizer unavailable"}}`},
		{name: "denied", reply: `{"status":{"allowed":true,"denied":true}}`},
		{name: "request failure", err: errors.New("cannot create subjectaccessreviews")},
		{name: "malformed result", reply: `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if contains(spec.Args, "--ignore-not-found") {
					return &core.MockCommand{OutputData: []byte("")}
				}
				if !contains(spec.Args, "--raw=/apis/authorization.k8s.io/v1/subjectaccessreviews") {
					t.Fatalf("unexpected request %v", spec.Args)
				}
				requests++
				return &core.MockCommand{OutputData: []byte(tc.reply), OutputErr: tc.err}
			}}
			check := checkOperatorEffectivePermissions(core.NewTestKubectlClient(mock))
			if check.OK != tc.want || requests == 0 {
				t.Fatalf("check=%+v requests=%d", check, requests)
			}
			if tc.name == "missing binding" && (!strings.Contains(check.Detail, "watch endpointslices.discovery.k8s.io") || !strings.Contains(check.Detail, "create secrets.")) {
				t.Fatalf("missing grants not explained: %s", check.Detail)
			}
		})
	}
}

func TestOperatorForbiddenWatchLogsFailDoctor(t *testing.T) {
	mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		return &core.MockCommand{OutputData: []byte(`cannot list resource "endpointslices": forbidden`)}
	}}
	if check := checkOperatorRecentReconcileErrors(core.NewTestKubectlClient(mock)); check.OK {
		t.Fatal("forbidden informer logs must fail doctor")
	}
}
