package credentialheaders

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidateNameAcceptsArbitraryCustomerHeaders(t *testing.T) {
	for _, name := range []string{"X-Example-Credential", "Private-Token", "Authorization", "JOB-TOKEN"} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestValidateNameRejectsReservedHeaders(t *testing.T) {
	for _, name := range []string{"", "Host", "Connection", "X-Forwarded-For", "X-MCP-Verified-SPIFFE-ID", "Mcp-Session-Id", "Cookie", "Transfer-Encoding", "bad name"} {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) succeeded", name)
		}
	}
}

func TestValidateValueHidesTheCredential(t *testing.T) {
	secret := "super-secret-token"
	err := ValidateValue("  ")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("empty error = %v", err)
	}
	err = ValidateValue(secret + "\n")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("control error = %v", err)
	}
	if err := ValidateValue(secret); err != nil {
		t.Fatal(err)
	}
	if err := ValidateValue("Bearer " + secret); err != nil {
		t.Fatalf("bearer value rejected: %v", err)
	}
}

func TestCheckRequestAnyAndAll(t *testing.T) {
	names := []string{"X-Example-Credential", "Private-Token"}
	h := http.Header{"X-Example-Credential": []string{"one"}}
	if got := CheckRequest(h, names, "any"); got != "" {
		t.Fatalf("any = %q", got)
	}
	if got := CheckRequest(h, names, ""); got != "" {
		t.Fatalf("default any = %q", got)
	}
	if got := CheckRequest(h, names, "all"); got != "missing_credential" {
		t.Fatalf("all = %q", got)
	}
	h.Set("Private-Token", "two")
	if got := CheckRequest(h, names, "all"); got != "" {
		t.Fatalf("all present = %q", got)
	}
	if got := CheckRequest(http.Header{}, names, "any"); got != "missing_credential" {
		t.Fatalf("missing = %q", got)
	}
	if got := CheckRequest(http.Header{"X-Example-Credential": []string{"  "}}, names, "any"); got != "empty_credential" {
		t.Fatalf("empty = %q", got)
	}
	dup := http.Header{"X-Example-Credential": []string{"one", "two"}}
	if got := CheckRequest(dup, names, "any"); got != "duplicate_credential" {
		t.Fatalf("duplicate = %q", got)
	}
	if got := CheckRequest(h, []string{"X-Forwarded-For"}, "any"); got != "invalid_credential_config" {
		t.Fatalf("reserved = %q", got)
	}
}
