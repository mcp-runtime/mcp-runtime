package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota

	"mcp-ui/internal/assets"
	"mcp-ui/internal/auth"
	uiproxy "mcp-ui/internal/proxy"
	"mcp-ui/internal/session"

	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/platformmode"
	"mcp-runtime/pkg/publishscope"
	"mcp-runtime/pkg/serviceutil"
)

//go:embed static/*
var staticFS embed.FS

var (
	loginAttemptConfig     = configuredLoginAttempts()
	loginAttempts          = auth.NewAttemptTracker(time.Now, loginAttemptConfig)
	sessions               = session.NewStore(time.Now)
	authLoginHooks         auth.Hooks
	authHTTPClient         = &http.Client{Timeout: 10 * time.Second}
	sessionProxyHTTPClient = uiproxy.NewHTTPClient()
	forceSecureCookie      = boolEnvOr("UI_FORCE_SECURE_COOKIE", false)
)

func configuredLoginAttempts() auth.AttemptConfig {
	config := auth.DefaultAttemptConfig()
	config.Capacity = intEnvOr("UI_LOGIN_RATE_CAPACITY", config.Capacity)
	config.Refill = durationEnvOr("UI_LOGIN_RATE_REFILL", config.Refill)
	config.FailureWindow = durationEnvOr("UI_LOGIN_FAILURE_WINDOW", config.FailureWindow)
	config.FailureThreshold = intEnvOr("UI_LOGIN_FAILURE_THRESHOLD", config.FailureThreshold)
	config.LockoutDuration = durationEnvOr("UI_LOGIN_LOCKOUT", config.LockoutDuration)
	return config
}

func authServerForStore(store *session.Store) *auth.Server {
	return &auth.Server{Store: store, Client: auth.Client{HTTP: authHTTPClient}, Attempts: loginAttempts, Hooks: &authLoginHooks, ForceSecureCookie: forceSecureCookie}
}

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
	upstreamAPIKey := auth.FirstAPIKey(apiKeys)
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
	authServer := authServerForStore(sessions)
	mux.HandleFunc("/auth/login", authServer.Login(apiKey, upstreamAPIKey, apiUpstream))
	mux.HandleFunc("/auth/logout", authServer.Logout())
	mux.HandleFunc("/auth/status", authServer.Status())
	parsedAPIKeys := auth.ParseAPIKeyList(apiKeys)
	parsedAdminAPIKeys := auth.ParseAPIKeyList(adminAPIKeys)
	mux.HandleFunc("/auth/admin-check", authServer.AdminCheck(parsedAPIKeys, parsedAdminAPIKeys, legacyAdminAPIKeyFallbackEnabled()))

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
