package platformauth

import "testing"

func TestResolveAuthURL(t *testing.T) {
	got, err := resolveAuthURL("http://mcp-platform-api.mcp-platform.svc.cluster.local:8080")
	if err != nil {
		t.Fatalf("resolveAuthURL() err = %v", err)
	}
	want := "http://mcp-platform-api.mcp-platform.svc.cluster.local:8080/internal/auth/resolve"
	if got != want {
		t.Fatalf("resolveAuthURL() = %q, want %q", got, want)
	}
}

func TestParseServiceBaseURLRejectsMetadata(t *testing.T) {
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"ftp://mcp-platform-api:8080",
		"http://user:pass@mcp-platform-api:8080",
		"",
	} {
		if _, err := parseServiceBaseURL(raw); err == nil {
			t.Fatalf("parseServiceBaseURL(%q) err = nil, want error", raw)
		}
	}
}
