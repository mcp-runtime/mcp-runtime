package credentialheaders

import (
	"strings"
	"testing"
)

func TestValidateGatewayHeader(t *testing.T) {
	for _, ok := range []string{"Authorization", "X-GitLab-PAT-Token", "X_Api_Key", "1st-Token", "api~key"} {
		if err := ValidateGatewayHeader(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Host", "Cookie", "Via", "Accept-Encoding", "Last-Event-Id", "Sec-Fetch-Mode", "X-Forwarded-For", "Mcp-Session-Id", "bad name", "bad\nname"} {
		if err := ValidateGatewayHeader(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateInjectedValue(t *testing.T) {
	for _, ok := range []string{"glpat-abc_123", "Bearer abc.def", `p@ss&w=rd%20+/;"'<>{}\|~^`, "inner  space", "tab\there"} {
		if err := ValidateInjectedValue(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"empty": "", "leading space": " x", "trailing space": "x ", "CR": "a\rb", "LF": "a\nb", "NUL": "a\x00b",
		"DEL": "a\x7fb", "non-ASCII": "café", "too long": strings.Repeat("a", 8193),
	} {
		if err := ValidateInjectedValue(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
