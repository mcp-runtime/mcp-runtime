// Credential gateway example. Public fixtures only; not a production vault.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const demoUsername = "demo-user"
const demoPassword = "demo-password" // Deliberately public test fixture.

type headerRule struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	UsernameField string `json:"username_field"`
	PasswordField string `json:"password_field"`
}
type upstreamConfig struct {
	CredentialSource string       `json:"credential_source"`
	SubjectPrefix    string       `json:"subject_prefix"`
	CredentialTTL    int          `json:"credential_ttl_seconds"`
	Headers          []headerRule `json:"headers"`
}

func defaultUpstreamConfig() upstreamConfig {
	return upstreamConfig{CredentialSource: "user-1password", SubjectPrefix: "upstream-demo:", CredentialTTL: 3600, Headers: []headerRule{{Name: "Authorization", Type: "basic", UsernameField: "username", PasswordField: "password"}}}
}
func (c upstreamConfig) validate() error {
	if c.CredentialSource != "user-1password" || c.SubjectPrefix == "" || c.CredentialTTL < 60 || c.CredentialTTL > 3600 || len(c.Headers) != 1 {
		return errors.New("invalid upstream credential configuration")
	}
	h := c.Headers[0]
	if h.Name != "Authorization" || h.Type != "basic" || h.UsernameField != "username" || h.PasswordField != "password" {
		return errors.New("this example supports one Basic Authorization header from username/password fields")
	}
	return nil
}

type login struct {
	username, password string
	expires            time.Time
}
type authorization struct {
	redirect, state, challenge, csrf, binding string
	expires                                   time.Time
	attempts                                  int
}
type providerCode struct {
	subject, challenge, redirect string
	expires                      time.Time
}
type providerToken struct {
	subject string
	expires time.Time
}
type demo struct {
	mu                                           sync.Mutex
	resource, origin, basePath, issuer, upstream string
	verifier                                     *mcpauth.JWTVerifier
	config                                       upstreamConfig
	logins                                       map[string]login
	requests                                     map[string]authorization
	codes                                        map[string]providerCode
	tokens                                       map[string]providerToken
	now                                          func() time.Time
	client                                       *http.Client
}

func newDemo(resource string, verifier *mcpauth.JWTVerifier) *demo {
	u, _ := url.Parse(resource)
	issuer := "https://auth.mcpruntime.org/mcp-auth"
	if verifier != nil && verifier.Issuer != "" {
		issuer = verifier.Issuer
	}
	return &demo{resource: resource, origin: u.Scheme + "://" + u.Host, basePath: u.Path, issuer: issuer, verifier: verifier, config: defaultUpstreamConfig(), logins: map[string]login{}, requests: map[string]authorization{}, codes: map[string]providerCode{}, tokens: map[string]providerToken{}, now: time.Now, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func opaque() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (d *demo) prune() {
	now := d.now()
	for k, v := range d.logins {
		if !now.Before(v.expires) {
			delete(d.logins, k)
		}
	}
	for k, v := range d.requests {
		if !now.Before(v.expires) {
			delete(d.requests, k)
		}
	}
	for k, v := range d.codes {
		if !now.Before(v.expires) {
			delete(d.codes, k)
		}
	}
	for k, v := range d.tokens {
		if !now.Before(v.expires) {
			delete(d.tokens, k)
		}
	}
}
func securePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

// validateUpstream tests the submitted credentials against the fixed service,
// before any upstream identity/code or MCP OAuth token can be issued.
func (d *demo) validateUpstream(ctx context.Context, username, password string) error {
	if username == "" || password == "" || len(username) > 128 || len(password) > 1024 || strings.ContainsAny(username+password, "\r\n\x00") {
		return errors.New("upstream rejected credentials")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.upstream+"/auth/check", nil)
	if err != nil {
		return errors.New("upstream unavailable")
	}
	req.SetBasicAuth(username, password)
	resp, err := d.client.Do(req)
	if err != nil {
		return errors.New("upstream unavailable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return errors.New("upstream rejected credentials")
	}
	return nil
}

func (d *demo) handler() http.Handler {
	mux := http.NewServeMux()
	u, _ := url.Parse(d.resource)
	target, _ := url.Parse(d.upstream)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "upstream unavailable", 503) }
	// The selected credential is attached only after validating the MCP token.
	type credentialKey struct{}
	proxy.Director = func(r *http.Request) {
		director(r)
		r.URL.Path = "/mcp"
		r.URL.RawPath = ""
		r.URL.RawQuery = ""
		r.Host = target.Host
		r.Header.Del("Authorization")
		r.Header.Del("Cookie")
		r.Header.Del("Proxy-Authorization")
		credential := r.Context().Value(credentialKey{}).(login)
		r.SetBasicAuth(credential.username, credential.password)
	}
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := mcpauth.TokenClaimsFromContext(r.Context())
		if !ok || !strings.HasPrefix(claims.Subject, d.config.SubjectPrefix) {
			http.Error(w, "upstream login required", 403)
			return
		}
		subject := strings.TrimPrefix(claims.Subject, d.config.SubjectPrefix)
		d.mu.Lock()
		d.prune()
		credential, found := d.logins[subject]
		d.mu.Unlock()
		if !found {
			http.Error(w, "upstream credential session expired; sign in again", 403)
			return
		}
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), credentialKey{}, credential)))
	})
	metadataPath := "/.well-known/oauth-protected-resource" + u.Path
	mux.Handle(u.Path, mcpauth.RequireToken(d.verifier, mcpauth.ResourceMetadata{URL: d.origin + metadataPath}, protected))
	mux.HandleFunc(metadataPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": d.resource, "authorization_servers": []string{d.issuer}, "scopes_supported": []string{"tools:read"}, "bearer_methods_supported": []string{"header"}})
	})
	mux.HandleFunc(d.basePath+"/login/authorize", d.authorize)
	mux.HandleFunc(d.basePath+"/login/approve", d.approve)
	mux.HandleFunc(d.basePath+"/login/token", d.providerToken)
	mux.HandleFunc(d.basePath+"/login/userinfo", d.userinfo)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) })
	return mux
}

func testUpstreamHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "header-authenticated-upstream", Version: "0.2.0"}, nil)
	type emptyArgs struct{}
	mcp.AddTool(server, &mcp.Tool{Name: "upstream_status", Description: "Verify the upstream MCP server received its required Basic header. Returns no credential values."}, func(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"authenticated":true,"service":"header-authenticated-upstream","oauth_token_forwarded":false}`}}}, nil, nil
	})
	type echoArgs struct {
		Message string `json:"message"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a message after upstream header authentication."}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Message}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != demoUsername || p != demoPassword {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/auth/check" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"subject":"demo-user"}`)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func run() error {
	resource := os.Getenv("MCP_AUTH_RESOURCE")
	if resource == "" {
		resource = "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp"
	}
	u, err := url.Parse(resource)
	localTest := os.Getenv("DEMO_LOCAL_TEST") == "true"
	localHTTP := err == nil && localTest && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")
	if err != nil || (u.Scheme != "https" && !localHTTP) || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || !strings.HasSuffix(u.Path, "/mcp") {
		return errors.New("MCP_AUTH_RESOURCE must be an HTTPS /mcp URL")
	}
	issuer := os.Getenv("MCP_AUTH_ISSUER")
	if issuer == "" {
		issuer = "https://auth.mcpruntime.org/mcp-auth"
	}
	issuerURL, err := url.Parse(issuer)
	issuerLocal := err == nil && localTest && issuerURL.Scheme == "http" && (issuerURL.Hostname() == "127.0.0.1" || issuerURL.Hostname() == "localhost")
	if err != nil || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.Fragment != "" || issuerURL.RawQuery != "" || (issuerURL.Scheme != "https" && !issuerLocal) {
		return errors.New("MCP_AUTH_ISSUER must be HTTPS outside explicit loopback testing")
	}
	metadata, err := mcpauth.DiscoverAuthorizationServer(&http.Client{Timeout: 10 * time.Second}, issuer)
	if err != nil {
		return errors.New("authorization server discovery failed")
	}
	verifier := &mcpauth.JWTVerifier{Issuer: issuer, JWKSURL: metadata.JWKSURI, Audience: resource, RequiredScopes: map[string]bool{"tools:read": true}, TokenTypes: []string{"at+jwt"}, RequireType: true}
	d := newDemo(resource, verifier)
	configFile := os.Getenv("UPSTREAM_AUTH_CONFIG")
	if configFile == "" {
		configFile = "/upstream-auth.json"
	}
	if data, err := os.ReadFile(configFile); err == nil {
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&d.config); err != nil {
			return errors.New("invalid upstream auth configuration")
		}
	} else {
		return errors.New("upstream auth configuration is required")
	}
	if err := d.config.validate(); err != nil {
		return err
	}
	upstreamAddress := "127.0.0.1:9099"
	if localTest {
		upstreamAddress = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", upstreamAddress)
	if err != nil {
		return err
	}
	d.upstream = "http://" + listener.Addr().String()
	upstream := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: testUpstreamHandler()}
	defer upstream.Close()
	go func() {
		if err := upstream.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print("test upstream stopped")
		}
	}()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.mu.Lock()
				d.prune()
				d.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8088"
	}
	log.Printf("credential gateway listening on :%s; upstream login precedes OAuth; public fixtures only", port)
	listenAddress := ":" + port
	if localTest {
		listenAddress = "127.0.0.1:" + port
	}
	s := &http.Server{Addr: listenAddress, Handler: d.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	return s.ListenAndServe()
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
