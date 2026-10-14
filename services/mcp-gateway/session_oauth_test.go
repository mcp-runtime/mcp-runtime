package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func TestOAuthSIDResolvesExternalConsentBinding(t *testing.T) {
	issuer := newTestJWTIssuer(t)
	server := minimalServer()
	server.httpClient = issuer.server.Client()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, nil)
	ex.Policy = oauthPolicy(issuer.url)
	ex.Policy.Session = &policy.Session{Required: true, Store: "platform"}
	ex.Inspection = rpcInspection{Method: "tools/call", ToolCall: true}
	namespace := string(ex.Policy.Server.Namespace)
	binding := policy.Binding{Name: "session", Namespace: policy.Namespace(namespace), HumanID: "human", AgentID: "agent", TeamID: "team-id", ConsentedTrust: "low"}
	server.sessionSync = &gatewaySessionSync{lastSync: time.Now(), staleWindow: time.Minute, sessions: map[string]sessionstate.Session{namespace + "/session": {Binding: binding}}}
	token := issuer.sign(t, jwt.MapClaims{"iss": issuer.url, "aud": "http://proxy.example.com/mcp", "sub": "human", "azp": "agent", "team_id": "team-id", "sid": "session", "exp": time.Now().Add(time.Hour).Unix()})
	ex.R.Header.Set("Authorization", "Bearer "+token)
	if result := server.authFilter(ex); result != Continue || ex.Identity.Session == nil || ex.Identity.Session.ConsentedTrust != "low" {
		t.Fatalf("result=%v identity=%+v decision=%+v", result, ex.Identity, ex.Decision)
	}
	// An identity mismatch must never receive another principal's consent.
	binding.HumanID = "other-human"
	server.sessionSync.sessions[namespace+"/session"] = sessionstate.Session{Binding: binding}
	if result := server.authFilter(ex); result != Reject || ex.Decision.Reason != "session_identity_mismatch" {
		t.Fatalf("result=%v decision=%+v", result, ex.Decision)
	}
	// With optional sessions preserve the grant-only OAuth path.
	ex.Policy.Session.Required = false
	if result := server.authFilter(ex); result != Continue {
		t.Fatalf("optional session result=%v", result)
	}
}
