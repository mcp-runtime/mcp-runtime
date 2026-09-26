package runtimeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-runtime-api/internal/platformclient"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	sentinelaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/platformauth"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// adapterTestFixture sets up an in-memory access manager preloaded with a
// server and a grant so the adapter session handler can be exercised
// end-to-end without a real cluster.
type adapterTestFixture struct {
	server         *RuntimeServer
	principal      principal
	scheme         *runtime.Scheme
	setAgentTeam   func(string)
	setAgentStatus func(string)
}

func newAdapterTestFixture(t *testing.T, grants ...mcpv1alpha1.MCPAccessGrant) adapterTestFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	objects := []runtime.Object{
		&mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "mcp-team-acme"},
		},
	}
	for i := range grants {
		objects = append(objects, &grants[i])
	}
	mgr := sentinelaccess.NewManager(dynamicfake.NewSimpleDynamicClient(scheme, objects...), nil)
	agentTeamID := "team-acme"
	agentStatus := "active"
	identityHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/identity/agents/agt_01arz3ndektsv4rrffq69g5fav" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(platformclient.Agent{ID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: agentTeamID, TeamSlug: "acme", Name: "Ops", Status: agentStatus})
	}))
	t.Cleanup(identityHTTP.Close)
	identity := &platformclient.Client{BaseURL: identityHTTP.URL, Token: "test-token", HTTP: identityHTTP.Client()}
	return adapterTestFixture{
		server: &RuntimeServer{accessMgr: mgr, identity: identity},
		principal: principal{
			Subject:   "user-123",
			Email:     "user@example.org",
			Namespace: "mcp-team-acme",
			Role:      roleUser,
			Teams: []platformauth.PrincipalTeam{
				{ID: "team-acme", Namespace: "mcp-team-acme"},
			},
		},
		scheme:         scheme,
		setAgentTeam:   func(teamID string) { agentTeamID = teamID },
		setAgentStatus: func(status string) { agentStatus = status },
	}
}

func adapterRequest(t *testing.T, body adapterSessionRequest) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return httptest.NewRequest(http.MethodPost, "/api/runtime/adapter/sessions", bytes.NewReader(raw))
}

func decodeAdapterResponse(t *testing.T, recorder *httptest.ResponseRecorder) adapterSessionResponse {
	t.Helper()
	var resp adapterSessionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	return resp
}

func TestAdapterSessionIssuesNewSessionFromMatchingGrant(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "g1",
			Namespace:         "mcp-team-acme",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef:     mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:       mcpv1alpha1.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			MaxTrust:      mcpv1alpha1.TrustLevel("high"),
			PolicyVersion: "v3",
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := adapterRequest(t, adapterSessionRequest{
		ServerName:     "demo",
		Namespace:      "mcp-team-acme",
		AgentID:        "agt_01arz3ndektsv4rrffq69g5fav",
		RequestedTrust: "medium",
	})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeAdapterResponse(t, w)
	if got.HumanID != "user-123" || got.AgentID != "agt_01arz3ndektsv4rrffq69g5fav" || got.TeamID != "team-acme" {
		t.Fatalf("identity = %#v, want user-123/agt_01arz3ndektsv4rrffq69g5fav/team-acme", got)
	}
	if got.ConsentedTrust != "medium" {
		t.Fatalf("consentedTrust = %q, want medium (requested, within max=high)", got.ConsentedTrust)
	}
	if got.PolicyVersion != "v3" {
		t.Fatalf("policyVersion = %q, want v3 (from grant)", got.PolicyVersion)
	}
	if got.Reused {
		t.Fatal("reused = true on first call, want false")
	}
	if !strings.HasPrefix(got.Name, "adapter-") {
		t.Fatalf("name = %q, want adapter-<hash> prefix", got.Name)
	}
	if got.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expiresAt = %v, must be in the future", got.ExpiresAt)
	}
}

func TestAdapterSessionLifetimeCannotOutliveGrant(t *testing.T) {
	grantExpiry := time.Now().UTC().Add(30 * time.Minute)
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "debug-window", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
			ExpiresAt: &metav1.Time{Time: grantExpiry},
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", Namespace: "mcp-team-acme", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", RequestedTTL: "2h"})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeAdapterResponse(t, w)
	if got.ExpiresAt.After(grantExpiry) {
		t.Fatalf("session expiry %s extends beyond grant expiry %s", got.ExpiresAt, grantExpiry)
	}
	if got.ExpiresAt.Before(grantExpiry.Add(-time.Second)) {
		t.Fatalf("session expiry %s should be capped at grant expiry %s", got.ExpiresAt, grantExpiry)
	}
}

func TestAdapterSessionRefreshCapsExistingSessionWhenGrantExpiryIsShortened(t *testing.T) {
	grantExpiry := time.Now().UTC().Add(30 * time.Minute)
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "debug-window", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
			ExpiresAt: &metav1.Time{Time: grantExpiry},
		},
	}
	fx := newAdapterTestFixture(t, grant)
	sessionName := adapterSessionName("user-123", "agt_01arz3ndektsv4rrffq69g5fav", "team-acme", "demo")
	_, err := fx.server.accessMgr.ApplySession(t.Context(), &sentinelaccess.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "mcp-team-acme"},
		Spec: sentinelaccess.MCPAgentSessionSpec{
			ServerRef:      sentinelaccess.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:        sentinelaccess.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			ConsentedTrust: sentinelaccess.TrustLow,
			PolicyVersion:  "v1",
			ExpiresAt:      &metav1.Time{Time: time.Now().UTC().Add(time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("seed longer-lived adapter session: %v", err)
	}
	req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", Namespace: "mcp-team-acme", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", RequestedTTL: "2h"})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeAdapterResponse(t, w)
	if got.Reused {
		t.Fatal("reused = true for a session that outlived the shortened grant")
	}
	if got.ExpiresAt.After(grantExpiry) {
		t.Fatalf("session expiry %s extends beyond grant expiry %s", got.ExpiresAt, grantExpiry)
	}
}

func TestAdapterSessionCannotRefreshExpiredGrant(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "expired-debug", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-acme"},
			ExpiresAt: &metav1.Time{Time: time.Now().Add(-time.Second)},
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", Namespace: "mcp-team-acme", AgentID: "agt_01arz3ndektsv4rrffq69g5fav"})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "no enabled MCPAccessGrant") {
		t.Fatalf("status/body = %d/%s, want expired grant to block session refresh", w.Code, w.Body.String())
	}
}

func TestAdapterSessionIssuesCrossTeamSessionFromGrantedTeam(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "globex-to-acme", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-globex"},
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
		},
	}
	fx := newAdapterTestFixture(t, grant)
	fx.setAgentTeam("team-globex")
	fx.principal.Namespace = "mcp-team-globex"
	fx.principal.Teams = []platformauth.PrincipalTeam{
		{ID: "team-globex", Namespace: "mcp-team-globex"},
	}
	req := adapterRequest(t, adapterSessionRequest{
		ServerName: "demo",
		Namespace:  "mcp-team-acme",
		AgentID:    "agt_01arz3ndektsv4rrffq69g5fav",
	})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeAdapterResponse(t, w)
	if got.TeamID != "team-globex" {
		t.Fatalf("teamID = %q, want team-globex", got.TeamID)
	}
}

func TestAdapterSessionRejectsGrantForUnheldTeam(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "globex-to-acme", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-globex"},
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := adapterRequest(t, adapterSessionRequest{
		ServerName: "demo",
		Namespace:  "mcp-team-acme",
		AgentID:    "agt_01arz3ndektsv4rrffq69g5fav",
	})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no enabled MCPAccessGrant") {
		t.Fatalf("body = %q, want 'no enabled MCPAccessGrant'", w.Body.String())
	}
}

func TestAdapterSessionRejectsWhenNoGrantMatches(t *testing.T) {
	fx := newAdapterTestFixture(t) // no grants
	req := adapterRequest(t, adapterSessionRequest{
		ServerName: "demo",
		Namespace:  "mcp-team-acme",
		AgentID:    "agt_01arz3ndektsv4rrffq69g5fav",
	})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no enabled MCPAccessGrant") {
		t.Fatalf("body = %q, want 'no enabled MCPAccessGrant'", w.Body.String())
	}
}

func TestAdapterSessionTrustCappedAtGrantMaxTrust(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "g1", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{TeamID: "team-acme"}, // team wildcard for humanID/agentID
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := adapterRequest(t, adapterSessionRequest{
		ServerName:     "demo",
		Namespace:      "mcp-team-acme",
		AgentID:        "agt_01arz3ndektsv4rrffq69g5fav",
		RequestedTrust: "high",
	})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decodeAdapterResponse(t, w)
	if got.ConsentedTrust != "low" {
		t.Fatalf("consentedTrust = %q, want low (capped at grant.MaxTrust)", got.ConsentedTrust)
	}
}

func TestAdapterSessionPicksHighestTrustWithDeterministicTiebreak(t *testing.T) {
	older := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "g-older",
			Namespace:         "mcp-team-acme",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{TeamID: "team-acme"},
			MaxTrust:  mcpv1alpha1.TrustLevel("high"),
		},
	}
	newer := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "g-newer",
			Namespace:         "mcp-team-acme",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{TeamID: "team-acme"},
			MaxTrust:  mcpv1alpha1.TrustLevel("high"),
		},
	}
	low := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "g-low", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{TeamID: "team-acme"},
			MaxTrust:  mcpv1alpha1.TrustLevel("low"),
		},
	}
	fx := newAdapterTestFixture(t, low, newer, older)
	g, teamID, err := fx.server.Access().selectAdapterGrant(
		t.Context(),
		"mcp-team-acme", "demo",
		"user-123", "agt_01arz3ndektsv4rrffq69g5fav", []string{"team-acme"}, "team-acme", false,
	)
	if err != nil {
		t.Fatalf("selectAdapterGrant: %v", err)
	}
	if g.Name != "g-older" {
		t.Fatalf("selected = %q, want g-older (highest trust, oldest first for tiebreak)", g.Name)
	}
	if teamID != "team-acme" {
		t.Fatalf("teamID = %q, want team-acme", teamID)
	}
}

func TestMatchingAdapterGrantRequiresCallerTeamEvenForAdmin(t *testing.T) {
	grant := sentinelaccess.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-b"}
	if _, ok := matchingAdapterGrantTeamID(grant, "user-123", "agt_01arz3ndektsv4rrffq69g5fav", []string{"team-a"}, "team-a", true); ok {
		t.Fatal("admin without membership in the subject team matched a cross-team grant")
	}
	if team, ok := matchingAdapterGrantTeamID(grant, "user-123", "agt_01arz3ndektsv4rrffq69g5fav", []string{"team-b"}, "team-a", false); !ok || team != "team-b" {
		t.Fatalf("matchingAdapterGrantTeamID() = (%q, %t), want (team-b, true)", team, ok)
	}
}

func TestSessionGrantLinkRequiresExactServerAndSubject(t *testing.T) {
	grant := mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "cross-team", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-b"},
			MaxTrust:  mcpv1alpha1.TrustLevel(sentinelaccess.TrustHigh),
		},
	}
	fx := newAdapterTestFixture(t, grant)
	req := accessSessionRequest{
		Namespace: "mcp-team-acme", GrantName: "cross-team",
		ServerRef: sentinelaccess.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
		Subject:   sentinelaccess.SubjectRef{HumanID: "user-123", AgentID: "agt_01arz3ndektsv4rrffq69g5fav", TeamID: "team-b"},
	}
	annotations, linked, err := fx.server.Access().sessionGrantLink(t.Context(), req)
	if err != nil || linked == nil || annotations[adapterGrantNameAnnotation] != grant.Name {
		t.Fatalf("sessionGrantLink() = (%v, %v, %v), want linked grant annotation", annotations, linked, err)
	}
	req.Subject.TeamID = "team-c"
	if _, _, err := fx.server.Access().sessionGrantLink(t.Context(), req); err == nil {
		t.Fatal("sessionGrantLink accepted a subject team that differs from the grant")
	}
}

func TestGrantRevokeSessionsOnlyTouchesLinkedSessionsAndIsRetrySafe(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	grant := &mcpv1alpha1.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "cross-team", Namespace: "mcp-team-acme"},
		Spec: mcpv1alpha1.MCPAccessGrantSpec{
			ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "mcp-team-acme"},
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-b", AgentID: "agent-b", TeamID: "team-b"},
		},
	}
	linked := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "linked", Namespace: "mcp-team-acme", Annotations: map[string]string{
			adapterGrantNameAnnotation: "cross-team", adapterGrantNamespaceAnnotation: "mcp-team-acme",
		}},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{Subject: grant.Spec.Subject},
	}
	unlinked := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "unlinked", Namespace: "mcp-team-acme"},
		Spec:       mcpv1alpha1.MCPAgentSessionSpec{Subject: grant.Spec.Subject},
	}
	accessMgr := sentinelaccess.NewManager(dynamicfake.NewSimpleDynamicClient(scheme,
		&mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "mcp-team-acme"}, Spec: mcpv1alpha1.MCPServerSpec{TeamID: "team-a"}},
		grant, linked, unlinked,
	), nil)
	audit := &fakeAuditWriter{}
	runtimeServer := &RuntimeServer{accessMgr: accessMgr}
	runtimeServer.SetAuditWriter(audit)
	adminCtx := withPrincipal(httptest.NewRequest(http.MethodPost, "/", nil).Context(), principal{Role: roleAdmin, Subject: "admin-1"})
	revoke := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/grants/mcp-team-acme/cross-team/revoke-sessions", nil).WithContext(adminCtx)
		recorder := httptest.NewRecorder()
		runtimeServer.Access().HandleGrantItemPath(recorder, req)
		return recorder
	}
	if rec := revoke(); rec.Code != http.StatusOK {
		t.Fatalf("first revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := revoke(); rec.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	linkedAfter, err := accessMgr.GetSession(t.Context(), "linked", "mcp-team-acme")
	if err != nil || !linkedAfter.Spec.Revoked {
		t.Fatalf("linked session after revoke = %#v, err=%v; want revoked", linkedAfter, err)
	}
	unlinkedAfter, err := accessMgr.GetSession(t.Context(), "unlinked", "mcp-team-acme")
	if err != nil || unlinkedAfter.Spec.Revoked {
		t.Fatalf("unlinked session after revoke = %#v, err=%v; want unchanged", unlinkedAfter, err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "grant.session.revoked" {
		t.Fatalf("audit events = %#v, want exactly one successful session revocation", audit.events)
	}
}

func TestAdapterSessionRejectsUnknownOrInactiveAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		agentID string
		status  string
	}{
		{name: "unknown agent", agentID: "unknown-agent", status: "active"},
		{name: "inactive agent", agentID: "agt_01arz3ndektsv4rrffq69g5fav", status: "inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newAdapterTestFixture(t)
			fx.setAgentStatus(tc.status)
			req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", Namespace: "mcp-team-acme", AgentID: tc.agentID})
			req = req.WithContext(withPrincipal(req.Context(), fx.principal))
			w := httptest.NewRecorder()
			fx.server.Access().HandleAdapterSession(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want 403", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "unknown or inactive") {
				t.Fatalf("body = %q, want fail-closed agent error", w.Body.String())
			}
		})
	}
}

func TestAdapterSessionRequiresServerName(t *testing.T) {
	fx := newAdapterTestFixture(t)
	req := adapterRequest(t, adapterSessionRequest{Namespace: "mcp-team-acme", AgentID: "agt_01arz3ndektsv4rrffq69g5fav"})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAdapterSessionRequiresResolvedNamespace(t *testing.T) {
	fx := newAdapterTestFixture(t)
	fx.principal.Namespace = ""
	req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", AgentID: "agt_01arz3ndektsv4rrffq69g5fav"})
	req = req.WithContext(withPrincipal(req.Context(), fx.principal))
	w := httptest.NewRecorder()
	fx.server.Access().HandleAdapterSession(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "namespace is required") {
		t.Fatalf("body = %q, want namespace validation error", w.Body.String())
	}
}

func TestAdapterSessionRejectsTTLBeyondHardCap(t *testing.T) {
	d, err := parseAdapterTTL("48h")
	if err != nil {
		t.Fatalf("parseAdapterTTL: %v", err)
	}
	if d != adapterSessionMaxTTL {
		t.Fatalf("got %v, want capped at %v", d, adapterSessionMaxTTL)
	}
}

func TestAdapterSessionDeterministicName(t *testing.T) {
	n1 := adapterSessionName("h1", "a1", "t1", "srv")
	n2 := adapterSessionName("h1", "a1", "t1", "srv")
	if n1 != n2 {
		t.Fatalf("name n1 = %q, n2 = %q, want equal for identical inputs", n1, n2)
	}
	if adapterSessionName("h1", "a1", "t1", "srv") == adapterSessionName("h2", "a1", "t1", "srv") {
		t.Fatal("name should differ when humanID differs")
	}
	if !strings.HasPrefix(n1, "adapter-") {
		t.Fatalf("name = %q, want adapter- prefix", n1)
	}
}

func TestAdapterSessionReusableRejectsExpiredOrRevoked(t *testing.T) {
	now := time.Now()
	ok := &sentinelaccess.MCPAgentSession{
		Spec: sentinelaccess.MCPAgentSessionSpec{
			PolicyVersion:  "v1",
			ConsentedTrust: sentinelaccess.TrustLow,
			ExpiresAt:      &metav1.Time{Time: now.Add(time.Hour)},
		},
	}
	revoked := *ok
	revoked.Spec.Revoked = true
	soon := *ok
	soon.Spec.ExpiresAt = &metav1.Time{Time: now.Add(5 * time.Second)} // < refresh buffer
	mismatchPolicy := *ok
	mismatchPolicy.Spec.PolicyVersion = "v2"

	if !adapterSessionReusable(ok, "v1", sentinelaccess.TrustLow) {
		t.Fatal("happy path should be reusable")
	}
	if adapterSessionReusable(&revoked, "v1", sentinelaccess.TrustLow) {
		t.Fatal("revoked session must not be reused")
	}
	if adapterSessionReusable(&soon, "v1", sentinelaccess.TrustLow) {
		t.Fatal("session inside refresh buffer must not be reused")
	}
	if adapterSessionReusable(&mismatchPolicy, "v1", sentinelaccess.TrustLow) {
		t.Fatal("policy-version mismatch must not be reused")
	}
}
