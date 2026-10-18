package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"sync"

	cb "mcp-runtime/pkg/credentialbroker"
)

// Trailer fields may be populated only when the HTTP body reaches EOF (or is
// drained by Close). Sanitize there, before the next hop copies the fields.
type credentialTrailerBody struct {
	io.ReadCloser
	after func()
	mu    sync.Mutex
}

func (b *credentialTrailerBody) finish() { b.mu.Lock(); defer b.mu.Unlock(); b.after() }
func (b *credentialTrailerBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish()
	}
	return n, err
}
func (b *credentialTrailerBody) Close() error { err := b.ReadCloser.Close(); b.finish(); return err }

// Preserve the upgraded full-duplex response contract.
type credentialDuplexBody struct {
	*credentialTrailerBody
	writer io.Writer
}

func (b *credentialDuplexBody) Write(p []byte) (int, error) { return b.writer.Write(p) }

func sanitizeCredentialRequestTrailers(request *httputil.ProxyRequest) {
	cb.StripHeaders(request.Out.Trailer)
	if request.Out.Body == nil || request.Out.Trailer == nil {
		return
	}
	request.Out = request.Out.WithContext(context.WithValue(request.Out.Context(), credentialTrailerSource{}, request.In))
}

type credentialTrailerSource struct{}
type credentialTrailerTransport struct{ base http.RoundTripper }

func (t credentialTrailerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Tracing and proxy layers clone Trailer maps. Bind the final filtered map
	// immediately before net/http.Transport consumes the body and writes it.
	source, _ := request.Context().Value(credentialTrailerSource{}).(*http.Request)
	if source == nil || request.Body == nil || request.Trailer == nil {
		return t.base.RoundTrip(request)
	}
	out := request.Clone(request.Context())
	cb.StripHeaders(out.Trailer)
	out.Body = &credentialTrailerBody{ReadCloser: out.Body, after: func() {
		for name, values := range source.Trailer {
			out.Trailer[name] = append([]string(nil), values...)
		}
		cb.StripHeaders(out.Trailer)
	}}
	return t.base.RoundTrip(out)
}

func sanitizeCredentialResponse(response *http.Response) error {
	cb.StripHeaders(response.Header)
	cb.StripHeaders(response.Trailer)
	if response.Body == nil {
		return nil
	}
	body := &credentialTrailerBody{ReadCloser: response.Body, after: func() { cb.StripHeaders(response.Trailer) }}
	if writer, ok := response.Body.(io.Writer); ok {
		response.Body = &credentialDuplexBody{credentialTrailerBody: body, writer: writer}
	} else {
		response.Body = body
	}
	return nil
}
