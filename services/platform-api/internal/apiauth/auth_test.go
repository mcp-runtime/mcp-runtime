package apiauth

import (
	"net/http/httptest"
	"testing"
)

func TestRequestIPUsesFirstNonEmptyForwardedForHop(t *testing.T) {
	t.Setenv("MCP_TRUSTED_PROXY_CIDRS", "10.0.0.0/24")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("x-forwarded-for", "203.0.113.10, 10.0.0.2")

	if got := RequestIP(req); got != "203.0.113.10" {
		t.Fatalf("RequestIP() = %q, want forwarded client", got)
	}
}

func TestRequestIPEmptyForwardedForFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("x-forwarded-for", " , ")

	if got := RequestIP(req); got != "198.51.100.20" {
		t.Fatalf("RequestIP() = %q, want remote addr host", got)
	}
}

func TestRequestIPUnknownWhenNoAddressAvailable(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = ""

	if got := RequestIP(req); got != UnknownRequestIP {
		t.Fatalf("RequestIP() = %q, want %q", got, UnknownRequestIP)
	}
}
