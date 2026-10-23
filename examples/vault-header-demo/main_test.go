package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeaderAuthenticationOnValidationAndMCP(t *testing.T) {
	h := newHandler("/vault-header-demo/mcp")
	for _, path := range []string{"/vault-header-demo/mcp", "/vault-header-demo/mcp/auth/check"} {
		for _, valid := range []bool{false, true} {
			req := httptest.NewRequest("POST", path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Content-Type", "application/json")
			if strings.HasSuffix(path, "/auth/check") {
				req.Method = http.MethodGet
			}
			if valid {
				req.SetBasicAuth("demo-user", "demo-password")
			} else {
				req.SetBasicAuth("demo-user", "wrong")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if !valid && w.Code != 401 {
				t.Fatalf("bad header accepted: %s %d", path, w.Code)
			}
			if valid && (w.Code < 200 || w.Code >= 300) {
				t.Fatalf("valid header refused: %s %d", path, w.Code)
			}
			if strings.Contains(w.Body.String(), "demo-password") {
				t.Fatal("credential leaked")
			}
		}
	}
}
