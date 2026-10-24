package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota

	"mcp-gateway/credential"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/serviceutil"
	"mcp-runtime/pkg/sessionstate"
)

func main() {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MCP_GATEWAY_MODE")), "credential") {
		path := strings.TrimSpace(os.Getenv("MCP_GATEWAY_CONFIG"))
		if path == "" {
			log.Fatal("MCP_GATEWAY_CONFIG is required in credential mode")
		}
		if err := credential.Run(path); err != nil {
			log.Fatalf("credential gateway: %v", err)
		}
		return
	}
	port := serviceutil.EnvOr("PORT", strconv.Itoa(mcpdefaults.MCPGatewayPort))
	metricsPort := serviceutil.EnvOr("METRICS_PORT", "9103")
	upstream := serviceutil.EnvOr("UPSTREAM_URL", "http://127.0.0.1:8090")
	analyticsURL := strings.TrimSpace(os.Getenv("ANALYTICS_INGEST_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ANALYTICS_API_KEY"))
	analyticsTokenFile := strings.TrimSpace(os.Getenv("ANALYTICS_TOKEN_FILE"))
	source := serviceutil.EnvOr("ANALYTICS_SOURCE", "mcp-gateway")
	eventType := serviceutil.EnvOr("ANALYTICS_EVENT_TYPE", "mcp.request")
	stripPrefix := strings.TrimSpace(os.Getenv("STRIP_PREFIX"))
	externalBaseURL, err := parseExternalBaseURL(strings.TrimSpace(os.Getenv("EXTERNAL_BASE_URL")))
	if err != nil {
		log.Fatalf("invalid EXTERNAL_BASE_URL: %v", err)
	}

	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("invalid UPSTREAM_URL: %v", err)
	}

	proxy := newUpstreamReverseProxy(target)
	proxy.ErrorHandler = handleUpstreamError

	analyticsTLS, err := serviceutil.ClientTLSFromEnv("ANALYTICS")
	if err != nil {
		log.Fatalf("configure analytics TLS: %v", err)
	}
	if analyticsTLS != nil && !strings.HasPrefix(analyticsURL, "https://") {
		log.Fatal("configured analytics TLS requires an HTTPS ingest URL")
	}
	clients := newGatewayHTTPClients(analyticsTLS)

	srv := &gatewayServer{
		proxy:                  proxy,
		upstreamURL:            target,
		metrics:                newGatewayMetrics(prometheus.DefaultRegisterer),
		analyticsURL:           analyticsURL,
		apiKey:                 apiKey,
		analyticsToken:         newTokenFileSource(analyticsTokenFile),
		source:                 source,
		eventType:              eventType,
		stripPrefix:            stripPrefix,
		externalBaseURL:        externalBaseURL,
		httpClient:             clients.analytics,
		oauthHTTPClient:        clients.oauth,
		oauthInternalIssuerURL: strings.TrimSpace(os.Getenv("OAUTH_INTERNAL_ISSUER_URL")),
		policyFile:             strings.TrimSpace(os.Getenv("POLICY_FILE")),
		serverName:             strings.TrimSpace(os.Getenv("MCP_SERVER_NAME")),
		serverNamespace:        strings.TrimSpace(os.Getenv("MCP_SERVER_NAMESPACE")),
		serverUID:              strings.TrimSpace(os.Getenv("MCP_SERVER_UID")),
		clusterName:            strings.TrimSpace(os.Getenv("MCP_CLUSTER_NAME")),
		verifiedSPIFFEHeader:   serviceutil.EnvOr("VERIFIED_SPIFFE_HEADER", defaultVerifiedSPIFFEHeader),
		trustedProxySPIFFE:     strings.TrimSpace(os.Getenv("TRUSTED_PROXY_SPIFFE_ID")),
		defaultPolicyMode:      serviceutil.EnvOr("POLICY_MODE", defaultPolicyMode),
		defaultPolicyDecision:  serviceutil.EnvOr("POLICY_DEFAULT_DECISION", defaultPolicyDecision),
		defaultPolicyVersion:   serviceutil.EnvOr("POLICY_VERSION", defaultPolicyVersion),
		oauthProviders:         map[string]*oauthProvider{},
	}
	if err := srv.startPolicyCache(); err != nil {
		log.Fatalf("initial policy load failed: %v", err)
	}
	if brokerURL := strings.TrimSpace(os.Getenv("CREDENTIAL_BROKER_URL")); brokerURL != "" {
		var err error
		srv.credentialBroker, err = newCredentialBroker(brokerURL, os.Getenv("CREDENTIAL_BROKER_TOKEN_FILE"), os.Getenv("CREDENTIAL_BROKER_CA_FILE"))
		if err != nil {
			log.Fatal(err)
		}
	}
	if baseURL := strings.TrimSpace(os.Getenv("SESSION_SYNC_URL")); baseURL != "" {
		staleWindow := sessionstate.DefaultStaleWindow
		if raw := strings.TrimSpace(os.Getenv("SESSION_SYNC_STALE_WINDOW")); raw != "" {
			var err error
			staleWindow, err = time.ParseDuration(raw)
			if err != nil || staleWindow <= 0 || staleWindow > sessionstate.DefaultStaleWindow {
				log.Fatal("SESSION_SYNC_STALE_WINDOW must be positive and at most 10m")
			}
		}
		srv.sessionSync = &gatewaySessionSync{
			server:  sessionstate.Server{Name: srv.serverName, Namespace: srv.serverNamespace, UID: strings.TrimSpace(os.Getenv("MCP_SERVER_UID"))},
			baseURL: baseURL, token: newTokenFileSource(strings.TrimSpace(os.Getenv("SESSION_SYNC_TOKEN_FILE"))),
			client:      &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
			staleWindow: staleWindow,
		}
		if srv.sessionSync.server.UID == "" {
			log.Fatal("MCP_SERVER_UID is required for session sync")
		}
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "mcp_gateway_session_sync_age_seconds", Help: "Age of the last complete session sync; -1 before the first sync."}, func() float64 {
			srv.sessionSync.mu.RLock()
			defer srv.sessionSync.mu.RUnlock()
			if srv.sessionSync.lastSync.IsZero() {
				return -1
			}
			return time.Since(srv.sessionSync.lastSync).Seconds()
		}))
	}

	mux := http.NewServeMux()
	// Liveness: always OK while the process is serving.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Readiness: OK only after a valid policy snapshot has been activated.
	mux.HandleFunc("/ready", srv.handleReady)
	// Sanitized applied-policy metadata (schema version, revision, reload state).
	mux.HandleFunc("/config/status", srv.handleConfigStatus)
	// Prometheus metrics, including policy reload observability.
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", srv.handleGateway)

	metricsShutdown, metricsErrs := serviceutil.StartMetricsServer(metricsPort)

	shutdown, err := initTracer("mcp-gateway")
	if err != nil {
		log.Printf("otel init failed: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}()
	}

	log.Printf("mcp-gateway listening on :%s -> %s (metrics on :%s)", port, upstream, metricsPort)
	handler := otelhttp.NewHandler(mux, "http.server", otelhttp.WithFilter(serviceutil.TraceableRequest))
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	tlsCertFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	tlsKeyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	tlsClientCAFile := strings.TrimSpace(os.Getenv("TLS_CLIENT_CA_FILE"))
	var tlsReloader *certReloader
	var trustReloader *clientCAReloader
	if tlsCertFile != "" || tlsKeyFile != "" || tlsClientCAFile != "" {
		tlsConfig, clientTrust, err := gatewayTLSConfig(tlsClientCAFile)
		if err != nil {
			log.Fatalf("configure gateway mTLS: %v", err)
		}
		trustReloader = clientTrust
		if tlsCertFile == "" || tlsKeyFile == "" {
			log.Fatal("configure gateway mTLS: TLS_CERT_FILE and TLS_KEY_FILE are required")
		}
		// Serve the certificate via GetCertificate so cert-manager rotations are
		// picked up without a restart (see certReloader).
		tlsReloader, err = newCertReloader(tlsCertFile, tlsKeyFile)
		if err != nil {
			log.Fatalf("load gateway TLS certificate: %v", err)
		}
		tlsConfig.GetCertificate = tlsReloader.GetCertificate
		httpServer.TLSConfig = tlsConfig
	}

	serverErrs := make(chan error, 2)
	go func() {
		if err, ok := <-metricsErrs; ok {
			serverErrs <- err
		}
	}()
	go func() {
		var err error
		if httpServer.TLSConfig != nil {
			// Cert/key come from TLSConfig.GetCertificate (the reloader), so the
			// filename args are empty.
			err = httpServer.ListenAndServeTLS("", "")
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrs <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if srv.sessionSync != nil {
		go srv.sessionSync.run(ctx)
	}
	if tlsReloader != nil {
		go tlsReloader.watch(ctx, defaultCertReloadInterval)
	}
	if trustReloader != nil {
		go trustReloader.watch(ctx, defaultCertReloadInterval)
	}

	select {
	case err := <-serverErrs:
		srv.stopAnalyticsDispatcher()
		log.Fatalf("server failed: %v", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			srv.stopAnalyticsDispatcher()
			log.Fatalf("server shutdown failed: %v", err)
		}
		if err := metricsShutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srv.stopAnalyticsDispatcher()
			log.Fatalf("metrics shutdown failed: %v", err)
		}
		srv.stopAnalyticsDispatcher()
	}
}

// initTracer initializes OpenTelemetry tracing for the service.
func initTracer(serviceName string) (func(context.Context) error, error) {
	return serviceutil.InitTracer(serviceName)
}

type gatewayHTTPClients struct {
	analytics, oauth         *http.Client
	analyticsBase, oauthBase *http.Transport
}

// newGatewayHTTPClients builds one client per trust domain. The analytics
// client pins the internal ingest CA; the OAuth client talks to issuers and
// JWKS endpoints, which are usually public HTTPS hosts, so it keeps the system
// roots. Sharing one transport made public issuers fail certificate checks.
func newGatewayHTTPClients(analyticsTLS *tls.Config) gatewayHTTPClients {
	newBase := func(cfg *tls.Config) *http.Transport {
		return &http.Transport{
			TLSClientConfig:     cfg,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	newClient := func(base *http.Transport) *http.Client {
		return &http.Client{
			Timeout:       3 * time.Second,
			Transport:     otelhttp.NewTransport(base),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	analyticsBase, oauthBase := newBase(analyticsTLS), newBase(nil)
	return gatewayHTTPClients{
		analytics: newClient(analyticsBase), analyticsBase: analyticsBase,
		oauth: newClient(oauthBase), oauthBase: oauthBase,
	}
}
