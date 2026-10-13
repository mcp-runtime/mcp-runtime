package serviceutil

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// TracedTransport propagates the current W3C context and records connection
// phases without capturing addresses, credentials, headers or payloads.
func TracedTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*otelhttp.Transport); ok {
		return base
	}
	return otelhttp.NewTransport(base, otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
		span := trace.SpanFromContext(ctx)
		event := func(name string, failed bool) {
			span.AddEvent(name, trace.WithAttributes(attribute.Bool("failed", failed)))
		}
		return &httptrace.ClientTrace{
			DNSStart:          func(httptrace.DNSStartInfo) { event("dns.start", false) },
			DNSDone:           func(info httptrace.DNSDoneInfo) { event("dns.end", info.Err != nil) },
			ConnectStart:      func(string, string) { event("connect.start", false) },
			ConnectDone:       func(_, _ string, err error) { event("connect.end", err != nil) },
			TLSHandshakeStart: func() { event("tls.start", false) },
			TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { event("tls.end", err != nil) },
			GotConn: func(info httptrace.GotConnInfo) {
				span.AddEvent("connection.acquired", trace.WithAttributes(attribute.Bool("connection.reused", info.Reused)))
			},
			GotFirstResponseByte: func() { event("response.first_byte", false) },
		}
	}))
}

var tracedDefaultTransport = TracedTransport(nil)

// DoHTTP preserves the caller's timeouts, redirects and cookie jar. It does not
// mutate shared clients or transports; the underlying connection pool is reused.
func DoHTTP(client *http.Client, request *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	if client.Transport == nil {
		copyClient.Transport = tracedDefaultTransport
	} else {
		copyClient.Transport = TracedTransport(client.Transport)
	}
	// #nosec G704 -- callers retain destination validation; this wrapper only instruments their existing request.
	return copyClient.Do(request)
}
