// Package serviceutil provides HTTP utilities for MCP services.
package serviceutil

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// WriteJSON writes a JSON response with the specified status code.
// It sets appropriate Content-Type headers and handles JSON marshaling errors.
// It first marshals the payload to check for encoding errors before writing headers.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to encode response"})
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// StartMetricsServer starts a Prometheus metrics server with /metrics and /health.
func StartMetricsServer(port string) (func(context.Context) error, <-chan error) {
	metricsServer := &http.Server{
		Addr:              listenAddr(port),
		Handler:           metricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	return metricsServer.Shutdown, errs
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func listenAddr(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ":0"
	}
	if strings.HasPrefix(port, ":") || strings.Contains(port, ":") {
		return port
	}
	return ":" + port
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
	r.wroteHeader = true
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func (r *statusRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LogRequests logs HTTP method, path, status, and duration for each request.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if failureStatus(recorder.status) {
			RecordSpanFailure(r.Context(), r.Method+" "+r.URL.Path, http.StatusText(recorder.status), recorder.status, nil)
		}
		// #nosec G706 -- request method/path are operational logs.
		log.Printf("%s %s %d %s%s", r.Method, r.URL.Path, recorder.status, time.Since(start), TraceLogSuffix(r.Context()))
	})
}
