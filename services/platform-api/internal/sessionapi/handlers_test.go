package sessionapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

type fakeSessionStore struct {
	Store
	server     sessionstate.Server
	item       sessionstate.Session
	changesErr error
	calls      int
}

func (s *fakeSessionStore) SessionSnapshot(_ context.Context, server sessionstate.Server) (sessionstate.Snapshot, error) {
	s.server = server
	s.calls++
	return sessionstate.Snapshot{Epoch: "current", Sequence: 1, Sessions: []sessionstate.Session{s.item}}, nil
}
func (s *fakeSessionStore) SessionChanges(_ context.Context, server sessionstate.Server, _ string, _ int64) (sessionstate.Changes, error) {
	s.server = server
	s.calls++
	return sessionstate.Changes{Epoch: "current", Sequence: 1, Changes: []sessionstate.Change{}}, s.changesErr
}
func (s *fakeSessionStore) GetSession(_ context.Context, namespace, name string) (sessionstate.Session, bool, error) {
	s.calls++
	return s.item, namespace == s.item.Namespace && name == string(s.item.Binding.Name), nil
}

func gatewayTestHandler(store *fakeSessionStore) http.Handler {
	return (Handler{Store: store, Token: "shared-control-token", Authenticate: func(_ context.Context, token string) (sessionstate.Server, error) {
		if token != "gateway-token" {
			return sessionstate.Server{}, errors.New("wrong audience")
		}
		return sessionstate.Server{Namespace: "tenant", Name: "server", UID: "server-uid"}, nil
	}}).Gateway()
}

func TestGatewayAudienceNeverAcceptsControlPlaneCredential(t *testing.T) {
	store := &fakeSessionStore{}
	h := gatewayTestHandler(store)
	for _, token := range []string{"", "shared-control-token", "wrong-token"} {
		req := httptest.NewRequest("GET", sessionstate.GatewayPath, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 && rec.Code != 403 {
			t.Fatalf("token %q status=%d", token, rec.Code)
		}
	}
	if store.calls != 0 {
		t.Fatal("unauthenticated request reached storage")
	}
}

func TestGatewayScopeIgnoresCallerServerFields(t *testing.T) {
	store := &fakeSessionStore{}
	h := gatewayTestHandler(store)
	req := httptest.NewRequest("GET", sessionstate.GatewayPath+"?server=other&namespace=other&server_uid=other", nil)
	req.Header.Set("Authorization", "Bearer gateway-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || store.server.UID != "server-uid" || store.server.Namespace != "tenant" {
		t.Fatalf("status=%d scope=%+v", rec.Code, store.server)
	}
}

func TestGatewayGetCannotReadOtherServerSession(t *testing.T) {
	store := &fakeSessionStore{item: sessionstate.Session{Namespace: "tenant", ServerNamespace: "tenant", ServerUID: "other-uid", Binding: policy.Binding{Name: "same-name"}}}
	h := gatewayTestHandler(store)
	req := httptest.NewRequest("GET", sessionstate.GatewayPath+"/tenant/same-name", nil)
	req.Header.Set("Authorization", "Bearer gateway-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayCompactedCursorReturnsGone(t *testing.T) {
	store := &fakeSessionStore{changesErr: sessionstate.ErrReset}
	h := gatewayTestHandler(store)
	req := httptest.NewRequest("GET", sessionstate.GatewayPath+"/changes?after=12&epoch=old", nil)
	req.Header.Set("Authorization", "Bearer gateway-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 410 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestSeparateListenersOwnOnlyTheirAudience(t *testing.T) {
	access := http.NewServeMux()
	Handler{Token: "control", Store: &fakeSessionStore{}}.RegisterAccess(access)
	for _, tt := range []struct {
		h     http.Handler
		path  string
		token string
	}{{access, sessionstate.GatewayPath, "control"}, {gatewayTestHandler(&fakeSessionStore{}), sessionstate.AccessPath, "gateway-token"}, {gatewayTestHandler(&fakeSessionStore{}), "/internal/identity/teams", "gateway-token"}} {
		req := httptest.NewRequest("GET", tt.path, nil)
		req.Header.Set("Authorization", "Bearer "+tt.token)
		rec := httptest.NewRecorder()
		tt.h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("path=%s status=%d", tt.path, rec.Code)
		}
	}
}

func TestAccessRejectsUnboundedAndTrailingBodies(t *testing.T) {
	mux := http.NewServeMux()
	Handler{Token: "control", Store: &fakeSessionStore{}}.RegisterAccess(mux)
	for _, body := range []string{`{} {}`, `{"unknown":true}`, strings.Repeat(" ", 1<<20) + "{}"} {
		req := httptest.NewRequest("POST", sessionstate.AccessPath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer control")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("status=%d", rec.Code)
		}
	}
}
