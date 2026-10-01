package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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

	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/serviceutil"
)

func main() {
	port := serviceutil.EnvOr("PORT", strconv.Itoa(mcpdefaults.MCPGatewayPort))
	metricsPort := serviceutil.EnvOr("METRICS_PORT", "9103")
	upstream := serviceutil.EnvOr("UPSTREAM_URL", "http://127.0.0.1:8090")
	analyticsURL := strings.TrimSpace(os.Getenv("ANALYTICS_INGEST_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ANALYTICS_API_KEY"))
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

	analyticsTransport := otelhttp.NewTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})

	sharedClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: analyticsTransport,
	}

	srv := &gatewayServer{
		proxy:                  proxy,
		metrics:                newGatewayMetrics(prometheus.DefaultRegisterer),
		analyticsURL:           analyticsURL,
		apiKey:                 apiKey,
		source:                 source,
		eventType:              eventType,
		stripPrefix:            stripPrefix,
		externalBaseURL:        externalBaseURL,
		httpClient:             sharedClient,
		oauthInternalIssuerURL: strings.TrimSpace(os.Getenv("OAUTH_INTERNAL_ISSUER_URL")),
		policyFile:             strings.TrimSpace(os.Getenv("POLICY_FILE")),
		serverName:             strings.TrimSpace(os.Getenv("MCP_SERVER_NAME")),
		serverNamespace:        strings.TrimSpace(os.Getenv("MCP_SERVER_NAMESPACE")),
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
	if tlsCertFile != "" || tlsKeyFile != "" || tlsClientCAFile != "" {
		tlsConfig, err := gatewayTLSConfig(tlsClientCAFile)
		if err != nil {
			log.Fatalf("configure gateway mTLS: %v", err)
		}
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
	if tlsReloader != nil {
		go tlsReloader.watch(ctx, defaultCertReloadInterval)
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

func gatewayTLSConfig(clientCAFile string) (*tls.Config, error) {
	if clientCAFile == "" {
		return nil, errors.New("TLS_CLIENT_CA_FILE is required")
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA bundle: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("client CA bundle contains no certificates")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAs,
	}, nil
}

// initTracer initializes OpenTelemetry tracing for the service.
func initTracer(serviceName string) (func(context.Context) error, error) {
	return serviceutil.InitTracer(serviceName)
}
