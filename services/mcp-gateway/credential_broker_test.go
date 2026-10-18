package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/policy"
)

func TestGatewayBrokerUsesVerifiedSessionAndPrivateRotatingUpstreamDelivery(t *testing.T) {
	var calls atomic.Int64
	value := "fixture-upstream-one"
	broker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-pod-token" || r.URL.Path != cb.ResolvePath || r.Header.Get("X-Human-Id") != "" {
			t.Error("broker authentication/path/identity boundary changed")
		}
		var request cb.ResolutionRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Session != "team/session" || request.Tool != "search" || len(request.Bindings) != 1 || request.Bindings[0] != "upstream" {
			t.Error("broker request did not use authenticated session/tool policy")
		}
		_ = json.NewEncoder(w).Encode(cb.Delivery{Headers: map[string]string{cb.HeaderPrefix + "Token": value}})
	}))
	defer broker.Close()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(tokenFile, []byte("fixture-pod-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: broker.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := newCredentialBroker(broker.URL, tokenFile, caFile)
	if err != nil {
		t.Fatal(err)
	}
	s := minimalServer()
	s.credentialBroker = client
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get(cb.HeaderPrefix+"Token") != value || r.Header.Get(cb.HeaderPrefix+"Forged") != "" {
			t.Error("private value not injected or forged credential survived")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	s.upstreamURL = target
	s.proxy = newUpstreamReverseProxy(target)
	newExchange := func() *Exchange {
		ex := newTestExchange(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search"}}`, nil)
		ex.Policy = &policy.Document{CredentialBindings: []string{"upstream"}}
		ex.Inspection = rpcInspection{Method: "tools/call", ToolName: "search", ToolCall: true}
		ex.Identity = identityContext{SessionID: "session", Session: &policy.Binding{Name: "session", Namespace: "team", HumanID: "human", AgentID: "verified-agent"}}
		ex.Decision = policy.Allow("fixture", "v1")
		ex.R.Header.Set(cb.HeaderPrefix+"Token", "caller-forged")
		ex.R.Header.Set(cb.HeaderPrefix+"Forged", "caller-forged")
		return ex
	}
	for _, rotated := range []string{"fixture-upstream-one", "fixture-upstream-two"} {
		value = rotated
		ex := newExchange()
		if s.upstreamFilter(ex) != Respond {
			t.Fatal("upstream did not complete")
		}
		if strings.Contains(ex.W.ResponseWriter.(*httptest.ResponseRecorder).Body.String(), "fixture-upstream") {
			t.Fatal("credential exposed in gateway response")
		}
	}
	if calls.Load() != 2 || upstreamCalls != 2 {
		t.Fatal("broker value was cached")
	}
	ex := newExchange()
	ex.Identity.Session = nil
	if s.upstreamFilter(ex) != Respond || ex.Decision.Allowed || ex.W.ResponseWriter.(*httptest.ResponseRecorder).Code != 503 || calls.Load() != 2 || upstreamCalls != 2 {
		t.Fatal("sessionless call reached broker/upstream")
	}
	s.credentialBroker = nil
	ex = newExchange()
	s.upstreamFilter(ex)
	if ex.Decision.Allowed || upstreamCalls != 2 {
		t.Fatal("missing broker allowed a tool call")
	}
}

func TestBrokerRejectsPlaintextUntrustedTLSRedirectsAndUnrelatedHeaders(t *testing.T) {
	if _, err := newCredentialBroker("http://platform.test", "/projected/token", ""); err == nil {
		t.Fatal("plaintext broker configured")
	}
	for _, origin := range []string{"https://user:secret@platform.test", "https://platform.test?token=secret", "https://platform.test/wrong/path"} {
		if _, err := newCredentialBroker(origin, "/token", ""); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cb.Delivery{Headers: map[string]string{"Authorization": "forbidden"}})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(file, []byte("fixture"), 0600)
	client, err := newCredentialBroker(server.URL, file, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.resolve(t.Context(), cb.ResolutionRequest{}); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	client.client = server.Client()
	if _, err = client.resolve(t.Context(), cb.ResolutionRequest{}); err == nil {
		t.Fatal("broker overwrote an unrelated authentication header")
	}
	var redirected atomic.Int64
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redirect.Certificate().Raw}), 0600)
	client, err = newCredentialBroker(redirect.URL, file, ca)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.resolve(t.Context(), cb.ResolutionRequest{}); err == nil || redirected.Load() != 0 {
		t.Fatal("redirect forwarded projected credential")
	}
}

func TestCallerCredentialHeadersAreStrippedWithoutBrokerOnDiscovery(t *testing.T) {
	s := minimalServer()
	ex := newTestExchange(http.MethodPost, "/mcp", `{}`, nil)
	ex.R.Header.Set(cb.HeaderPrefix+"Token", "forged")
	ex.Inspection = rpcInspection{Method: "tools/list"}
	if !s.injectCredentials(ex) || ex.R.Header.Get(cb.HeaderPrefix+"Token") != "" {
		t.Fatal("discovery retained a caller-injected credential")
	}
}
