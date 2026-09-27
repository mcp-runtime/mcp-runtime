package svcboot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"mcp-runtime/pkg/serviceutil"
)

// Config drives the shared HTTP + metrics + OTEL bootstrap for split API services.
type Config struct {
	ServiceName string
	Port        string
	MetricsPort string
	Handler     http.Handler
	OnShutdown  func(context.Context) error
}

// Default per-connection timeouts for split API services. Routes that accept
// large uploads or run long operations (for example the runtime-api registry
// push) extend their own deadlines with http.ResponseController instead of
// raising these for every route.
const (
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 15 * time.Second
	DefaultWriteTimeout      = 15 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
)

// NewHTTPServer returns the service HTTP server with the shared default
// timeouts.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
	}
}

// Run starts metrics, OTEL-instrumented HTTP, and blocks until shutdown.
func Run(cfg Config) error {
	if cfg.Handler == nil {
		return errors.New("svcboot: handler is required")
	}
	port := cfg.Port
	if port == "" {
		port = "8080"
	}
	metricsPort := cfg.MetricsPort
	if metricsPort == "" {
		metricsPort = "9090"
	}
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "mcp-service"
	}

	shutdown, err := serviceutil.InitTracer(serviceName)
	if err != nil {
		log.Printf("otel init failed: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}()
	}

	metricsShutdown, metricsErrs := serviceutil.StartMetricsServer(metricsPort)
	log.Printf("%s listening on :%s", serviceName, port)

	requestMetrics := serviceutil.DefaultRequestMetrics()
	handler := otelhttp.NewHandler(serviceutil.LogRequests(requestMetrics.Middleware(serviceName, cfg.Handler)), "http.server")
	httpServer := NewHTTPServer(":"+port, handler)

	shutdownSignals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serverErrs := make(chan error, 2)
	go func() {
		if err, ok := <-metricsErrs; ok {
			serverErrs <- fmt.Errorf("metrics server failed: %w", err)
		}
	}()
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrs <- fmt.Errorf("http server failed: %w", err)
		}
	}()

	select {
	case <-shutdownSignals.Done():
		log.Printf("shutdown signal received")
	case err := <-serverErrs:
		log.Printf("%v", err)
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http shutdown error: %v", err)
	}
	if err := metricsShutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("metrics shutdown error: %v", err)
	}
	if cfg.OnShutdown != nil {
		if err := cfg.OnShutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// APIKeySet parses comma-separated API keys into a lookup set.
func APIKeySet(envValue string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range strings.Split(envValue, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}
