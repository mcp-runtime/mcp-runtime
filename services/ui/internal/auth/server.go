package auth

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"log"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/serviceutil"
	"mcp-ui/internal/session"
	"net/http"
	"strings"
	"time"
)

// Hooks override credential verification for injected integrations; normal
// requests use Client. They never replace store issuance or login throttling.
type Hooks struct {
	Password func(context.Context, string, string, string) (Principal, string, time.Time, error)
	OIDC     func(context.Context, string, string) (Principal, string, time.Time, error)
}

// Server owns browser authentication, throttling and session-cookie responses.
// Bootstrap provides its store, upstream client, limiter and cookie policy.
type Server struct {
	Store             *session.Store
	Client            Client
	Attempts          *AttemptTracker
	Hooks             *Hooks
	ForceSecureCookie bool
}

const (
	loginFailureLogEvery = 3
	loginRequestMaxBytes = 8 * 1024
	loginClientUnknownIP = platformauth.UnknownRequestIP
)

func (s *Server) Login(apiKey, upstreamAPIKey, apiUpstream string) http.HandlerFunc {
	type loginRequest struct {
		APIKey   string `json:"api_key"`
		IDToken  string `json:"id_token"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("allow", http.MethodPost)
			serviceutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		clientID := loginClientID(r)
		if !s.Attempts.Allow(clientID) {
			serviceutil.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_requests"})
			return
		}

		var req loginRequest
		r.Body = http.MaxBytesReader(w, r.Body, loginRequestMaxBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			serviceutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}

		presentedAPIKey := strings.TrimSpace(req.APIKey)
		idToken := strings.TrimSpace(req.IDToken)
		email := strings.TrimSpace(req.Email)
		password := strings.TrimSpace(req.Password)
		if presentedAPIKey == "" && idToken == "" && (email == "" || password == "") {
			serviceutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_credentials"})
			return
		}

		var (
			sess session.Session
			err  error
		)

		if email != "" || password != "" {
			var (
				p         Principal
				token     string
				expiresAt time.Time
				verifyErr error
			)
			if s.Hooks != nil && s.Hooks.Password != nil {
				p, token, expiresAt, verifyErr = s.Hooks.Password(r.Context(), apiUpstream, email, password)
			} else {
				p, token, expiresAt, verifyErr = s.Client.Password(r.Context(), apiUpstream, email, password)
			}
			if verifyErr != nil {
				failures := s.Attempts.RecordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "password")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = s.Store.Create(r.Context(), session.Session{
				Principal:          p,
				UpstreamAuthHeader: "Bearer " + token,
				ExpiresAt:          expiresAt,
			})
		} else if idToken != "" {
			var (
				p         Principal
				token     string
				expiresAt time.Time
				verifyErr error
			)
			if s.Hooks != nil && s.Hooks.OIDC != nil {
				p, token, expiresAt, verifyErr = s.Hooks.OIDC(r.Context(), apiUpstream, idToken)
			} else {
				p, token, expiresAt, verifyErr = s.Client.Session(r.Context(), apiUpstream, idToken)
			}
			if verifyErr != nil {
				failures := s.Attempts.RecordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "oidc")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = s.Store.Create(r.Context(), session.Session{
				Principal:          p,
				UpstreamAuthHeader: "Bearer " + token,
				ExpiresAt:          expiresAt,
			})
		} else {
			if apiKey == "" {
				serviceutil.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_key_not_configured"})
				return
			}
			if !hmac.Equal([]byte(presentedAPIKey), []byte(apiKey)) {
				failures := s.Attempts.RecordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "api_key")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = s.Store.Create(r.Context(), session.Session{
				Principal: Principal{
					Role:     "admin",
					AuthType: "ui_api_key",
				},
				UpstreamAPIKey: upstreamAPIKey,
			})
		}
		if err != nil {
			serviceutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_create_failed"})
			return
		}

		priorFailures := s.Attempts.RecordSuccess(clientID)
		if priorFailures > 0 {
			// #nosec G706 -- authentication telemetry log with bounded fields.
			log.Printf(`auth_login_success_after_failures timestamp=%q prior_failures=%d`, time.Now().UTC().Format(time.RFC3339), priorFailures)
		}
		http.SetCookie(w, s.newSessionCookie(r, sess.ID, sess.ExpiresAt))
		serviceutil.WriteJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"principal":     sess.Principal,
			"csrf_token":    sess.CSRFToken,
		})
	}
}

func loginClientID(r *http.Request) string {
	return platformauth.RequestIP(r)
}

func (s *Server) Logout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("allow", http.MethodPost)
			serviceutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if cookie, err := r.Cookie(session.CookieName); err == nil {
			s.Store.Delete(strings.TrimSpace(cookie.Value))
		}
		http.SetCookie(w, s.expiredSessionCookie(r))
		serviceutil.WriteJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
	}
}

// handleAdminCheck is the Traefik forwardAuth target for /grafana. It returns
// 204 when the caller is an admin (logged-in UI session or admin API key) and
// 401 otherwise. The original request body and path do not matter — Traefik
// forwards only headers and consumes the status. The Cache-Control header is
// set by securityHeadersMiddleware for /auth/.
func (s *Server) AdminCheck(apiKeys, adminAPIKeys []string, legacyAdminKeys bool) http.HandlerFunc {
	gateKeys := adminAPIKeys
	if len(gateKeys) == 0 && legacyAdminKeys {
		gateKeys = apiKeys
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := s.Store.FromRequest(r); ok && strings.EqualFold(sess.Principal.Role, "admin") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if validAPIKeyHeader(r, gateKeys) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
}

func matchAPIKey(presented string, keys []string) bool {
	for _, key := range keys {
		if hmac.Equal([]byte(presented), []byte(key)) {
			return true
		}
	}
	return false
}

// parseAPIKeyList splits a comma-separated API-key list into a slice of
// trimmed, non-empty entries, suitable for use as a per-process lookup.
func ParseAPIKeyList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, key := range parts {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *Server) Status() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.Store.FromRequest(r)
		if !ok {
			serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"authenticated": false})
			return
		}
		serviceutil.WriteJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"principal":     sess.Principal,
			"csrf_token":    sess.CSRFToken,
		})
	}
}

func (s *Server) newSessionCookie(r *http.Request, sessionID string, expiresAt time.Time) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = int(session.Duration.Seconds())
	}
	// #nosec G124 -- Secure is enabled automatically for TLS / x-forwarded-proto=https; HttpOnly and SameSite are set.
	return &http.Cookie{
		Name:     session.CookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	}
}

func (s *Server) expiredSessionCookie(r *http.Request) *http.Cookie {
	// #nosec G124 -- Secure is enabled automatically for TLS / x-forwarded-proto=https; HttpOnly and SameSite are set.
	return &http.Cookie{
		Name:     session.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	}
}

func validAPIKeyHeader(r *http.Request, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	presented := strings.TrimSpace(r.Header.Get("x-api-key"))
	if presented == "" {
		return false
	}
	return matchAPIKey(presented, keys)
}

func FirstAPIKey(apiKeys string) string {
	for _, key := range strings.Split(apiKeys, ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (s *Server) secureCookie(r *http.Request) bool {
	if s.ForceSecureCookie {
		return true
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("x-forwarded-proto"), "https")
}
