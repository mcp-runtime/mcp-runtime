package proxy

import (
	"crypto/hmac"
	"net/http"
	"net/url"
	"strings"

	"mcp-ui/internal/session"
)

// CSRF protection for the cookie-backed UI session proxy.
//
// The session cookie is SameSite=Strict, which already blocks the common
// cross-site form/fetch vectors, but SameSite is a defense-in-depth control
// rather than a guarantee: it is browser-enforced, has historically varied
// across implementations, and does nothing for a same-site subdomain attacker.
// Any state-changing route reached with an ambient cookie therefore carries its
// own token.
//
// This is a synchroniser-token scheme rather than a double-submit cookie: the
// authoritative token lives in the server-side session record, so an attacker
// who can set cookies on the origin still cannot forge a matching pair. The
// token is handed to same-origin JavaScript in the /auth/login and /auth/status
// bodies; the same-origin policy is what keeps a cross-origin page from reading
// it.
const CSRFHeaderName = "X-CSRF-Token"

// unsafeHTTPMethod reports whether a method may change server state and so
// requires CSRF verification. Anything not explicitly known to be safe is
// treated as unsafe, so a new method added later fails closed.
func unsafeHTTPMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

// csrfTokenValid compares the presented token against the session token in
// constant time. An empty session token can never be satisfied, so a session
// minted before CSRF existed cannot be used to mutate state.
func csrfTokenValid(sess session.Session, presented string) bool {
	expected := strings.TrimSpace(sess.CSRFToken)
	if expected == "" {
		return false
	}
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(presented))
}

// sameOriginRequest checks the Origin header against the request's own host.
// It is a second, independent signal: a browser sets Origin on every unsafe
// cross-origin request and scripts cannot forge it. A missing Origin is not
// treated as a failure because same-origin requests may legitimately omit it;
// the token check is what makes the decision in that case.
func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// verifyCSRF returns the HTTP status to reject with, or 0 when the request may
// proceed. Ordering matters: origin is cheap and unambiguous, so it is checked
// before the token comparison.
func verifyCSRF(r *http.Request, sess session.Session) int {
	if !unsafeHTTPMethod(r.Method) {
		return 0
	}
	if !sameOriginRequest(r) {
		return http.StatusForbidden
	}
	if !csrfTokenValid(sess, r.Header.Get(CSRFHeaderName)) {
		return http.StatusForbidden
	}
	return 0
}
