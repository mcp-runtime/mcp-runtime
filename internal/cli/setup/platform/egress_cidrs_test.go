package platform

import (
	"strings"
	"testing"

	setupplan "mcp-runtime/internal/cli/setup/plan"
)

func TestRenderPodEgressCIDRs(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_CIDRS", "10.0.0.0/8")
	t.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", "10.42.0.0/16,10.43.0.0/16")
	content := "" +
		"            - name: MCP_POD_EGRESS_CIDRS\n" +
		"              value: \"\" # MCP_POD_EGRESS_CIDRS\n" +
		"            - name: MCP_POD_EGRESS_EXCEPT_CIDRS\n" +
		"              value: \"\" # MCP_POD_EGRESS_EXCEPT_CIDRS\n"
	rendered, err := renderAnalyticsManifest(content, AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, `value: "10.0.0.0/8" # MCP_POD_EGRESS_CIDRS`) {
		t.Fatalf("destination CIDR was not rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, `value: "10.42.0.0/16,10.43.0.0/16" # MCP_POD_EGRESS_EXCEPT_CIDRS`) {
		t.Fatalf("exception CIDRs were not rendered:\n%s", rendered)
	}
}

func TestRenderPodEgressCIDRsRejectsDefaultRoute(t *testing.T) {
	t.Setenv("MCP_POD_EGRESS_CIDRS", "0.0.0.0/0")
	t.Setenv("MCP_POD_EGRESS_EXCEPT_CIDRS", "")
	_, err := renderAnalyticsManifest("value: \"\" # MCP_POD_EGRESS_CIDRS\n", AnalyticsImageSet{}, "", setupplan.PlatformModeTenant)
	if err == nil {
		t.Fatal("default route was accepted")
	}
}
