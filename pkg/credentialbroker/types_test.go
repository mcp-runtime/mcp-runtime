package credentialbroker

import (
	"net/http"
	"strings"
	"testing"
)

const testHuman = "11111111-1111-1111-1111-111111111111"
const testTeam = "22222222-2222-2222-2222-222222222222"

// Cloud reference checks run through validateReference: those providers are
// disabled (see registry_test.go) but their reference rules are retained.
func TestProviderReferencesCannotSelectAnotherNamespaceOrEndpoint(t *testing.T) {
	valid := []Source{
		{Provider: "kubernetes", Resource: "upstream-token", Key: "token"},
		{Provider: "gcp", Resource: "projects/project-1/secrets/upstream", Version: "latest"},
		{Provider: "aws", Resource: "arn:aws:secretsmanager:us-east-1:123456789012:secret:upstream-Abc123"},
		{Provider: "azure", Resource: "https://fixture.vault.azure.net/secrets/upstream"},
		{Provider: "vault", Resource: "secret/team/upstream", Key: "token"},
	}
	for _, source := range valid {
		source.Namespace, source.Name, source.TeamID = "team-one", "upstream", testTeam
		if err := source.validateReference(); err != nil {
			t.Fatalf("provider %s: %v", source.Provider, err)
		}
	}
	for _, source := range []Source{
		{Provider: "kubernetes", Resource: "foreign/secret", Key: "token"}, {Provider: "kubernetes", Resource: "secret", Key: "token", Version: "v1"},
		{Provider: "azure", Resource: "https://fixture.vault.azure.net.evil.test/secrets/name"}, {Provider: "azure", Resource: "https://fixture.vault.azure.net/secrets/name?token=hidden"},
		{Provider: "gcp", Resource: "https://evil.test/secret"}, {Provider: "vault", Resource: "secret/../private", Key: "token"}, {Provider: "missing", Resource: "secret"},
	} {
		source.Namespace, source.Name, source.TeamID = "team-one", "upstream", testTeam
		if source.validateReference() == nil {
			t.Fatalf("accepted unsafe %s reference", source.Provider)
		}
	}
}

func TestProviderVersionCannotTraverseOrNest(t *testing.T) {
	resources := map[string]string{
		"gcp":   "projects/project-1/secrets/upstream",
		"aws":   "arn:aws:secretsmanager:us-east-1:123456789012:secret:upstream-Abc123",
		"azure": "https://fixture.vault.azure.net/secrets/upstream",
	}
	for provider, resource := range resources {
		for version, ok := range map[string]bool{"latest": true, "3": true, "AWSCURRENT": true, "a_b-C9": true, "..": false, ".": false, "a/b": false, "": true, "a.b": false, "v:x": false} {
			source := Source{Provider: provider, Resource: resource, Version: version, Namespace: "team-one", Name: "upstream", TeamID: testTeam}
			if got := source.validateReference() == nil; got != ok {
				t.Errorf("%s version %q: accepted=%v want %v", provider, version, got, ok)
			}
		}
	}
}

func TestBindingRequiresExplicitFullIdentityToolsAndReservedDeliveryHeader(t *testing.T) {
	base := Binding{Namespace: "team-one", Name: "upstream", Source: "source", Server: "server", ServerUID: "uid", Tools: []string{"search"}, HumanID: testHuman, AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: testTeam, Header: HeaderPrefix + "Token"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Binding){func(b *Binding) { b.HumanID = "" }, func(b *Binding) { b.AgentID = "unverified-agent" }, func(b *Binding) { b.TeamID = "" }, func(b *Binding) { b.Tools = []string{"*"} }, func(b *Binding) { b.Header = "Authorization" }, func(b *Binding) { b.Header = HeaderPrefix + "Token\r\nBad" }, func(b *Binding) { b.SessionID = "session" }} {
		candidate := base
		change(&candidate)
		if candidate.Validate() == nil {
			t.Fatal("incomplete binding accepted")
		}
	}
	if !base.Matches(testHuman, base.AgentID, testTeam, "team-one/session", "search") || base.Matches(testHuman, "other", testTeam, "team-one/session", "search") || base.Matches(testHuman, base.AgentID, testTeam, "team-one/session", "delete") {
		t.Fatal("binding subject/tool narrowing failed")
	}
	base.SessionID = "team-one/session"
	if base.Matches(testHuman, base.AgentID, testTeam, "team-one/other", "search") {
		t.Fatal("foreign session accepted")
	}
}

func TestCredentialHeadersAndValuesRejectInjection(t *testing.T) {
	h := http.Header{"x-mcp-credential-token": []string{"forged"}, "X-Mcp-Credential-Other": []string{"forged"}, "Accept": []string{"application/json"}}
	StripHeaders(h)
	for key := range h {
		if strings.HasPrefix(strings.ToLower(key), "x-mcp-credential-") {
			t.Fatal("forged credential survived")
		}
	}
	if !ValidValue([]byte("opaque upstream token")) {
		t.Fatal("safe opaque value rejected")
	}
	for _, value := range []string{"", "secret\r\nInjected: true", "secret\x00", " secret", "secret ", string([]byte{0xff}), strings.Repeat("a", MaxValueBytes+1)} {
		if ValidValue([]byte(value)) {
			t.Fatal("unsafe value accepted")
		}
	}
}

func TestCredentialUpstreamMustStayInTheServerPod(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8088", "http://[::1]:8088/mcp"} {
		if !LocalUpstream(raw, 8088) {
			t.Fatal("same-Pod upstream rejected")
		}
	}
	for _, raw := range []string{"http://foreign.namespace.svc:8088", "http://127.0.0.1.evil.test:8088", "http://127.0.0.1:8089", "http://user:secret@127.0.0.1:8088", "https://127.0.0.1:8088", "http://127.0.0.1:8088?destination=foreign"} {
		if LocalUpstream(raw, 8088) {
			t.Fatal("credential can escape its workload")
		}
	}
}
