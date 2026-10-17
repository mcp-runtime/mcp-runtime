package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	cb "mcp-runtime/pkg/credentialbroker"
)

func TestReservedCredentialTrailersCannotCrossEitherProxyBoundary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Error(err)
		}
		if r.Trailer.Get(cb.HeaderPrefix+"Caller") != "" {
			t.Error("caller credential trailer reached workload")
		}
		if r.Trailer.Get("X-Safe-Request-Trailer") != "safe-request" {
			t.Error("unrelated request trailer lost")
		}
		w.Header().Set("Trailer", "X-Safe-Response-Trailer")
		if r.URL.Query().Get("late") == "" {
			w.Header().Add("Trailer", cb.HeaderPrefix+"Token")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
		w.Header().Set("X-Safe-Response-Trailer", "safe-response")
		if r.URL.Query().Get("late") == "" {
			w.Header().Set(cb.HeaderPrefix+"Token", "provider-value-must-not-escape")
		} else {
			w.Header().Set(http.TrailerPrefix+cb.HeaderPrefix+"Late", "provider-value-must-not-escape")
		}
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newUpstreamReverseProxy(target)
	front := httptest.NewServer(proxy)
	defer front.Close()
	for _, suffix := range []string{"", "?late=1"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, front.URL+"/mcp"+suffix, strings.NewReader(`{"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.ContentLength = -1
		request.TransferEncoding = []string{"chunked"}
		request.Trailer = http.Header{cb.HeaderPrefix + "Caller": []string{"forged"}, "X-Safe-Request-Trailer": []string{"safe-request"}}
		response, err := front.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || strings.Contains(string(body), "provider-value") {
			t.Fatal("unexpected proxy response")
		}
		for name := range response.Trailer {
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(cb.HeaderPrefix)) {
				t.Fatal("reserved response trailer escaped")
			}
		}
		if response.Trailer.Get("X-Safe-Response-Trailer") != "safe-response" {
			t.Fatal("unrelated response trailer lost")
		}
	}
}
