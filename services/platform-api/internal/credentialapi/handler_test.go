package credentialapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	session    sessionstate.Session
	bindings   []cb.Binding
	sources    []cb.Source
	deleted    bool
	afterFetch func()
	put        bool
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

// UseCredentials mirrors the real store contract: authorize, fetch with no lock,
// then authorize again after state is re-read (afterFetch simulates a change
// that happens during the provider read).
func (s *fakeStore) UseCredentials(_ context.Context, _ sessionstate.Server, _ cb.ResolutionRequest, authorize func(sessionstate.Session, []cb.Binding, []cb.Source) error, fetch func(sessionstate.Session, []cb.Binding, []cb.Source) (cb.Delivery, error)) (cb.Delivery, error) {
	if err := authorize(s.session, s.bindings, s.sources); err != nil {
		return cb.Delivery{}, err
	}
	out, err := fetch(s.session, s.bindings, s.sources)
	if err != nil {
		return cb.Delivery{}, err
	}
	if s.afterFetch != nil {
		s.afterFetch()
	}
	if err := authorize(s.session, s.bindings, s.sources); err != nil {
		return cb.Delivery{}, err
	}
	return out, nil
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
				return sessionstate.Server{}, fmt.Errorf("%w: bad token", cb.ErrTokenRejected)
			}
			return sessionstate.Server{Namespace: "ns", Name: "srv", UID: "uid-1"}, nil
		},
		Providers: map[string]cb.Provider{"kubernetes": valueProvider{[]byte(secret)}},
		Limits:    NewLimits(),
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

type countingProvider struct {
	calls int
	err   error
}

func (p *countingProvider) Read(context.Context, cb.Source) (cb.Value, error) {
	p.calls++
	if p.err != nil {
		return cb.Value{}, p.err
	}
	return cb.Value{Bytes: []byte(secret)}, nil
}

func TestResolveDeniesWhenGrantRevokedDuringProviderRead(t *testing.T) {
	f := newFixture(t, nil)
	f.store.afterFetch = func() {
		g := &mcpv1.MCPAccessGrant{}
		if err := f.handler.Kube.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "g1"}, g); err != nil {
			t.Fatal(err)
		}
		g.Spec.Disabled = true
		if err := f.handler.Kube.Update(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
	w := resolve(f.handler, nil)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("revocation during provider read must deny without value, got %d", w.Code)
	}
}

func TestResolveRateLimitsPerServerAndReturns429(t *testing.T) {
	f := newFixture(t, nil)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	var last int
	for i := 0; i < int(serverBurst)+1; i++ {
		last = resolve(f.handler, nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once the per-server burst is spent, got %d", last)
	}
	now = now.Add(2 * time.Second)
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("bucket must refill, got %d", w.Code)
	}
}

func countingAuth(f *fixture, reviews *int) {
	f.handler.Authenticate = func(_ context.Context, token string) (sessionstate.Server, error) {
		*reviews++
		if token != "pod-token" {
			return sessionstate.Server{}, fmt.Errorf("%w: bad token", cb.ErrTokenRejected)
		}
		return sessionstate.Server{Namespace: "ns", Name: "srv", UID: "uid-1"}, nil
	}
}

func withToken(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func TestInvalidTokenFloodNeverDeniesCachedValidCaller(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	f.handler.Limits.server.burst, f.handler.Limits.server.rate = 100000, 100000
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("valid caller: %d", w.Code)
	}
	for i := 0; i < 500; i++ {
		resolve(f.handler, withToken("junk-"+strconv.Itoa(i)))
		if w := resolve(f.handler, nil); w.Code != http.StatusOK {
			t.Fatalf("flood of invalid tokens from the same address denied a valid caller at %d: %d", i, w.Code)
		}
	}
}

func TestInvalidTokensAreThrottledAndTokenReviewIsBounded(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	limited := 0
	for i := 0; i < 500; i++ {
		if resolve(f.handler, withToken("junk-"+strconv.Itoa(i))).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 || reviews > int(sourceBurst) {
		t.Fatalf("cold invalid tokens must be throttled: limited=%d reviews=%d", limited, reviews)
	}
	// Repeating the same tokens never reaches TokenReview again.
	before := reviews
	for i := 0; i < 50; i++ {
		resolve(f.handler, withToken("junk-0"))
		resolve(f.handler, nil)
	}
	if reviews > before+1 {
		t.Fatalf("cached tokens hit TokenReview: %d -> %d", before, reviews)
	}
	if w := resolve(f.handler, withToken("junk-0")); w.Code != http.StatusForbidden {
		t.Fatalf("cached invalid token must be 403, got %d", w.Code)
	}
}

func TestAuthCacheExpiresAndHoldsOnlyHashes(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		resolve(f.handler, nil)
	}
	if reviews != 1 {
		t.Fatalf("valid token reviewed %d times", reviews)
	}
	now = now.Add(authCacheTTL + time.Second)
	resolve(f.handler, nil)
	if reviews != 2 {
		t.Fatalf("auth cache must expire, reviews=%d", reviews)
	}
	for k := range f.handler.Limits.authOK {
		if strings.Contains(k, "pod-token") {
			t.Fatal("token value used as cache key")
		}
	}
}

func TestProviderFailureNegativeCacheExpiresAndHoldsNoSecret(t *testing.T) {
	f := newFixture(t, nil)
	p := &countingProvider{err: errors.New("down")}
	f.handler.Providers["kubernetes"] = p
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	f.handler.Limits.source.burst, f.handler.Limits.source.rate = 1000, 1000
	f.handler.Limits.server.burst, f.handler.Limits.server.rate = 1000, 1000
	for i := 0; i < 3; i++ {
		if w := resolve(f.handler, nil); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d", w.Code)
		}
	}
	if p.calls != 1 {
		t.Fatalf("failure must be cached briefly, provider called %d times", p.calls)
	}
	now = now.Add(failureTTL + time.Second)
	resolve(f.handler, nil)
	if p.calls != 2 {
		t.Fatalf("negative cache must expire, provider called %d times", p.calls)
	}
	p.err = nil
	now = now.Add(failureTTL + time.Second)
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("recovered provider must be served, got %d", w.Code)
	}
	for k := range f.handler.Limits.failures {
		if strings.Contains(k, secret) {
			t.Fatal("secret in cache key")
		}
	}
}

func TestNegativeCacheAndLimitersAreBounded(t *testing.T) {
	l := NewLimits()
	for i := 0; i < failureMaxItems*3; i++ {
		l.RecordFailure(strconv.Itoa(i))
		l.AllowColdReview(strconv.Itoa(i))
		l.RecordAuth(strconv.Itoa(i), sessionstate.Server{}, false)
	}
	if len(l.failures) > failureMaxItems || len(l.source.items) > limiterMaxKeys || len(l.authBad)+len(l.authOK) > authCacheMax {
		t.Fatalf("unbounded: %d failures %d sources", len(l.failures), len(l.source.items))
	}
}

func TestTransientTokenReviewErrorIsNotCachedAndFailsOnlyThatRequest(t *testing.T) {
	f := newFixture(t, nil)
	calls := 0
	f.handler.Authenticate = func(context.Context, string) (sessionstate.Server, error) {
		calls++
		if calls == 1 {
			return sessionstate.Server{}, errors.New("apiserver timeout")
		}
		return sessionstate.Server{Namespace: "ns", Name: "srv", UID: "uid-1"}, nil
	}
	if w := resolve(f.handler, nil); w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("transient failure must fail closed with 503, got %d", w.Code)
	}
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("a valid caller must succeed right after an API blip, got %d", w.Code)
	}
}

func TestDefiniteRejectionIsCached(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	for i := 0; i < 5; i++ {
		if w := resolve(f.handler, withToken("junk")); w.Code != http.StatusForbidden {
			t.Fatalf("got %d", w.Code)
		}
	}
	if reviews != 1 {
		t.Fatalf("definite rejection must be cached, reviews=%d", reviews)
	}
}

func exhaustColdBudget(f fixture, reviews *int) {
	for i := 0; i < int(reviewBurst)*3; i++ {
		resolve(f.handler, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer cold-"+strconv.Itoa(i))
			r.RemoteAddr = "198.51.100." + strconv.Itoa(i%250+1) + ":1"
		})
	}
}

func TestExpiredRecentlyValidTokenRenewsWhileColdBudgetIsExhausted(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	f.handler.Limits.server.burst, f.handler.Limits.server.rate = 100000, 100000
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("initial: %d", w.Code)
	}
	exhaustColdBudget(f, &reviews)
	if w := resolve(f.handler, withToken("cold-new")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("unknown token must still be throttled, got %d", w.Code)
	}
	now = now.Add(authCacheTTL + 5*time.Second) // expired, inside the renewal window
	exhaustColdBudget(f, &reviews)
	if w := resolve(f.handler, nil); w.Code != http.StatusOK {
		t.Fatalf("recently valid token must renew despite exhausted cold budget, got %d", w.Code)
	}
	// A different token cannot ride on that renewal.
	if w := resolve(f.handler, withToken("pod-token-2")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("other token renewed through another's entry: %d", w.Code)
	}
	// Past the renewal window the token is cold again.
	now = now.Add(authRenewWindow + time.Second)
	exhaustColdBudget(f, &reviews)
	if w := resolve(f.handler, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("token past the renewal window is cold, got %d", w.Code)
	}
}

func TestRenewalIsRateLimitedPerToken(t *testing.T) {
	f := newFixture(t, nil)
	reviews := 0
	countingAuth(&f, &reviews)
	now := time.Unix(1000, 0)
	f.handler.Limits.Now = func() time.Time { return now }
	resolve(f.handler, nil)
	reviews = 0
	for i := 0; i < 50; i++ {
		e := f.handler.Limits.authOK[tokenHash("pod-token")]
		e.until = now.Add(-time.Millisecond)
		f.handler.Limits.authOK[tokenHash("pod-token")] = e
		resolve(f.handler, nil)
	}
	if reviews > int(renewBurst)+1 {
		t.Fatalf("renewal reviews unbounded: %d", reviews)
	}
}
