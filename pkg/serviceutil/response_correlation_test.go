package serviceutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseCorrelationOwnsHeaderAndPreservesStreaming(t *testing.T) {
	ctx, _, end := newRecordedSpan(t)
	defer end()
	for _, mode := range []string{"header", "write", "flush", "empty"} {
		t.Run(mode, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := CorrelateResponse(recorder, ctx)
			if mode != "empty" {
				writer.Header().Add(TraceResponseHeader, "untrusted-upstream-id")
			}
			switch mode {
			case "header":
				writer.WriteHeader(http.StatusForbidden)
			case "write":
				_, _ = writer.Write([]byte("ok"))
			case "flush":
				if err := http.NewResponseController(writer).Flush(); err != nil {
					t.Fatal(err)
				}
			}
			values := recorder.Result().Header.Values(TraceResponseHeader)
			if len(values) != 1 || values[0] != TraceIDFromContext(ctx) {
				t.Fatalf("header = %v, want active trace ID", values)
			}
			if mode == "flush" && !recorder.Flushed {
				t.Fatal("stream not flushed")
			}
		})
	}
}

func TestResponseCorrelationWithoutSpanDoesNotReflectHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := CorrelateResponse(recorder, context.Background())
	writer.Header().Set(TraceResponseHeader, "untrusted-id")
	writer.WriteHeader(http.StatusOK)
	if got := recorder.Result().Header.Get(TraceResponseHeader); got != "" {
		t.Fatalf("untrusted ID reflected: %q", got)
	}
}
