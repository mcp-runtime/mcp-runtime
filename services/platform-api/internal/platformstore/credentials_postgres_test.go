package platformstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"mcp-platform-api/internal/credentialapi"
	mcpv1 "mcp-runtime/api/v1alpha1"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPostgresCredentialBrokerAPIIsolationRotationRevocationAndAudit(t *testing.T) {
	s := lifecyclePostgresStore(t)
	ctx := t.Context()
	admin, err := s.CreatePasswordUser(ctx, "credential-admin@example.test", "fixture-test-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreatePasswordUser(ctx, "credential-owner@example.test", "fixture-test-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "credential-team", "Credential team", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	human, _, err := s.CreateTeamUser(ctx, team.Slug, "credential-member@example.test", "fixture-test-password", "member")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.CreateAgent(ctx, team.Slug, "Credential fixture", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := &mcpv1.MCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: team.Namespace, Name: "upstream", UID: types.UID(uuid.NewString())}, Spec: mcpv1.MCPServerSpec{TeamID: team.ID, CredentialBindings: []string{"token"}, Policy: &mcpv1.PolicyConfig{Mode: mcpv1.PolicyModeAllowList, DefaultDecision: mcpv1.PolicyDecisionDeny, PolicyVersion: "v1"}, Tools: []mcpv1.ToolConfig{{Name: "search", RequiredTrust: mcpv1.TrustLevelLow, SideEffect: mcpv1.ToolSideEffectRead}}}}
	grant := &mcpv1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: team.Namespace, Name: "access", Annotations: map[string]string{"mcpruntime.org/server-uid": string(server.UID)}}, Spec: mcpv1.MCPAccessGrantSpec{ServerRef: mcpv1.ServerReference{Name: server.Name}, Subject: mcpv1.SubjectRef{HumanID: human.ID, AgentID: agent.ID, TeamID: team.ID}, MaxTrust: mcpv1.TrustLevelLow, AllowedSideEffects: []mcpv1.ToolSideEffect{mcpv1.ToolSideEffectRead}, PolicyVersion: "v1", ToolRules: []mcpv1.ToolRule{{Name: "search", Decision: mcpv1.PolicyDecisionAllow, RequiredTrust: mcpv1.TrustLevelLow}}}}
	session := sessionstate.Session{Namespace: team.Namespace, ServerNamespace: team.Namespace, ServerName: server.Name, ServerUID: string(server.UID), GrantNamespace: team.Namespace, GrantName: grant.Name, AuthorityTeamID: team.ID, Binding: policy.Binding{Name: "session", Namespace: policy.Namespace(team.Namespace), HumanID: policy.HumanID(human.ID), AgentID: policy.AgentID(agent.ID), TeamID: policy.TeamID(team.ID), ConsentedTrust: "low", PolicyVersion: "v1", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	session, err = s.PutSession(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	source := cb.Source{Namespace: team.Namespace, Name: "provider", Provider: "kubernetes", Resource: "upstream-token", Key: "token", TeamID: team.ID}
	binding := cb.Binding{Namespace: team.Namespace, Name: "token", Source: source.Name, Server: server.Name, ServerUID: string(server.UID), Tools: []string{"search"}, HumanID: human.ID, AgentID: agent.ID, TeamID: team.ID, Header: cb.HeaderPrefix + "Token"}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: team.Namespace, Name: source.Resource}, Data: map[string][]byte{"token": []byte("fixture-upstream-value-one")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, grant, secret).Build()
	identity := sessionstate.Server{Namespace: server.Namespace, Name: server.Name, UID: string(server.UID)}
	var authentications atomic.Int64
	h := credentialapi.Handler{Store: s, Kube: kube, Providers: credentialapi.Providers(kube), Authenticate: func(_ context.Context, token string) (sessionstate.Server, error) {
		authentications.Add(1)
		if token != "fixture-pod-token" {
			return sessionstate.Server{}, cb.ErrDenied
		}
		return identity, nil
	}}
	// Exercise real signed human authentication for management, with live
	// directory refresh rather than synthetic namespace/role headers.
	auth := platformauth.Authenticator{Secret: s.jwtSecret, Audience: platformauth.AudiencePlatform, Principals: s}
	management := auth.Middleware(http.HandlerFunc(h.Management))
	call := func(user User, method, path string, value any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if value != nil {
			raw, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		token, e := s.CreateAccessToken(user, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		management.ServeHTTP(w, r)
		return w
	}
	path := cb.ManagementPath + "/" + team.Namespace
	w := call(human, http.MethodPut, path+"/sources/provider", source)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member registered a provider: %d", w.Code)
	}
	w = call(admin, http.MethodPut, path+"/sources/provider", source)
	if w.Code != 200 {
		t.Fatalf("admin source: %d %s", w.Code, w.Body.String())
	}
	if json.Unmarshal(w.Body.Bytes(), &source) != nil || source.Revision != 1 {
		t.Fatal("source revision not returned")
	}
	w = call(human, http.MethodPut, path+"/bindings/token", binding)
	if w.Code != 403 {
		t.Fatalf("member managed binding: %d", w.Code)
	}
	w = call(owner, http.MethodPut, path+"/bindings/token", binding)
	if w.Code != 200 {
		t.Fatalf("owner binding: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &binding)
	// Stale update/delete and deletion of a referenced source fail closed.
	stale := binding
	stale.Revision = 0
	if w = call(owner, http.MethodPut, path+"/bindings/token", stale); w.Code != 409 {
		t.Fatalf("stale update: %d", w.Code)
	}
	if w = call(admin, http.MethodDelete, path+"/sources/provider?revision=1", nil); w.Code != 409 {
		t.Fatalf("bound source deletion: %d", w.Code)
	}
	foreign := path
	path = cb.ManagementPath + "/mcp-team-foreign"
	if w = call(owner, http.MethodGet, path+"/sources", nil); w.Code != 403 {
		t.Fatalf("foreign namespace read: %d", w.Code)
	}
	path = foreign
	for _, kind := range []string{"sources", "bindings"} {
		w = call(human, http.MethodGet, path+"/"+kind, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-upstream-value") {
			t.Fatalf("metadata leak/list failure: %d", w.Code)
		}
	}
	// Unknown raw-value fields are refused without echoing their content.
	r := httptest.NewRequest(http.MethodPut, path+"/sources/provider", strings.NewReader(`{"secret_value":"fixture-upstream-value-forbidden"}`))
	r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Role: "admin", Subject: admin.ID}))
	w = httptest.NewRecorder()
	h.Management(w, r)
	if w.Code != 400 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatal("raw management value accepted or echoed")
	}
	request := cb.ResolutionRequest{Session: session.ID, Tool: "search", Bindings: []string{"token"}}
	resolve := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(request)
		r := httptest.NewRequest(http.MethodPost, cb.ResolvePath, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Human-Id", human.ID)
		r.Header.Set("X-Team-Id", team.ID)
		w := httptest.NewRecorder()
		h.Resolve(w, r)
		return w
	}
	if w = resolve("forged-human-token"); w.Code != 403 {
		t.Fatal("caller identity headers selected a credential")
	}
	if w = resolve("fixture-pod-token"); w.Code != 200 || !strings.Contains(w.Body.String(), "fixture-upstream-value-one") {
		t.Fatalf("authorized read: %d %s", w.Code, w.Body.String())
	}
	identity.Namespace = "foreign"
	if w = resolve("fixture-pod-token"); w.Code != 403 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatal("verified workload namespace bypassed session binding")
	}
	identity.Namespace = server.Namespace
	// Provider data changes are observed on the next call without redeployment.
	if err = kube.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["token"] = []byte("fixture-upstream-value-two")
	if err = kube.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if w = resolve("fixture-pod-token"); w.Code != 200 || !strings.Contains(w.Body.String(), "fixture-upstream-value-two") {
		t.Fatal("rotation required redeployment")
	}
	saved := h.Providers
	h.Providers = nil
	if w = resolve("fixture-pod-token"); w.Code != 503 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatal("missing provider did not fail redacted")
	}
	h.Providers = saved
	// Revoke while the backing provider is reading. The post-I/O canonical
	// grant check must prevent that already-read value from escaping.
	originalProvider := h.Providers["kubernetes"]
	h.Providers["kubernetes"] = credentialTestProvider(func(ctx context.Context, source cb.Source) (cb.Value, error) {
		value, err := originalProvider.Read(ctx, source)
		if err != nil {
			return value, err
		}
		grant.Spec.Disabled = true
		if err = kube.Update(ctx, grant); err != nil {
			t.Fatal(err)
		}
		return value, nil
	})
	if w = resolve("fixture-pod-token"); w.Code != 403 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatal("grant revoked during provider I/O released a value")
	}
	h.Providers["kubernetes"] = originalProvider
	grant.Spec.Disabled = false
	_ = kube.Update(ctx, grant)
	// An enforced grant revoked during provider reads cannot release a value.
	grant.Spec.Disabled = true
	if err = kube.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if w = resolve("fixture-pod-token"); w.Code != 403 {
		t.Fatalf("disabled grant: %d", w.Code)
	}
	grant.Spec.Disabled = false
	_ = kube.Update(ctx, grant)
	// The provider read must not hold the platform-wide session lock: revoke the
	// session from inside the read (it would block until the 3s deadline if the
	// lock were held) and require the already-read value to be withheld.
	h.Providers["kubernetes"] = credentialTestProvider(func(ctx context.Context, source cb.Source) (cb.Value, error) {
		value, err := originalProvider.Read(ctx, source)
		if err != nil {
			return value, err
		}
		revokeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err = s.SetSessionRevoked(revokeCtx, session.Namespace, string(session.Binding.Name), true, "fixture_revoked_during_read"); err != nil {
			t.Errorf("session revoke blocked by provider read lock: %v", err)
		}
		return value, nil
	})
	if w = resolve("fixture-pod-token"); w.Code != 403 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatalf("session revoked during provider read released a value: %d", w.Code)
	}
	h.Providers["kubernetes"] = originalProvider
	if err = s.SetSessionRevoked(ctx, session.Namespace, string(session.Binding.Name), false, "fixture_restored"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetSessionRevoked(ctx, session.Namespace, string(session.Binding.Name), true, "fixture_revoked"); err != nil {
		t.Fatal(err)
	}
	if w = resolve("fixture-pod-token"); w.Code != 403 || strings.Contains(w.Body.String(), "fixture-upstream") {
		t.Fatal("revoked session resolved credential")
	}
	var audits, denials, leaks int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE status='denied'),COUNT(*) FILTER(WHERE message LIKE '%fixture-upstream-value%' OR resource LIKE '%fixture-upstream-value%') FROM audit_logs WHERE action LIKE 'credential_%'`).Scan(&audits, &denials, &leaks); err != nil {
		t.Fatal(err)
	}
	if audits < 6 || denials < 3 || leaks != 0 {
		t.Fatalf("audit counts %d/%d, leaks %d", audits, denials, leaks)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_metadata WHERE data::text LIKE '%fixture-upstream-value%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("provider value persisted")
	}
	// Audit failure rolls back metadata writes and prevents value delivery.
	if _, err = s.db.ExecContext(ctx, `ALTER TABLE audit_logs RENAME TO audit_logs_unavailable`); err != nil {
		t.Fatal(err)
	}
	source.Revision = 1
	source.Disabled = true
	if _, err = s.PutCredential(ctx, platformauth.Principal{Role: "admin", Subject: admin.ID}, &source, nil); err == nil {
		t.Fatal("metadata committed without audit")
	}
	if _, err = s.db.ExecContext(ctx, `ALTER TABLE audit_logs_unavailable RENAME TO audit_logs`); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListCredentials(ctx, platformauth.Principal{Role: "admin", Subject: admin.ID}, team.Namespace, "sources")
	if err != nil {
		t.Fatal(err)
	}
	var unchanged cb.Source
	_ = json.Unmarshal(items[0], &unchanged)
	if unchanged.Disabled || unchanged.Revision != 1 {
		t.Fatal("audit failure changed source")
	}
	if authentications.Load() < 6 {
		t.Fatal("runtime authentication not performed per request")
	}
	if err = s.DeleteCredential(ctx, platformauth.Principal{Subject: owner.ID}, team.Namespace, "bindings", "token", binding.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCredential(ctx, platformauth.Principal{Subject: admin.ID}, team.Namespace, "sources", "provider", source.Revision); err != nil && !errors.Is(err, cb.ErrConflict) {
		t.Fatal(err)
	}
}

type credentialTestProvider func(context.Context, cb.Source) (cb.Value, error)

func (f credentialTestProvider) Read(ctx context.Context, source cb.Source) (cb.Value, error) {
	return f(ctx, source)
}
