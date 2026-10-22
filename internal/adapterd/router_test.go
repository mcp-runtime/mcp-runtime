package adapterd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConfiguredProfileMCPRequestFlow(t *testing.T) {
	var methods []string
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/mcp" {
			t.Errorf("upstream path = %q, want /mcp", req.URL.Path)
		}
		if got := req.Header.Get(LocalTokenHeader); got != "" {
			t.Errorf("local profile token leaked upstream: %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer oauth-test" {
			t.Errorf("upstream authorization = %q", got)
		}
		var rpc struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil {
			t.Errorf("decode JSON-RPC request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wantSession := "session-test"
		if rpc.Method == "initialize" {
			wantSession = ""
		}
		if got := req.Header.Get("Mcp-Session-Id"); got != wantSession {
			t.Errorf("%s MCP session ID = %q, want %q", rpc.Method, got, wantSession)
		}
		methods = append(methods, rpc.Method)
		if rpc.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		if rpc.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-test")
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "adapter-flow-test", "version": "1"}}
		} else if rpc.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
		} else {
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "flow-ok"}}, "isError": false}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}))
	upstream.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	upstream.StartTLS()
	defer upstream.Close()

	certPEM, keyPEM := testClientCertificate(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	d := testDaemon(t.TempDir())
	endpoint, err := url.Parse(upstream.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	profile, token, err := d.AddProfile(context.Background(), AddProfileRequest{Server: "flow-test", RuntimeURL: endpoint.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.storeMaterial(profile.ID, Material{CertPEM: certPEM, KeyPEM: keyPEM, CA: caPEM, Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := d.mount(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(d.Router)
	defer local.Close()

	steps := []struct {
		method string
		id     int
		status int
	}{
		{method: "initialize", id: 1, status: http.StatusOK},
		{method: "notifications/initialized", status: http.StatusAccepted},
		{method: "tools/list", id: 2, status: http.StatusOK},
		{method: "tools/call", id: 3, status: http.StatusOK},
	}
	for _, step := range steps {
		message := map[string]any{"jsonrpc": "2.0", "method": step.method}
		if step.id != 0 {
			message["id"] = step.id
		}
		switch step.method {
		case "initialize":
			message["params"] = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "adapter-flow-test", "version": "1"}}
		case "tools/list":
			message["params"] = map[string]any{}
		case "tools/call":
			message["params"] = map[string]any{"name": "echo", "arguments": map[string]string{"message": "flow-ok"}}
		}
		body, _ := json.Marshal(message)
		req, err := http.NewRequest(http.MethodPost, local.URL+"/"+profile.Slug+"/mcp", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer oauth-test")
		req.Header.Set(LocalTokenHeader, token)
		if step.method != "initialize" {
			req.Header.Set("Mcp-Session-Id", "session-test")
		}
		resp, err := local.Client().Do(req)
		if err != nil {
			t.Fatalf("%s request: %v", step.method, err)
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s response: %v", step.method, readErr)
		}
		if resp.StatusCode != step.status {
			t.Fatalf("%s status = %d, want %d", step.method, resp.StatusCode, step.status)
		}
		if step.method == "initialize" && resp.Header.Get("Mcp-Session-Id") != "session-test" {
			t.Fatalf("initialize session ID = %q", resp.Header.Get("Mcp-Session-Id"))
		}
		if step.method == "tools/call" && !strings.Contains(string(responseBody), "flow-ok") {
			t.Fatalf("tools/call response = %s", responseBody)
		}
	}
	if want := []string{"initialize", "notifications/initialized", "tools/list", "tools/call"}; strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("upstream methods = %v, want %v", methods, want)
	}
}

func testClientCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "adapter-flow-test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return cert, keyPEM
}

func TestRouterIsolatesProfilesByPath(t *testing.T) {
	r := NewRouter()
	var seen []string
	r.Set(Route{
		Profile: Profile{Slug: "cully", Server: "cully"},
		Token:   "cully-token",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, "cully "+req.URL.Path+" auth="+req.Header.Get("Authorization")+" local="+req.Header.Get(LocalTokenHeader))
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	r.Set(Route{
		Profile: Profile{Slug: "github", Server: "github"},
		Token:   "github-token",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, "github "+req.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/cully/mcp", strings.NewReader("{}"))
	req.Header.Set(LocalTokenHeader, "cully-token")
	req.Header.Set("Authorization", "Bearer oauth-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cully status %d", rec.Code)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "auth=Bearer oauth-token") || !strings.Contains(seen[0], "local=") || strings.Contains(seen[0], "cully-token") {
		t.Fatalf("forwarded request = %v", seen)
	}

	for _, token := range []string{"", "cully-token", "wrong-token"} {
		unauthorized := httptest.NewRequest(http.MethodPost, "/github/mcp", nil)
		if token != "" {
			unauthorized.Header.Set(LocalTokenHeader, token)
		}
		unauthorizedRec := httptest.NewRecorder()
		r.ServeHTTP(unauthorizedRec, unauthorized)
		if unauthorizedRec.Code != http.StatusUnauthorized {
			t.Fatalf("github with token %q status %d, want 401", token, unauthorizedRec.Code)
		}
	}

	valid := httptest.NewRequest(http.MethodPost, "/github/mcp", nil)
	valid.Header.Set(LocalTokenHeader, "github-token")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, valid)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("github status %d", rec.Code)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/missing/mcp", nil)
	unknown.Header.Set(LocalTokenHeader, "cully-token")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status %d", rec.Code)
	}
}

func TestUniqueSlugSuffix(t *testing.T) {
	taken := map[string]bool{"cully": true}
	if got := UniqueSlug("cully", func(s string) bool { return taken[s] }); got != "cully-2" {
		t.Fatalf("slug %s", got)
	}
	slug, err := SlugFromServer("Cully Server")
	if err != nil || slug != "cully-server" {
		t.Fatalf("slug %q err %v", slug, err)
	}
}

func TestHealthIsLocalReadinessOnly(t *testing.T) {
	r := NewRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusNoContent {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("health %d %s", rec.Code, body)
	}
}
