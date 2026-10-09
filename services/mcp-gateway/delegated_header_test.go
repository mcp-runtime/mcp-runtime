package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	policypkg "mcp-runtime/pkg/policy"
)

func delegatedPolicy(headers []string, presence string, rules ...policypkg.DelegatedToolRule) *policypkg.Document {
	doc := &policypkg.Document{
		Server: policypkg.Server{Name: "pilot", Namespace: "mcp-servers"},
		Auth: &policypkg.Auth{
			Mode:               "header",
			Headers:            headers,
			CredentialPresence: presence,
		},
		Policy: &policypkg.Config{
			Mode:               "allow-list",
			DefaultDecision:    "deny",
			PolicyVersion:      "v1",
			DelegatedToolRules: rules,
		},
		Tools: []policypkg.Tool{{Name: "list_projects", SideEffect: "read"}, {Name: "delete_project", SideEffect: "destructive"}},
	}
	if err := policypkg.Stamp(doc, ""); err != nil {
		panic(err)
	}
	return doc
}

func TestDelegatedHeaderForwardsConfiguredCredentialsAndStripsIdentity(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var authz, custom, session, spiffe string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		authz = r.Header.Get("Authorization")
		custom = r.Header.Get("X-Example-Credential")
		session = r.Header.Get("Mcp-Session-Id")
		spiffe = r.Header.Get(defaultVerifiedSPIFFEHeader)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	s.verifiedSPIFFEHeader = defaultVerifiedSPIFFEHeader
	doc := delegatedPolicy([]string{"X-Example-Credential", "Private-Token"}, "any", policypkg.DelegatedToolRule{Name: "list_projects", Decision: "allow"})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects"}}`
	ex := newTestExchange(http.MethodPost, "/pilot/mcp", body, map[string]string{
		"Content-Type":              "application/json",
		"X-Example-Credential":      "synthetic-token",
		"Mcp-Session-Id":            "session-1",
		defaultVerifiedSPIFFEHeader: "spiffe://example.org/ns/team/sa/forged",
	})
	ex.Policy = doc
	s.inspectFilter(ex)
	if got := s.authFilter(ex); got != Continue {
		t.Fatalf("authFilter = %v", got)
	}
	if ex.Identity.HumanID != "" || ex.Identity.AgentID != "" {
		t.Fatalf("identity = %#v, want unverified", ex.Identity)
	}
	if got := s.authzFilter(ex); got != Continue {
		t.Fatalf("authz = %#v", ex.Decision)
	}
	if got := s.upstreamFilter(ex); got != Respond {
		t.Fatal(got)
	}
	if calls.Load() != 1 || custom != "synthetic-token" || authz != "" || session != "session-1" || spiffe != "" {
		t.Fatalf("calls=%d authz=%q custom=%q session=%q spiffe=%q", calls.Load(), authz, custom, session, spiffe)
	}
	if ex.W.status != http.StatusOK {
		t.Fatalf("status = %d", ex.W.status)
	}
	payload := s.auditPayload(ex.R, ex.OriginalPath, "tools/call", "list_projects", ex.Identity, doc, ex.Decision, ex.W.status, 1, 1)
	encoded := strings.ToLower(strings.Join([]string{
		payload["reason"].(string), payload["auth_delegation"].(string), payload["caller_identity"].(string), payload["human_id"].(string),
	}, " "))
	if strings.Contains(encoded, "synthetic-token") || payload["caller_identity"] != "unverified" || payload["auth_delegation"] != "upstream_header" {
		t.Fatalf("audit = %#v", payload)
	}
}

func TestDelegatedHeaderDenyDoesNotReachUpstream(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	doc := delegatedPolicy([]string{"X-Example-Credential"}, "any", policypkg.DelegatedToolRule{Name: "list_projects", Decision: "allow"})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_project"}}`
	ex := newTestExchange(http.MethodPost, "/pilot/mcp", body, map[string]string{
		"Content-Type":         "application/json",
		"X-Example-Credential": "synthetic-token",
	})
	ex.Policy = doc
	s.inspectFilter(ex)
	if s.authFilter(ex) != Continue || s.authzFilter(ex) != Reject {
		t.Fatalf("decision = %#v", ex.Decision)
	}
	if calls.Load() != 0 || ex.Decision.Reason != "tool_side_effect_unknown" && ex.Decision.Reason != "tool_not_allowed" {
		t.Fatalf("calls=%d decision=%#v", calls.Load(), ex.Decision)
	}
}

func TestDelegatedHeaderForwardsOpaqueSchemes(t *testing.T) {
	t.Parallel()
	var basic, apiKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		basic = r.Header.Get("Authorization")
		apiKey = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	doc := delegatedPolicy([]string{"Authorization", "X-Api-Key"}, "all")
	ex := newTestExchange(http.MethodPost, "/pilot/mcp", `{"method":"initialize"}`, map[string]string{
		"Authorization": "Basic c3ludGhldGlj",
		"X-Api-Key":     "raw-token",
	})
	ex.Policy = doc
	s.inspectFilter(ex)
	if s.authFilter(ex) != Continue || s.authzFilter(ex) != Continue || s.upstreamFilter(ex) != Respond {
		t.Fatalf("decision = %#v", ex.Decision)
	}
	if basic != "Basic c3ludGhldGlj" || apiKey != "raw-token" {
		t.Fatalf("basic=%q apiKey=%q", basic, apiKey)
	}
}

func TestDelegatedHeaderMissingCredentialAndUpstreamRejection(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("Authorization = %q, want the bearer value forwarded unchanged", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	s := minimalServer()
	s.proxy = newUpstreamReverseProxy(target)
	doc := delegatedPolicy([]string{"Authorization", "X-Example-Credential"}, "any")
	missing := newTestExchange(http.MethodPost, "/pilot/mcp", `{"method":"initialize"}`, nil)
	missing.Policy = doc
	if s.authFilter(missing) != Reject || missing.Decision.Reason != "missing_credential" {
		t.Fatalf("missing = %#v", missing.Decision)
	}
	if calls.Load() != 0 {
		t.Fatal("missing credential reached upstream")
	}
	ex := newTestExchange(http.MethodPost, "/pilot/mcp", `{"method":"initialize"}`, map[string]string{"Authorization": "Bearer synthetic"})
	ex.Policy = doc
	s.inspectFilter(ex)
	if s.authFilter(ex) != Continue || s.authzFilter(ex) != Continue || s.upstreamFilter(ex) != Respond {
		t.Fatalf("forward decision = %#v", ex.Decision)
	}
	if calls.Load() != 1 || ex.W.status != http.StatusUnauthorized {
		t.Fatalf("calls=%d status=%d", calls.Load(), ex.W.status)
	}
}

func TestDelegatedHeaderPolicyUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	doc := delegatedPolicy([]string{"X-Example-Credential"}, "any")
	ex := newTestExchange(http.MethodPost, "/pilot/mcp", `{"method":"tools/call","params":{"name":"list_projects"}}`, map[string]string{
		"Content-Type":         "application/json",
		"X-Example-Credential": "synthetic-token",
	})
	ex.Policy = doc
	ex.PolicyErr = errPolicyUnavailable
	s.inspectFilter(ex)
	if s.authzFilter(ex) != Reject || ex.Decision.Reason != "policy_unavailable" {
		t.Fatalf("decision = %#v", ex.Decision)
	}
}

func TestOAuthFailureDoesNotFallBackToHeader(t *testing.T) {
	t.Parallel()
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, map[string]string{"X-Example-Credential": "synthetic-token"})
	ex.Policy = oauthPolicy("https://issuer.example.com")
	if got := s.authFilter(ex); got != Reject {
		t.Fatalf("authFilter = %v, want OAuth rejection", got)
	}
}
