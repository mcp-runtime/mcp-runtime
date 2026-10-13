package serviceutil

import (
	"bufio"
	"context"
	"net"
	"net/http"
)

// TraceResponseHeader exposes the active span's trace ID, never a caller's
// arbitrary correlation header. It is an observation identifier, not identity.
const TraceResponseHeader = "X-Trace-ID"

// CorrelateResponse sets the trace ID before headers are committed, including
// proxy responses. Unwrap preserves ResponseController operations and streaming.
func CorrelateResponse(w http.ResponseWriter, ctx context.Context) http.ResponseWriter {
	writer := &correlationWriter{ResponseWriter: w, traceID: TraceIDFromContext(ctx)}
	writer.correlate()
	return writer
}

type correlationWriter struct {
	http.ResponseWriter
	traceID string
}

func (w *correlationWriter) correlate() {
	w.Header().Del(TraceResponseHeader)
	if w.traceID != "" {
		w.Header().Set(TraceResponseHeader, w.traceID)
	}
}

func (w *correlationWriter) WriteHeader(status int) {
	w.correlate()
	w.ResponseWriter.WriteHeader(status)
}

func (w *correlationWriter) Write(body []byte) (int, error) {
	w.correlate()
	return w.ResponseWriter.Write(body)
}

func (w *correlationWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *correlationWriter) Flush() {
	w.correlate()
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *correlationWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
