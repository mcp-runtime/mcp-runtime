package agentadapter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestRuntimeTransportRoutesThroughBase(t *testing.T) {
	t.Parallel()

	var called bool
	transport := &RuntimeTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			return &http.Response{StatusCode: http.StatusTeapot, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodGet, "http://example/test", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if !called {
		t.Fatal("base round-tripper was not invoked")
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
}

func TestRuntimeTransportClientAppliesTimeout(t *testing.T) {
	t.Parallel()

	transport := &RuntimeTransport{Timeout: 7 * time.Second}
	client := transport.Client()
	if client.Timeout != 7*time.Second {
		t.Fatalf("client.Timeout = %s, want 7s", client.Timeout)
	}
	if client.Transport != transport {
		t.Fatalf("client.Transport = %#v, want transport", client.Transport)
	}
}

func TestRuntimeTransportNilBaseUsesDefault(t *testing.T) {
	t.Parallel()

	transport := &RuntimeTransport{}
	if got := transport.base(); got != http.DefaultTransport {
		t.Fatalf("base() = %#v, want http.DefaultTransport", got)
	}
}

func TestRuntimeTransportNilReceiverClientDoesNotPanic(t *testing.T) {
	t.Parallel()

	var transport *RuntimeTransport
	client := transport.Client()
	if client == nil {
		t.Fatal("Client() returned nil")
	}
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %s, want 0 for nil receiver", client.Timeout)
	}
}

func TestRuntimeTransportInjectsAuthHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	transport := &RuntimeTransport{
		AuthHeader: "Bearer test-token",
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "http://example/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization header = %q, want Bearer test-token", gotAuth)
	}
}

func TestRuntimeTransportAuthHeaderDoesNotMutateOriginalRequest(t *testing.T) {
	t.Parallel()

	transport := &RuntimeTransport{
		AuthHeader: "Bearer secret",
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
		}),
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example/mcp", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("original request Authorization = %q, want empty (must not mutate caller)", got)
	}
}

func TestRuntimeTransportRetriesRetryableMethodOnBadGateway(t *testing.T) {
	t.Parallel()

	attempts := 0
	transport := &RuntimeTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts < retryMaxAttempts {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     http.Header{},
					Request:    req,
				}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
		}),
	}

	ctx := withRPCMethod(context.Background(), "tools/list")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example/mcp", strings.NewReader(`{}`))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d after retry", resp.StatusCode, http.StatusOK)
	}
	if attempts != retryMaxAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, retryMaxAttempts)
	}
}

func TestRuntimeTransportDoesNotRetryNonRetryableMethod(t *testing.T) {
	t.Parallel()

	attempts := 0
	transport := &RuntimeTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
				Request:    req,
			}, nil
		}),
	}

	ctx := withRPCMethod(context.Background(), "tools/call")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example/mcp", strings.NewReader(`{}`))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (no retry for tools/call)", resp.StatusCode, http.StatusBadGateway)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry for tools/call)", attempts)
	}
}

func TestRuntimeTransportNilRespDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Misbehaving base: returns (nil, nil), violating the RoundTripper
	// contract. The transport must not panic.
	transport := &RuntimeTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, nil
		}),
	}

	ctx := withRPCMethod(context.Background(), "tools/list")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example/mcp", strings.NewReader(`{}`))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil for nil-returning base", resp)
	}
}

func TestRetryBackoffCapsWithoutShiftConversion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: retryBaseDelay},
		{attempt: 1, want: retryBaseDelay},
		{attempt: 2, want: 2 * retryBaseDelay},
		{attempt: 20, want: retryMaxDelay},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.want.String(), func(t *testing.T) {
			t.Parallel()
			if got := retryBackoff(tt.attempt); got != tt.want {
				t.Fatalf("retryBackoff(%d) = %s, want %s", tt.attempt, got, tt.want)
			}
		})
	}
}
