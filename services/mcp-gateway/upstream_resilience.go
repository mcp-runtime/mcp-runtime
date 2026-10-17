package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type upstreamOutcomeKey struct{}
type upstreamOutcome struct {
	mu     sync.Mutex
	reason string
	status int
	reject bool
}

func markUpstreamOutcome(r *http.Request, status int, reason string, reject bool) {
	if outcome, ok := r.Context().Value(upstreamOutcomeKey{}).(*upstreamOutcome); ok {
		outcome.mu.Lock()
		outcome.status, outcome.reason, outcome.reject = status, reason, reject
		outcome.mu.Unlock()
	}
}

type upstreamGateError struct {
	status int
	reason string
}

func (e upstreamGateError) Error() string { return e.reason }

type breakerLease struct {
	generation uint64
	probe      bool
}
type upstreamBreaker struct {
	mu         sync.Mutex
	failures   int
	threshold  int
	cooldown   time.Duration
	openUntil  time.Time
	probing    bool
	generation uint64
	now        func() time.Time
}

func (b *upstreamBreaker) acquire() (breakerLease, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.openUntil.IsZero() {
		if b.now().Before(b.openUntil) || b.probing {
			return breakerLease{}, false
		}
		b.probing = true
		return breakerLease{b.generation, true}, true
	}
	return breakerLease{generation: b.generation}, true
}

// outcome is -1 for a failure, 0 for cancellation, 1 for success. Completions
// from requests admitted before a breaker transition cannot close a newer one.
func (b *upstreamBreaker) complete(lease breakerLease, outcome int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if lease.generation != b.generation {
		return
	}
	if lease.probe {
		b.probing = false
		b.generation++
		if outcome == 1 {
			b.openUntil = time.Time{}
			b.failures = 0
		} else {
			b.openUntil = b.now().Add(b.cooldown)
		}
		return
	}
	if !b.openUntil.IsZero() {
		return
	}
	if outcome == 1 {
		b.failures = 0
	}
	if outcome == -1 {
		b.failures++
		if b.failures >= b.threshold {
			b.openUntil = b.now().Add(b.cooldown)
			b.generation++
		}
	}
}

type resilientUpstreamTransport struct {
	base         http.RoundTripper
	unaryTimeout time.Duration
	unary        chan struct{}
	streams      chan struct{}
	breaker      *upstreamBreaker
}

func newResilientUpstreamTransport(base http.RoundTripper, timeout time.Duration, unaryLimit, streamLimit, failures int, cooldown time.Duration) *resilientUpstreamTransport {
	return &resilientUpstreamTransport{base: base, unaryTimeout: timeout, unary: make(chan struct{}, unaryLimit), streams: make(chan struct{}, streamLimit), breaker: &upstreamBreaker{threshold: failures, cooldown: cooldown, now: time.Now}}
}

func (t *resilientUpstreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	lease, ok := t.breaker.acquire()
	if !ok {
		return nil, upstreamGateError{http.StatusServiceUnavailable, "upstream_unavailable"}
	}
	// Reserve capacity before dispatch: returning 429 after a tool has already
	// executed would invite an unsafe retry. GET session streams/upgrades have
	// their own budget; a POST remains an active RPC even if it responds in SSE.
	permit := t.unary
	if r.Method == http.MethodGet || r.Header.Get("Upgrade") != "" {
		permit = t.streams
	}
	select {
	case permit <- struct{}{}:
	default:
		t.breaker.complete(lease, 0)
		return nil, upstreamGateError{http.StatusTooManyRequests, "upstream_backpressure"}
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	timer := time.AfterFunc(t.unaryTimeout, func() { cancel(context.DeadlineExceeded) })
	var once sync.Once
	complete := func(result int) { once.Do(func() { t.breaker.complete(lease, result) }) }
	cleanup := func() { timer.Stop(); cancel(nil); <-permit }
	response, err := t.base.RoundTrip(r.WithContext(ctx))
	if err != nil {
		result := -1
		if r.Context().Err() != nil {
			result = 0
		}
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		complete(result)
		cleanup()
		return nil, err
	}
	stream := response.StatusCode == http.StatusSwitchingProtocols || strings.EqualFold(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]), "text/event-stream")
	if stream {
		// MCP tool calls advertise both JSON and SSE in Accept. Only the actual
		// response Content-Type can decide whether a read deadline is safe.
		timer.Stop()
	}
	if response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout {
		complete(-1)
	} else if stream {
		complete(1)
	}
	originalBody := response.Body
	body := &upstreamBody{ReadCloser: originalBody, readResult: func(err error) error {
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			markUpstreamOutcome(r, http.StatusGatewayTimeout, "upstream_timeout", false)
			complete(-1)
			return context.DeadlineExceeded
		}
		if err == io.EOF {
			complete(1)
		} else if err != nil {
			if ctx.Err() != nil {
				complete(0)
			} else {
				complete(-1)
			}
		}
		return err
	}, beforeClose: func() { complete(0); timer.Stop(); cancel(nil) }, afterClose: func() { <-permit }}
	response.Body = body
	if duplex, ok := originalBody.(io.ReadWriteCloser); ok {
		// ReverseProxy requires the writer interface for existing HTTP upgrades.
		response.Body = &upstreamDuplexBody{upstreamBody: body, writer: duplex}
	}
	return response, nil
}

type upstreamDuplexBody struct {
	*upstreamBody
	writer io.Writer
}

func (b *upstreamDuplexBody) Write(p []byte) (int, error) { return b.writer.Write(p) }

type upstreamBody struct {
	io.ReadCloser
	readResult  func(error) error
	beforeClose func()
	afterClose  func()
	once        sync.Once
	closeErr    error
}

func (b *upstreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		err = b.readResult(err)
	}
	return n, err
}
func (b *upstreamBody) Close() error {
	b.once.Do(func() {
		b.beforeClose()
		b.closeErr = b.ReadCloser.Close()
		b.afterClose()
	})
	return b.closeErr
}
