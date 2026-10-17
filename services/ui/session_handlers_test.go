package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-ui/internal/session"
)

func TestSharedSessionHandlersAcrossReplicas(t *testing.T) {
	backend, err := session.NewBackend(context.Background(), "memory", "", "")
	if err != nil {
		t.Fatal(err)
	}
	podA, podB := session.NewStoreWithBackend(backend, time.Now), session.NewStoreWithBackend(backend, time.Now)
	sess, err := podA.Create(context.Background(), uiSession{Principal: sessionPrincipal{Role: "admin", Subject: "alice"}, UpstreamAPIKey: "synthetic-upstream"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		return r
	}
	status := httptest.NewRecorder()
	handleStatus(podB).ServeHTTP(status, request(http.MethodGet, "/auth/status"))
	if !strings.Contains(status.Body.String(), `"authenticated":true`) || !strings.Contains(status.Body.String(), sess.CSRFToken) {
		t.Fatalf("replica B status=%s", status.Body.String())
	}
	admin := httptest.NewRecorder()
	handleAdminCheck(podB, nil, nil, false).ServeHTTP(admin, request(http.MethodGet, "/auth/admin-check"))
	if admin.Code != http.StatusNoContent {
		t.Fatalf("replica B admin-check=%d", admin.Code)
	}
	handleLogout(podB).ServeHTTP(httptest.NewRecorder(), request(http.MethodPost, "/auth/logout"))
	if _, ok := podA.Get(sess.ID); ok {
		t.Fatal("logout on replica B left replica A authenticated")
	}
}
