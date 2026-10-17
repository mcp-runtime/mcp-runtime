package main

import (
	"mcp-ui/internal/auth"
	"mcp-ui/internal/session"
	"net/http"
	"time"
)

type sessionPrincipal = session.Principal
type uiSession = session.Session
type uiSessionStore = session.Store

const sessionCookieName = session.CookieName

var loginFailureThreshold = loginAttemptConfig.FailureThreshold

func newUISessionStore(now func() time.Time) *session.Store { return session.NewStore(now) }
func newLoginAttemptTracker(now func() time.Time) *auth.AttemptTracker {
	return auth.NewAttemptTracker(now, loginAttemptConfig)
}
func handleLogin(apiKey, upstreamAPIKey, apiUpstream string, store *session.Store) http.HandlerFunc {
	return authServerForStore(store).Login(apiKey, upstreamAPIKey, apiUpstream)
}
func handleLogout(store *session.Store) http.HandlerFunc { return authServerForStore(store).Logout() }
func handleStatus(store *session.Store) http.HandlerFunc { return authServerForStore(store).Status() }
func handleAdminCheck(store *session.Store, keys, adminKeys []string, legacy bool) http.HandlerFunc {
	return authServerForStore(store).AdminCheck(keys, adminKeys, legacy)
}
func parseAPIKeyList(raw string) []string { return auth.ParseAPIKeyList(raw) }
