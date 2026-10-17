package proxy

import (
	"net/http"
	"net/url"
	"time"

	"mcp-ui/internal/session"
)

type uiSession = session.Session
type sessionPrincipal = session.Principal
type uiSessionStore = session.Store

const sessionCookieName = session.CookieName

func newUISessionStore(now func() time.Time) *session.Store { return session.NewStore(now) }
func newSessionProxyWithUpstreams(platform, runtime, analytics *url.URL, store *session.Store) *SessionProxy {
	return NewSessionProxy(platform, runtime, analytics, store, NewHTTPClient())
}
func newPublicCatalogProxy(runtime *url.URL, enabled bool) http.Handler {
	return NewPublicCatalogProxy(runtime, enabled, NewHTTPClient())
}
func grafanaSessionProxy(platform, runtime *url.URL, store *session.Store, prefix string) http.Handler {
	return NewGrafanaProxy(platform, runtime, store, prefix, NewHTTPClient())
}
