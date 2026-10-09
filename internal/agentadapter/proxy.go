package agentadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	proxyReadHeaderTimeout = 5 * time.Second
	proxyShutdownTimeout   = 10 * time.Second
	// DefaultMaxInboundBytes caps the size of inbound JSON-RPC bodies that
	// the proxy buffers for metadata capture. Requests over the cap get a
	// 413 with a JSON-RPC parse-error body so the agent SDK can recover.
	DefaultMaxInboundBytes int64 = 16 << 20
)

type rpcRequestMetadataContextKey struct{}

// NewHTTPProxyHandler returns a reverse proxy that forwards MCP HTTP traffic
// to the configured runtime route. Session identity is carried by the TLS
// client certificate (and OAuth bearer when the target enables it), not by
// request headers.
func NewHTTPProxyHandler(cfg ProxyConfig) (http.Handler, error) {
	h, _, err := newProxyHandlerAndTracker(cfg)
	return h, err
}

// newProxyHandlerAndTracker builds the proxy handler together with the
// requestTracker that tracks every in-flight request. RunHTTPProxy calls this
// so it can cancel all tracked requests before draining the server.
func newProxyHandlerAndTracker(cfg ProxyConfig) (http.Handler, *requestTracker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	target := cloneURL(cfg.RuntimeURL)
	transport := cfg.transportOrDefault()
	if cfg.AuthMode == AuthModeHeader || len(cfg.CredentialHeaders) > 0 {
		transport = transport.withCredentialHeaders(target, cfg.CredentialHeaders)
	}
	logLevel := cfg.LogLevel
	logWriter := cfg.LogWriter
	hostHeader := cfg.HostHeader
	disableXFF := cfg.DisableXForwarded

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Transport:     transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			rewriteToRuntimeRoute(req.Out.URL, target, req.In.URL.RawQuery)
			req.Out.Host = target.Host
			if hostHeader != "" {
				req.Out.Host = hostHeader
			}
			if !disableXFF {
				req.SetXForwarded()
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < http.StatusBadRequest || resp.StatusCode >= http.StatusInternalServerError {
				return nil
			}
			// Delegated upstream error bodies are opaque and may contain secrets.
			// Preserve them for the client without parsing or logging their text.
			if cfg.AuthMode == AuthModeHeader || len(cfg.CredentialHeaders) > 0 {
				meta := rpcRequestMetadataFromContext(resp.Request.Context())
				logRuntimeDenial(logLevel, logWriter, "adapter/proxy", resp.StatusCode, "upstream request denied", meta)
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if isSessionExpiredBody(body) {
				body = injectRuntimeStatus(body, "session_expired")
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
			meta := rpcRequestMetadataFromContext(resp.Request.Context())
			logRuntimeDenial(logLevel, logWriter, "adapter/proxy", resp.StatusCode, extractHTTPErrorMessage(resp.StatusCode, body), meta)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			meta := rpcRequestMetadataFromContext(r.Context())
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			message := err.Error()
			if cfg.AuthMode == AuthModeHeader || len(cfg.CredentialHeaders) > 0 {
				message = "adapter could not forward request; check credential sources and client header conflicts"
			}
			_, _ = w.Write(jsonRPCHTTPError(rpcIDOrNull(meta), http.StatusBadGateway, message, nil))
		},
	}

	maxInbound := cfg.MaxInboundBytes
	if maxInbound <= 0 {
		maxInbound = DefaultMaxInboundBytes
	}
	metricsHandler := cfg.MetricsHandler

	tracker := newRequestTracker()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/livez":
			w.WriteHeader(http.StatusNoContent)
			return
		case "/readyz":
			w.WriteHeader(http.StatusNoContent)
			return
		case "/metrics":
			if metricsHandler == nil {
				http.Error(w, "metrics handler not configured", http.StatusNotFound)
				return
			}
			metricsHandler.ServeHTTP(w, r)
			return
		}
		// Track the request so shutdown can cancel it explicitly.
		reqCtx, id := tracker.track(r.Context())
		defer tracker.done(id)

		meta, err := captureRPCRequestMetadata(r, maxInbound)
		if err != nil {
			if errors.Is(err, errInboundBodyTooLarge) {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write(jsonRPCParseError("request body exceeds adapter limit"))
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := context.WithValue(reqCtx, rpcRequestMetadataContextKey{}, meta)
		ctx = withRPCMethod(ctx, meta.Method)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
	return handler, tracker, nil
}

// RunHTTPProxy serves the local HTTP adapter until the context is cancelled.
func RunHTTPProxy(ctx context.Context, cfg ProxyConfig) error {
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	handler, tracker, err := newProxyHandlerAndTracker(cfg)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: proxyReadHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		// Cancel all in-flight runtime requests so client.Do returns quickly,
		// then drain the HTTP server. Fall back to Close if Shutdown times out.
		tracker.cancelAll(errors.New("adapter shutdown"))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), proxyShutdownTimeout)
		defer cancel()
		if sErr := server.Shutdown(shutdownCtx); sErr != nil {
			_ = server.Close()
		}
		svrErr := <-errCh
		if errors.Is(svrErr, http.ErrServerClosed) {
			return nil
		}
		return svrErr
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func rewriteToRuntimeRoute(out, target *url.URL, inboundRawQuery string) {
	out.Scheme = target.Scheme
	out.Host = target.Host
	out.Path = target.Path
	out.RawPath = target.RawPath
	out.OmitHost = false
	out.ForceQuery = false
	out.Fragment = ""
	out.RawQuery = mergeRawQuery(target.RawQuery, inboundRawQuery)
}

func mergeRawQuery(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "&" + second
	}
}

// errInboundBodyTooLarge signals that the inbound JSON-RPC body exceeded the
// configured cap. Callers translate it to HTTP 413.
var errInboundBodyTooLarge = errors.New("inbound body exceeds configured limit")

func captureRPCRequestMetadata(r *http.Request, maxBytes int64) (rpcRequestMetadata, error) {
	if r.Body == nil {
		return rpcRequestMetadata{}, nil
	}
	// Read up to maxBytes+1 so we can distinguish "exactly at cap" from
	// "over cap" without buffering an arbitrary amount. Guard against
	// integer overflow when the caller configures math.MaxInt64.
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return rpcRequestMetadata{}, err
	}
	_ = r.Body.Close()
	if int64(len(body)) > maxBytes {
		return rpcRequestMetadata{}, errInboundBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return parseRPCRequestMetadata(bytes.TrimSpace(body)), nil
}

func rpcRequestMetadataFromContext(ctx context.Context) rpcRequestMetadata {
	meta, _ := ctx.Value(rpcRequestMetadataContextKey{}).(rpcRequestMetadata)
	return meta
}
