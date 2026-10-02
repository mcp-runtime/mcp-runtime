package doctor

import (
	"errors"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func TestCheckMCPAuthDeploymentRequiresCurrentRollout(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		err          error
		wantOK       bool
	}{
		{name: "optional absent", wantOK: true},
		{name: "API forbidden", err: errors.New("forbidden")},
		{name: "invalid JSON", output: "not json"},
		{name: "old ready pod and crashing replacement", output: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"replicas":2,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}`},
		{name: "new generation unobserved", output: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}`},
		{name: "candidate not available", output: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"replicas":1,"updatedReplicas":1,"readyReplicas":1}}`},
		{name: "progress deadline exceeded", output: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded"}]}}`},
		{name: "scaled to zero", output: `{"metadata":{"generation":2},"spec":{"replicas":0},"status":{"observedGeneration":2}}`},
		{name: "current revision ready", output: `{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}`, wantOK: true},
		{name: "two current replicas ready", output: `{"metadata":{"generation":2},"spec":{"replicas":2},"status":{"observedGeneration":2,"replicas":2,"updatedReplicas":2,"readyReplicas":2,"availableReplicas":2}}`, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				if strings.Join(spec.Args, " ") != "get deployment mcp-auth-server -n mcp-platform --ignore-not-found -o json" {
					t.Fatalf("unexpected command: %v", spec.Args)
				}
				return &core.MockCommand{OutputData: []byte(tc.output), OutputErr: tc.err}
			}}
			check := checkMCPAuthDeployment(core.NewTestKubectlClient(mock))
			if check.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v; detail: %s", check.OK, tc.wantOK, check.Detail)
			}
		})
	}
}
