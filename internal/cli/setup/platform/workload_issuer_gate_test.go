package platform

import (
	"strings"
	"testing"
)

func TestValidateWorkloadIssuerApprovalGate(t *testing.T) {
	previous := workloadIssuerApproverPolicyInstalled
	t.Cleanup(func() { workloadIssuerApproverPolicyInstalled = previous })

	cases := []struct {
		name      string
		enabled   string
		testMode  string
		ack       string
		installed bool
		wantErr   bool
	}{
		{name: "feature disabled", enabled: "false"},
		{name: "enabled without approver is refused", enabled: "true", wantErr: true},
		{name: "false ack does not pass", enabled: "true", ack: "false", wantErr: true},
		{name: "approver-policy installed", enabled: "true", installed: true},
		{name: "explicit acknowledgement", enabled: "true", ack: "true"},
		{name: "test mode exempt", enabled: "true", testMode: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_ADAPTER_CERTIFICATES", tc.enabled)
			t.Setenv("MCP_RUNTIME_TEST_MODE", tc.testMode)
			t.Setenv(workloadIssuerApprovalAckEnv, tc.ack)
			workloadIssuerApproverPolicyInstalled = func() bool { return tc.installed }
			err := validateWorkloadIssuerApprovalGate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), workloadIssuerApprovalAckEnv) {
				t.Fatalf("error should name the acknowledgement env: %v", err)
			}
		})
	}
}
