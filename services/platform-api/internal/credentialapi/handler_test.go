package credentialapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	mcpv1 "mcp-runtime/api/v1alpha1"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	hID    = "11111111-1111-4111-8111-111111111111"
	tID    = "22222222-2222-4222-8222-222222222222"
	aID    = "agt_0123456789abcdefghjkmnpqrs"
	secret = "provider-secret-value"
)

type fakeStore struct {
	session  sessionstate.Session
	bindings []cb.Binding
	sources  []cb.Source
	deleted  bool
	put      bool
}

func (s *fakeStore) ListCredentials(context.Context, platformauth.Principal, string, string) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"name":"b1"}`)}, nil
}
func (s *fakeStore) PutCredential(context.Context, platformauth.Principal, *cb.Source, *cb.Binding) (json.RawMessage, error) {
	s.put = true
	return json.RawMessage(`{"ok":true}`), nil
}
func (s *fakeStore) DeleteCredential(context.Context, platformauth.Principal, string, string, string, int64) error {
	s.deleted = true
	return nil
}
func (s *fakeStore) UseCredentials(_ context.Context, _ sessionstate.Server, _ cb.ResolutionRequest, fn func(sessionstate.Session, []cb.Binding, []cb.Source) (cb.Delivery, error)) (cb.Delivery, error) {
	return fn(s.session, s.bindings, s.sources)
}

type valueProvider struct{ value []byte }

func (p valueProvider) Read(context.Context, cb.Source) (cb.Value, error) {
	return cb.Value{Bytes: p.value}, nil
}

type fixture struct {
	server  *mcpv1.MCPServer
	grant   *mcpv1.MCPAccessGrant
	store   *fakeStore
	handler Handler
	kube    func() client.Client
}

func newFixture(t *testing.T, mutate func(*mcpv1.MCPServer, *mcpv1.MCPAccessGrant, *fakeStore)) fixture {
	t.Helper()
	server := &mcpv1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "srv", UID: "uid-1"},
		Spec: mcpv1.MCPServerSpec{
			Port:               8088,
			TeamID:             tID,
			CredentialBindings: []string{"b1"},
			Policy:             &mcpv1.PolicyConfig{Mode: mcpv1.PolicyModeAllowList, DefaultDecision: mcpv1.PolicyDecisionDeny, PolicyVersion: "v1"},
			Tools:              []mcpv1.ToolConfig{{Name: "echo", RequiredTrust: "low", SideEffect: "read"}},
		},
	}
	grant := &mcpv1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "g1", Annotations: map[string]string{"mcpruntime.org/server-uid": "uid-1"}},
		Spec: mcpv1.MCPAccessGrantSpec{
			ServerRef: mcpv1.ServerReference{Name: "srv"},
			Subject:   mcpv1.SubjectRef{HumanID: hID, AgentID: aID, TeamID: tID},
			MaxTrust:  "low", AllowedSideEffects: []mcpv1.ToolSideEffect{"read"},
			PolicyVersion: "v1",
			ToolRules:     []mcpv1.ToolRule{{Name: "echo", Decision: "allow"}},
		},
	}
	store := &fakeStore{
		session: sessionstate.Session{
			ID: "ns/s1", Namespace: "ns", ServerNamespace: "ns", ServerName: "srv", ServerUID: "uid-1",
			GrantNamespace: "ns", GrantName: "g1",
			Binding: policy.Binding{Name: "s1", Namespace: "ns", HumanID: hID, AgentID: aID, TeamID: tID, ConsentedTrust: "low"},
		},
		bindings: []cb.Binding{{Namespace: "ns", Name: "b1", Source: "src", Server: "srv", ServerUID: "uid-1", Tools: []string{"echo"}, HumanID: hID, AgentID: aID, TeamID: tID, Header: cb.HeaderPrefix + "Token"}},
		sources:  []cb.Source{{Namespace: "ns", Name: "src", Provider: "kubernetes", Resource: "r", Key: "k", UserID: hID}},
	}
	if mutate != nil {
		mutate(server, grant, store)
	}
	scheme := runtime.NewScheme()
	if err := mcpv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, grant).Build()
	return fixture{server: server, grant: grant, store: store, handler: Handler{
		Store: store, Kube: kube,
		Authenticate: func(_ context.Context, token string) (sessionstate.Server, error) {
			if token != "pod-token" {
				return sessionstate.Server{}, errors.New("bad token")
			}
			return sessionstate.Server{Namespace: "ns", Name: "srv", UID: "uid-1"}, nil
		},
		Providers: map[string]cb.Provider{"kubernetes": valueProvider{[]byte(secret)}},
	}}
}

func resolve(h Handler, mutate func(*http.Request)) *httptest.ResponseRecorder {
	body, _ := json.Marshal(cb.ResolutionRequest{Session: "ns/s1", Tool: "echo", Bindings: []string{"b1"}})
	r := httptest.NewRequest(http.MethodPost, cb.ResolvePath, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer pod-token")
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.Resolve(w, r)
	return w
}

func TestResolveDeliversOnlyForPodBoundIdentityAndCurrentGrant(t *testing.T) {
	f := newFixture(t, nil)
	w := resolve(f.handler, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), secret) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected private no-store delivery, got %d", w.Code)
	}
	var out cb.Delivery
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Headers[cb.HeaderPrefix+"Token"] != secret {
		t.Fatal("header not delivered under reserved name")
	}
}

func TestResolveDeniesWithoutDedicatedTokenOrWithAmbientCredentials(t *testing.T) {
	f := newFixture(t, nil)
	for name, mut := range map[string]func(*http.Request){
		"no bearer":    func(r *http.Request) { r.Header.Del("Authorization") },
		"wrong token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer other") },
		"api key":      func(r *http.Request) { r.Header.Set("X-Api-Key", "k") },
		"basic scheme": func(r *http.Request) { r.Header.Set("Authorization", "Basic abc") },
	} {
		if w := resolve(f.handler, mut); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("%s: got %d", name, w.Code)
		}
	}
}

func TestResolveDeniesWhenCurrentServerGrantOrSessionNoLongerAuthorize(t *testing.T) {
	cases := map[string]func(*mcpv1.MCPServer, *mcpv1.MCPAccessGrant, *fakeStore){
		"server UID replaced": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) { s.UID = types.UID("uid-2") },
		"server retiring": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) {
			s.Annotations = map[string]string{"mcpruntime.org/retiring": "true"}
		},
		"binding not on server": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) { s.Spec.CredentialBindings = nil },
		"policy not deny default": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) {
			s.Spec.Policy.DefaultDecision = mcpv1.PolicyDecisionAllow
		},
		"header auth mode": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) {
			s.Spec.Auth = &mcpv1.AuthConfig{Mode: "header"}
		},
		"grant disabled":           func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) { g.Spec.Disabled = true },
		"grant monitor mode":       func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) { g.Spec.Mode = mcpv1.GrantModeMonitor },
		"grant for another server": func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) { g.Spec.ServerRef.Name = "other" },
		"grant server UID stale": func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) {
			g.Annotations["mcpruntime.org/server-uid"] = "old"
		},
		"grant tool denied": func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) { g.Spec.ToolRules[0].Decision = "deny" },
		"grant subject changed": func(_ *mcpv1.MCPServer, g *mcpv1.MCPAccessGrant, _ *fakeStore) {
			g.Spec.Subject.HumanID = "33333333-3333-4333-8333-333333333333"
		},
		"session revoked":          func(_ *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, st *fakeStore) { st.session.Binding.Revoked = true },
		"binding other server UID": func(_ *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, st *fakeStore) { st.bindings[0].ServerUID = "uid-x" },
		"binding tool not on server": func(_ *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, st *fakeStore) {
			st.bindings[0].Tools = []string{"ghost"}
		},
		"remote upstream": func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, _ *fakeStore) {
			s.Spec.Gateway = &mcpv1.GatewayConfig{UpstreamURL: "http://example.com:8088"}
		},
	}
	for name, mut := range cases {
		f := newFixture(t, mut)
		w := resolve(f.handler, nil)
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), secret) {
			t.Errorf("%s: want 403 without value, got %d", name, w.Code)
		}
	}
}

func TestResolveRejectsInvalidProviderValuesAndNeverEchoesThem(t *testing.T) {
	for name, value := range map[string][]byte{"invalid utf8": {'a', 0xff, 'b'}, "header injection": []byte("a\r\nX: y"), "empty": nil} {
		f := newFixture(t, nil)
		f.handler.Providers["kubernetes"] = valueProvider{value}
		w := resolve(f.handler, nil)
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "X: y") {
			t.Errorf("%s: got %d %q", name, w.Code, w.Body.String())
		}
	}
}

func TestResolveRejectsDuplicateDeliveryHeader(t *testing.T) {
	f := newFixture(t, func(s *mcpv1.MCPServer, _ *mcpv1.MCPAccessGrant, st *fakeStore) {
		s.Spec.CredentialBindings = []string{"b1", "b2"}
		second := st.bindings[0]
		second.Name = "b2"
		st.bindings = append(st.bindings, second)
		st.sources = append(st.sources, st.sources[0])
	})
	body, _ := json.Marshal(cb.ResolutionRequest{Session: "ns/s1", Tool: "echo", Bindings: []string{"b1", "b2"}})
	r := httptest.NewRequest(http.MethodPost, cb.ResolvePath, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer pod-token")
	w := httptest.NewRecorder()
	f.handler.Resolve(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("duplicate header must be denied, got %d", w.Code)
	}
}

func TestManagementRejectsRawValuesUnknownKindsAndRequiresPrincipalAndRevision(t *testing.T) {
	f := newFixture(t, nil)
	do := func(method, path, body string, authed bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authed {
			r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Subject: hID}))
		}
		w := httptest.NewRecorder()
		f.handler.Management(w, r)
		return w
	}
	if w := do(http.MethodGet, cb.ManagementPath+"/ns/sources", "", false); w.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated management must be denied, got %d", w.Code)
	}
	if w := do(http.MethodGet, cb.ManagementPath+"/ns/secrets", "", true); w.Code != http.StatusNotFound {
		t.Fatalf("unknown kind got %d", w.Code)
	}
	raw := `{"namespace":"ns","name":"src","provider":"kubernetes","resource":"r","key":"k","user_id":"` + hID + `","value":"` + secret + `"}`
	w := do(http.MethodPut, cb.ManagementPath+"/ns/sources/src", raw, true)
	if w.Code != http.StatusBadRequest || f.store.put || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("raw value must be rejected before the store, got %d", w.Code)
	}
	if w := do(http.MethodDelete, cb.ManagementPath+"/ns/sources/src", "", true); w.Code != http.StatusBadRequest || f.store.deleted {
		t.Fatalf("delete without exact revision must be rejected, got %d", w.Code)
	}
	if w := do(http.MethodDelete, cb.ManagementPath+"/ns/sources/src?revision=3", "", true); w.Code != http.StatusNoContent || !f.store.deleted {
		t.Fatalf("delete with revision got %d", w.Code)
	}
}

func TestBindingManagementChecksLiveServerUIDAndTools(t *testing.T) {
	f := newFixture(t, nil)
	put := func(mut func(*cb.Binding)) int {
		b := f.store.bindings[0]
		mut(&b)
		body, _ := json.Marshal(b)
		r := httptest.NewRequest(http.MethodPut, cb.ManagementPath+"/ns/bindings/b1", bytes.NewReader(body))
		r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Subject: hID}))
		w := httptest.NewRecorder()
		f.handler.Management(w, r)
		return w.Code
	}
	if c := put(func(*cb.Binding) {}); c != http.StatusOK {
		t.Fatalf("valid binding got %d", c)
	}
	f.store.put = false
	if c := put(func(b *cb.Binding) { b.ServerUID = "stale" }); c != http.StatusForbidden || f.store.put {
		t.Fatalf("stale server UID got %d", c)
	}
	if c := put(func(b *cb.Binding) { b.Tools = []string{"ghost"} }); c != http.StatusForbidden || f.store.put {
		t.Fatalf("unknown tool got %d", c)
	}
}
