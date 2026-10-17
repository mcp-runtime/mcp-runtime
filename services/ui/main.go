package main

import (
	"context"
	"crypto/hmac"
	"embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota

	"mcp-ui/internal/assets"
	"mcp-ui/internal/auth"
	uiproxy "mcp-ui/internal/proxy"
	"mcp-ui/internal/session"

	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/platformmode"
	"mcp-runtime/pkg/publishscope"
	"mcp-runtime/pkg/serviceutil"
)

//go:embed static/*
var staticFS embed.FS

const (
	sessionCookieName = session.CookieName
	sessionDuration   = session.Duration

	defaultLoginRateLimitCapacity = 10
	defaultLoginRateLimitRefill   = time.Minute
	defaultLoginFailureWindow     = 15 * time.Minute
	defaultLoginFailureThreshold  = 5
	defaultLoginLockoutDuration   = 5 * time.Minute
	loginAttemptIdleTTL           = 30 * time.Minute
	loginAttemptMaxClients        = 4096
	loginAttemptPruneInterval     = time.Minute
	loginAttemptEvictionBatch     = 256
	loginFailureLogEvery          = 3
	loginRequestMaxBytes          = 8 * 1024
)

var (
	loginRateLimitCapacity = intEnvOr("UI_LOGIN_RATE_CAPACITY", defaultLoginRateLimitCapacity)
	loginRateLimitRefill   = durationEnvOr("UI_LOGIN_RATE_REFILL", defaultLoginRateLimitRefill)
	loginFailureWindow     = durationEnvOr("UI_LOGIN_FAILURE_WINDOW", defaultLoginFailureWindow)
	loginFailureThreshold  = intEnvOr("UI_LOGIN_FAILURE_THRESHOLD", defaultLoginFailureThreshold)
	loginLockoutDuration   = durationEnvOr("UI_LOGIN_LOCKOUT", defaultLoginLockoutDuration)
	forceSecureCookie      = boolEnvOr("UI_FORCE_SECURE_COOKIE", false)
	passwordLoginHook      func(context.Context, string, string, string) (sessionPrincipal, string, time.Time, error)
)

type sessionPrincipal = session.Principal

type uiSession = session.Session
type uiSessionStore = session.Store

func newUISessionStore(now func() time.Time) *uiSessionStore {
	return session.NewStore(now)
}

type loginAttemptTracker struct {
	mu        sync.Mutex
	clients   map[string]*loginClientState
	now       func() time.Time
	lastPrune time.Time
}

type loginClientState struct {
	tokens         int
	lastRefill     time.Time
	lastSeen       time.Time
	failures       int
	failuresExpire time.Time
	lockedUntil    time.Time
}

var (
	loginAttempts          = newLoginAttemptTracker(time.Now)
	sessions               = newUISessionStore(time.Now)
	oidcLoginHook          func(context.Context, string, string) (sessionPrincipal, string, time.Time, error)
	authHTTPClient         = &http.Client{Timeout: 10 * time.Second}
	sessionProxyHTTPClient = uiproxy.NewHTTPClient()
)

// main initializes and starts the MCP Runtime UI server.
// It serves static web assets and provides a dynamic /config.js endpoint
// with API configuration for the frontend. Includes tracing support.
func main() {
	port := serviceutil.EnvOr("PORT", "8082")
	apiBase := serviceutil.EnvOr("API_BASE", "/api/v1")
	apiKey := strings.TrimSpace(os.Getenv("API_KEY"))
	apiKeys := strings.TrimSpace(os.Getenv("API_KEYS"))
	adminAPIKeys := strings.TrimSpace(os.Getenv("ADMIN_API_KEYS"))
	apiUpstream := serviceutil.EnvOr("API_UPSTREAM", "http://mcp-platform-api.mcp-platform.svc.cluster.local:8080")
	if apiKey == "" && apiKeys == "" {
		log.Printf("WARNING: neither API_KEY nor API_KEYS is set; UI API-key login is disabled")
	}

	if err := configureSessionStore(context.Background()); err != nil {
		log.Fatalf("invalid session store configuration: %v", err)
	}

	mux, err := newMux(apiBase, apiUpstream, apiKey, apiKeys, adminAPIKeys)
	if err != nil {
		log.Fatalf("invalid API, runtime, or analytics upstream: %v", err)
	}

	shutdown, err := serviceutil.InitTracer("mcp-ui")
	if err != nil {
		log.Printf("otel init failed: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}()
	}

	log.Printf("mcp-ui listening on :%s", port)
	httpsMode := serviceutil.EnvOr("UI_REQUIRE_HTTPS", "auto")
	secured := securityHeadersMiddleware(httpsRedirectMiddleware(mux, httpsMode))
	handler := otelhttp.NewHandler(serviceutil.LogRequests(secured), "http.server")
	readTimeout := durationEnvOr("UI_HTTP_READ_TIMEOUT", 20*time.Minute)
	writeTimeout := durationEnvOr("UI_HTTP_WRITE_TIMEOUT", 20*time.Minute)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func newMux(apiBase, apiUpstream, apiKey, apiKeys, adminAPIKeys string) (*http.ServeMux, error) {
	routes := configuredPublicRoutes()
	if err := routes.Validate(); err != nil {
		return nil, err
	}
	apiBase = normalizePathPrefix(apiBase)
	upstreamAPIKey := firstAPIKey(apiKeys)
	if upstreamAPIKey == "" {
		upstreamAPIKey = apiKey
		apiKeys = apiKey
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	platformMode := normalizedPlatformMode()
	defaultNamespace := strings.TrimSpace(os.Getenv("UI_DEFAULT_NAMESPACE"))
	if defaultNamespace == "" {
		defaultNamespace = defaultCatalogNamespaceForMode(platformMode)
	}
	defaultPolicyVersion := serviceutil.EnvOr("UI_DEFAULT_POLICY_VERSION", mcpdefaults.PolicyVersion)
	baseJSON, err := json.Marshal(apiBase)
	if err != nil {
		return nil, err
	}
	defaultsJSON, err := json.Marshal(map[string]string{
		"namespace":     defaultNamespace,
		"policyVersion": defaultPolicyVersion,
	})
	if err != nil {
		return nil, err
	}
	googleClientID := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
	if googleClientID == "" {
		// Keep the documented MCP-prefixed alias working for deployments that
		// inject all platform settings through MCP_* environment variables.
		googleClientID = strings.TrimSpace(os.Getenv("MCP_GOOGLE_CLIENT_ID"))
	}
	googleClientIDJSON, err := json.Marshal(googleClientID)
	if err != nil {
		return nil, err
	}
	platformModeJSON, err := json.Marshal(platformMode)
	if err != nil {
		return nil, err
	}
	publicRoutesJSON, err := json.Marshal(map[string]string{"prefix": routes.PlatformPrefix(), "docs": routes.Docs, "grafana": routes.Grafana, "registry": routes.Registry})
	if err != nil {
		return nil, err
	}
	configJS := "window.MCP_PUBLIC_ROUTES = " + string(publicRoutesJSON) + ";\n" + "window.MCP_API_BASE = " + string(baseJSON) + ";\n" +
		"window.MCP_DEFAULTS = " + string(defaultsJSON) + ";\n" +
		"window.MCP_PLATFORM_MODE = " + string(platformModeJSON) + ";\n" +
		"window.MCP_GOOGLE_CLIENT_ID = " + string(googleClientIDJSON) + ";"
	mux.HandleFunc("/config.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/javascript")
		_, _ = w.Write([]byte(configJS))
	})
	mux.HandleFunc("/auth/login", handleLogin(apiKey, upstreamAPIKey, apiUpstream, sessions))
	mux.HandleFunc("/auth/logout", handleLogout(sessions))
	mux.HandleFunc("/auth/status", handleStatus(sessions))
	parsedAPIKeys := parseAPIKeyList(apiKeys)
	parsedAdminAPIKeys := parseAPIKeyList(adminAPIKeys)
	mux.HandleFunc("/auth/admin-check", handleAdminCheck(sessions, parsedAPIKeys, parsedAdminAPIKeys, legacyAdminAPIKeyFallbackEnabled()))

	runtimeBase, err := uiproxy.ParseUpstream(serviceutil.EnvOr("RUNTIME_UPSTREAM", uiproxy.DefaultRuntimeUpstream))
	if err != nil {
		return nil, err
	}
	analyticsBase, err := uiproxy.ParseUpstream(serviceutil.EnvOr("ANALYTICS_UPSTREAM", uiproxy.DefaultAnalyticsUpstream))
	if err != nil {
		return nil, err
	}
	platformBase, err := uiproxy.ParseUpstream(apiUpstream)
	if err != nil {
		return nil, err
	}
	mux.Handle(uiproxy.SessionAPIPrefix+"/", uiproxy.NewSessionProxy(platformBase, runtimeBase, analyticsBase, sessions, sessionProxyHTTPClient))
	mux.Handle(uiproxy.PublicCatalogAPIPrefix+"/", uiproxy.NewPublicCatalogProxy(runtimeBase, platformMode == "public", sessionProxyHTTPClient))

	mux.Handle("/", assets.Handler{Files: staticFS, RewriteHTML: func(data []byte) []byte { return prefixAssetHTML(data, routes.PlatformPrefix()) }})
	return mountPublicUI(mux, routes, platformBase), nil
}

func handleLogin(apiKey, upstreamAPIKey, apiUpstream string, store *uiSessionStore) http.HandlerFunc {
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
		if !loginAttempts.allow(clientID) {
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
			sess uiSession
			err  error
		)

		if email != "" || password != "" {
			var (
				p         sessionPrincipal
				token     string
				expiresAt time.Time
				verifyErr error
			)
			if passwordLoginHook != nil {
				p, token, expiresAt, verifyErr = passwordLoginHook(r.Context(), apiUpstream, email, password)
			} else {
				p, token, expiresAt, verifyErr = (auth.Client{HTTP: authHTTPClient}).Password(r.Context(), apiUpstream, email, password)
			}
			if verifyErr != nil {
				failures := loginAttempts.recordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "password")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = store.Create(r.Context(), uiSession{
				Principal:          p,
				UpstreamAuthHeader: "Bearer " + token,
				ExpiresAt:          expiresAt,
			})
		} else if idToken != "" {
			var (
				p         sessionPrincipal
				token     string
				expiresAt time.Time
				verifyErr error
			)
			if oidcLoginHook != nil {
				p, token, expiresAt, verifyErr = oidcLoginHook(r.Context(), apiUpstream, idToken)
			} else {
				p, token, expiresAt, verifyErr = (auth.Client{HTTP: authHTTPClient}).Session(r.Context(), apiUpstream, idToken)
			}
			if verifyErr != nil {
				failures := loginAttempts.recordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "oidc")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = store.Create(r.Context(), uiSession{
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
				failures := loginAttempts.recordFailure(clientID)
				if failures >= loginFailureLogEvery {
					// #nosec G706 -- authentication telemetry log with bounded fields.
					log.Printf(`auth_login_failure client=%q timestamp=%q failure_count=%d mode=%q`, clientID, time.Now().UTC().Format(time.RFC3339), failures, "api_key")
				}
				serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			sess, err = store.Create(r.Context(), uiSession{
				Principal: sessionPrincipal{
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

		priorFailures := loginAttempts.recordSuccess(clientID)
		if priorFailures > 0 {
			// #nosec G706 -- authentication telemetry log with bounded fields.
			log.Printf(`auth_login_success_after_failures timestamp=%q prior_failures=%d`, time.Now().UTC().Format(time.RFC3339), priorFailures)
		}
		http.SetCookie(w, newSessionCookie(r, sess.ID, sess.ExpiresAt))
		serviceutil.WriteJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"principal":     sess.Principal,
			"csrf_token":    sess.CSRFToken,
		})
	}
}

const loginClientUnknownIP = platformauth.UnknownRequestIP

func loginClientID(r *http.Request) string {
	return platformauth.RequestIP(r)
}

func newLoginAttemptTracker(now func() time.Time) *loginAttemptTracker {
	return &loginAttemptTracker{clients: map[string]*loginClientState{}, now: now}
}

func (t *loginAttemptTracker) allow(clientID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.stateForLocked(clientID, now)
	state.lastSeen = now
	refillLoginTokens(state, now)
	if now.Before(state.lockedUntil) {
		return false
	}
	if state.tokens <= 0 {
		return false
	}
	state.tokens--
	t.enforceMaxLocked(now)
	return true
}

func (t *loginAttemptTracker) recordFailure(clientID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.stateForLocked(clientID, now)
	state.lastSeen = now
	if now.After(state.failuresExpire) {
		state.failures = 0
	}
	state.failures++
	state.failuresExpire = now.Add(loginFailureWindow)
	if state.failures >= loginFailureThreshold {
		state.lockedUntil = now.Add(loginLockoutDuration)
	}
	t.enforceMaxLocked(now)
	return state.failures
}

func (t *loginAttemptTracker) recordSuccess(clientID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.pruneIfDueLocked(now)
	state := t.clients[clientID]
	if state == nil {
		return 0
	}
	state.lastSeen = now
	prior := state.failures
	state.failures = 0
	state.failuresExpire = time.Time{}
	state.lockedUntil = time.Time{}
	return prior
}

func (t *loginAttemptTracker) stateForLocked(clientID string, now time.Time) *loginClientState {
	state := t.clients[clientID]
	if state == nil {
		state = &loginClientState{tokens: loginRateLimitCapacity, lastRefill: now, lastSeen: now}
		t.clients[clientID] = state
	}
	return state
}

func (t *loginAttemptTracker) pruneIfDueLocked(now time.Time) {
	if !t.lastPrune.IsZero() && now.Sub(t.lastPrune) < loginAttemptPruneInterval {
		return
	}
	t.lastPrune = now
	for clientID, state := range t.clients {
		if now.Sub(state.lastSeen) > loginAttemptIdleTTL && !now.Before(state.lockedUntil) {
			delete(t.clients, clientID)
		}
	}
}

func (t *loginAttemptTracker) enforceMaxLocked(now time.Time) {
	if len(t.clients) <= loginAttemptMaxClients {
		return
	}
	target := loginAttemptMaxClients - loginAttemptEvictionBatch
	if target < 0 {
		target = 0
	}
	type candidate struct {
		clientID string
		lastSeen time.Time
		failures int
		locked   bool
	}
	candidates := make([]candidate, 0, len(t.clients))
	for clientID, state := range t.clients {
		candidates = append(candidates, candidate{
			clientID: clientID,
			lastSeen: state.lastSeen,
			failures: state.failures,
			locked:   now.Before(state.lockedUntil),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].locked != candidates[j].locked {
			return !candidates[i].locked
		}
		if (candidates[i].failures > 0) != (candidates[j].failures > 0) {
			return candidates[i].failures == 0
		}
		return candidates[i].lastSeen.Before(candidates[j].lastSeen)
	})
	for _, entry := range candidates[:len(t.clients)-target] {
		delete(t.clients, entry.clientID)
	}
}

func refillLoginTokens(state *loginClientState, now time.Time) {
	if state.lastRefill.IsZero() {
		state.lastRefill = now
	}
	elapsed := now.Sub(state.lastRefill)
	if elapsed < loginRateLimitRefill {
		return
	}
	refill := int(elapsed / loginRateLimitRefill)
	state.tokens += refill
	if state.tokens > loginRateLimitCapacity {
		state.tokens = loginRateLimitCapacity
	}
	state.lastRefill = state.lastRefill.Add(time.Duration(refill) * loginRateLimitRefill)
}

func handleLogout(store *uiSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("allow", http.MethodPost)
			serviceutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			store.Delete(strings.TrimSpace(cookie.Value))
		}
		http.SetCookie(w, expiredSessionCookie(r))
		serviceutil.WriteJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
	}
}

// handleAdminCheck is the Traefik forwardAuth target for /grafana. It returns
// 204 when the caller is an admin (logged-in UI session or admin API key) and
// 401 otherwise. The original request body and path do not matter — Traefik
// forwards only headers and consumes the status. The Cache-Control header is
// set by securityHeadersMiddleware for /auth/.
func handleAdminCheck(store *uiSessionStore, apiKeys, adminAPIKeys []string, legacyAdminKeys bool) http.HandlerFunc {
	gateKeys := adminAPIKeys
	if len(gateKeys) == 0 && legacyAdminKeys {
		gateKeys = apiKeys
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := store.FromRequest(r); ok && strings.EqualFold(sess.Principal.Role, "admin") {
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
func parseAPIKeyList(s string) []string {
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

func handleStatus(store *uiSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := store.FromRequest(r)
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

func newSessionCookie(r *http.Request, sessionID string, expiresAt time.Time) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = int(sessionDuration.Seconds())
	}
	// #nosec G124 -- Secure is enabled automatically for TLS / x-forwarded-proto=https; HttpOnly and SameSite are set.
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	}
}

func expiredSessionCookie(r *http.Request) *http.Cookie {
	// #nosec G124 -- Secure is enabled automatically for TLS / x-forwarded-proto=https; HttpOnly and SameSite are set.
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie(r),
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

func firstAPIKey(apiKeys string) string {
	for _, key := range strings.Split(apiKeys, ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizedPlatformMode() string {
	return string(platformmode.FromEnv())
}

func catalogNamespacesForMode(mode string) []string {
	normalized, _ := platformmode.Normalize(mode)
	if normalized == platformmode.ModePublic {
		// This list powers anonymous proxy reads, so exclude org catalogs.
		return normalized.CatalogNamespacesForScope(publishscope.Public)
	}
	return normalized.CatalogNamespaces()
}

func defaultCatalogNamespaceForMode(mode string) string {
	normalized, _ := platformmode.Normalize(mode)
	return normalized.DefaultCatalogNamespace()
}

func secureCookie(r *http.Request) bool {
	if forceSecureCookie {
		return true
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("x-forwarded-proto"), "https")
}

func normalizePathPrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "/api/v1"
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" && parsed.Path != trimmed {
		trimmed = parsed.Path
	}
	trimmed = "/" + strings.Trim(trimmed, "/")
	if trimmed == "/" {
		return "/api/v1"
	}
	return trimmed
}

// httpsRedirectMiddleware redirects HTTP requests to HTTPS based on the
// X-Forwarded-Proto header set by an upstream TLS-terminating proxy.
//
// mode controls behavior:
//   - "false"/"off"/"0"/"no": never redirect (useful in dev or when fronted differently)
//   - "true"/"on"/"1"/"yes": always redirect on X-Forwarded-Proto: http
//   - anything else (default "auto"): redirect only when Host looks public
//     (not localhost / not a bare IP). This is safe for the bundled Kind dev
//     stack where Host is `localhost:18080`.
func httpsRedirectMiddleware(next http.Handler, mode string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldRedirectToHTTPS(r, mode) {
			target := "https://" + r.Host + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func shouldRedirectToHTTPS(r *http.Request, mode string) bool {
	if r.URL != nil && r.URL.Path == "/auth/admin-check" {
		return false
	}
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	forcedMode := false
	switch normalizedMode {
	case "false", "off", "0", "no":
		return false
	case "true", "on", "1", "yes":
		forcedMode = true
	default:
		if isLocalHost(r.Host) {
			return false
		}
	}
	if r.TLS != nil {
		return false
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("x-forwarded-proto")))
	if proto == "https" {
		return false
	}
	if proto == "http" {
		return true
	}
	// No proxy header. Only redirect in forced mode for non-local hosts.
	return forcedMode && !isLocalHost(r.Host)
}

func isLocalHost(host string) bool {
	if host == "" {
		return true
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(h)
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	return false
}

// securityHeadersMiddleware adds baseline security headers on every response.
// HSTS is added only when the request was served over HTTPS so it never asks a
// browser to upgrade dev hostnames that have no certificate.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
		// Google Sign-In needs accounts.google.com for scripts/iframes/connect.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' https://accounts.google.com https://apis.google.com; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://accounts.google.com; "+
				"img-src 'self' data: https:; "+
				"font-src 'self' data: https://fonts.gstatic.com; "+
				"connect-src 'self' https://accounts.google.com; "+
				"frame-src https://accounts.google.com; "+
				// The dashboard no longer frames anything on its own origin
				// (the legacy same-origin iframe was removed in Phase 5), so
				// 'none' is safe here and blocks every ancestor, same-origin
				// included.
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		if isHTTPSRequest(r) {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		path := strings.TrimPrefix(r.URL.Path, configuredPublicRoutes().PlatformPrefix())
		if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/auth/") {
			h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("x-forwarded-proto")), "https")
}

func intEnvOr(key string, fallback int) int {
	parsed := serviceutil.EnvInt(key, fallback)
	if parsed <= 0 {
		// #nosec G706 -- fixed-format env validation log for local operator diagnostics.
		log.Printf("invalid %s=%q; using default %d", key, strings.TrimSpace(os.Getenv(key)), fallback)
		return fallback
	}
	return parsed
}

func durationEnvOr(key string, fallback time.Duration) time.Duration {
	parsed := serviceutil.EnvDuration(key, fallback)
	if parsed <= 0 {
		// #nosec G706 -- fixed-format env validation log for local operator diagnostics.
		log.Printf("invalid %s=%q; using default %s", key, strings.TrimSpace(os.Getenv(key)), fallback)
		return fallback
	}
	return parsed
}

func boolEnvOr(key string, fallback bool) bool {
	if parsed, ok := serviceutil.BoolEnv(key); ok {
		return parsed
	}
	return fallback
}

func legacyAdminAPIKeyFallbackEnabled() bool {
	for _, key := range []string{"MCP_LEGACY_ADMIN_API_KEY_FALLBACK", "LEGACY_ADMIN_API_KEY_FALLBACK"} {
		if parsed, ok := serviceutil.BoolEnv(key); ok {
			return parsed
		}
	}
	return strings.TrimSpace(os.Getenv("MCP_RUNTIME_TEST_MODE")) == "1"
}

// configureSessionStore installs the explicitly configured backend before mux
// construction, so handlers share one store without package-owned process env.
func configureSessionStore(ctx context.Context) error {
	backend, err := session.NewBackend(ctx, os.Getenv("UI_SESSION_STORE"), os.Getenv("UI_SESSION_DATABASE_URL"), os.Getenv("UI_SESSION_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}
	sessions = session.NewStoreWithBackend(backend, time.Now)
	return nil
}
