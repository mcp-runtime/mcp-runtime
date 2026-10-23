// This example uses public test credentials only. It is not a production vault.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const demoUsername = "demo-user"
const demoPassword = "demo-password" // Public fixture; never use a real password in this example.

type identity struct {
	subject, session string
	expires          time.Time
}
type login struct {
	username, password string
	expires            time.Time
}
type pending struct {
	identity identity
	expires  time.Time
	csrf     string
}
type demo struct {
	mu                                   sync.Mutex
	resource, origin, basePath, upstream string
	verifier                             *mcpauth.JWTVerifier
	logins                               map[string]login
	pending                              map[string]pending
	now                                  func() time.Time
	client                               *http.Client
}

func newDemo(resource string, verifier *mcpauth.JWTVerifier) *demo {
	u, _ := url.Parse(resource)
	return &demo{resource: resource, origin: u.Scheme + "://" + u.Host, basePath: u.Path, verifier: verifier,
		logins: map[string]login{}, pending: map[string]pending{}, now: time.Now,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func key(id identity) string { return id.subject + "\x00" + id.session }
func opaque() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (d *demo) prune() {
	now := d.now()
	for k, v := range d.pending {
		if !now.Before(v.expires) {
			delete(d.pending, k)
		}
	}
	for k, v := range d.logins {
		if !now.Before(v.expires) {
			delete(d.logins, k)
		}
	}
}
func (d *demo) ticket(id identity) (string, error) {
	if id.subject == "" || id.session == "" || !d.now().Before(id.expires) {
		return "", errors.New("a current OAuth session is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	if len(d.pending) >= 100 {
		return "", errors.New("too many pending connections; retry later")
	}
	t, err := opaque()
	if err != nil {
		return "", errors.New("connection unavailable")
	}
	d.pending[t] = pending{identity: id, expires: minTime(d.now().Add(5*time.Minute), id.expires)}
	return t, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (d *demo) save(ticket, username, password string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	p, ok := d.pending[ticket]
	if !ok {
		return errors.New("connection link expired or already used")
	}
	// Validate with the dedicated test upstream's fixed public fixture.
	if subtle.ConstantTimeCompare([]byte(username), []byte(demoUsername)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(demoPassword)) != 1 {
		return errors.New("test upstream rejected credentials; use the dedicated demo Login item")
	}
	if len(d.logins) >= 100 {
		if _, ok := d.logins[key(p.identity)]; !ok {
			return errors.New("demo connection limit reached")
		}
	}
	d.logins[key(p.identity)] = login{username: username, password: password, expires: minTime(d.now().Add(15*time.Minute), p.identity.expires)}
	delete(d.pending, ticket)
	return nil
}
func (d *demo) credentials(id identity) (login, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	value, ok := d.logins[key(id)]
	return value, ok
}
func (d *demo) callUpstream(ctx context.Context, id identity) (map[string]any, error) {
	credential, ok := d.credentials(id)
	if !ok {
		return nil, errors.New("upstream is not connected; call connect_upstream first")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.upstream+"/status", nil)
	if err != nil {
		return nil, errors.New("test upstream unavailable")
	}
	// The MCP OAuth bearer is never sent to the upstream API.
	req.SetBasicAuth(credential.username, credential.password)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.New("test upstream unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("test upstream rejected credentials")
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		return nil, errors.New("invalid upstream response")
	}
	return result, nil
}
func current(ctx context.Context) (identity, error) {
	c, ok := mcpauth.TokenClaimsFromContext(ctx)
	if !ok {
		return identity{}, errors.New("authentication required")
	}
	// Bind to this verified access token's unique ID. The deployed mcp-auth
	// 0.4.4 issues jti, but does not issue the later refresh-family sid claim.
	// Refreshing the OAuth token deliberately requires a new demo connection.
	sid, _ := c.Raw["jti"].(string)
	exp, _ := c.Raw["exp"].(float64)
	if c.Subject == "" || sid == "" || exp == 0 {
		return identity{}, errors.New("a session-bound OAuth token is required")
	}
	return identity{subject: c.Subject, session: sid, expires: time.Unix(int64(exp), 0)}, nil
}

type emptyArgs struct{}

func toolResult(value any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}
func (d *demo) mcpServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "onepassword-credential-demo", Version: "0.1.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "connect_upstream", Description: "Create a five-minute connection link. Open it and fill the dedicated demo Login with 1Password, or pass it to the local connector. Never put credentials in tool arguments."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		id, err := current(ctx)
		if err != nil {
			return nil, nil, err
		}
		ticket, err := d.ticket(id)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(map[string]any{"connect_url": d.origin + d.basePath + "/connect?ticket=" + ticket, "expires_in": 300, "instructions": "Use a dedicated test Login: username demo-user and password demo-password. Browser: open connect_url and use 1Password autofill. Desktop: run the local connector with this URL and op:// references. The URL permits one connection to this OAuth session; do not share it."})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "upstream_status", Description: "Call the fixed test upstream with credentials held for this OAuth session; return only authentication status."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		id, err := current(ctx)
		if err != nil {
			return nil, nil, err
		}
		result, err := d.callUpstream(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(result)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "disconnect_upstream", Description: "Discard the upstream credentials and pending connections for the current OAuth session."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		id, err := current(ctx)
		if err != nil {
			return nil, nil, err
		}
		d.mu.Lock()
		delete(d.logins, key(id))
		for k, p := range d.pending {
			if key(p.identity) == key(id) {
				delete(d.pending, k)
			}
		}
		d.mu.Unlock()
		return toolResult(map[string]bool{"disconnected": true})
	})
	return s
}
func (d *demo) handler() http.Handler {
	mux := http.NewServeMux()
	u, _ := url.Parse(d.resource)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return d.mcpServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	bound := auth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		id, err := current(ctx)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: key(id), Scopes: []string{"tools:read"}, Expiration: id.expires}, nil
	}, nil)(handler)
	metadataPath := "/.well-known/oauth-protected-resource" + u.Path
	mux.Handle(u.Path, mcpauth.RequireToken(d.verifier, mcpauth.ResourceMetadata{URL: d.origin + metadataPath}, bound))
	metadata := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": d.resource, "authorization_servers": []string{d.verifier.Issuer}, "scopes_supported": []string{"tools:read"}, "bearer_methods_supported": []string{"header"}})
	}
	mux.HandleFunc(metadataPath, metadata)
	// This alias is beneath the server ingress prefix, even if root well-known routing is unavailable.
	mux.HandleFunc(d.basePath+"/.well-known/oauth-protected-resource", metadata)
	mux.HandleFunc(d.basePath+"/connect", d.page)
	mux.HandleFunc(d.basePath+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != d.basePath+"/" {
			http.NotFound(w, r)
			return
		}
		securePage(w)
		_, _ = io.WriteString(w, "<!doctype html><title>1Password MCP demo</title><h1>1Password MCP credential demo</h1><p>Connect your MCP client to the /mcp URL, sign in, then call connect_upstream. Use dedicated test credentials only.</p>")
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) })
	return mux
}
func securePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

var pageTemplate = template.Must(template.New("connect").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Connect test upstream · 1Password MCP demo</title><style>body{font:17px system-ui;background:#f6f8fb;color:#17243a;max-width:560px;margin:8vh auto;padding:24px}main{background:white;border:1px solid #dce2eb;border-radius:16px;padding:32px}label{display:block;margin-top:20px}input{display:block;box-sizing:border-box;width:100%;padding:12px;font:inherit;margin-top:6px;border:1px solid #a6b1c3;border-radius:6px}button{margin-top:24px;background:#2456c9;color:white;border:0;padding:13px 20px;font:inherit;border-radius:6px}.note{font-size:14px;color:#526078}</style><main><h1>Connect your test upstream</h1><p>Use 1Password to fill the dedicated demo Login, then select Connect.</p><p class="note">Demo only: use username <code>demo-user</code> and password <code>demo-password</code>. Never enter real service credentials here. Values stay in this server's memory for at most 15 minutes and are scoped to the OAuth session that created this link.</p><form method="post" action="{{.Action}}"><input type="hidden" name="ticket" value="{{.Ticket}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><label for="username">Username</label><input id="username" name="username" autocomplete="username" required maxlength="128"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required maxlength="1024"><button type="submit">Connect test upstream</button></form></main></html>`))

func (d *demo) page(w http.ResponseWriter, r *http.Request) {
	securePage(w)
	switch r.Method {
	case "GET":
		ticket := r.URL.Query().Get("ticket")
		d.mu.Lock()
		d.prune()
		p, ok := d.pending[ticket]
		if !ok {
			d.mu.Unlock()
			http.Error(w, "Connection link expired or already used. Call connect_upstream again.", 410)
			return
		}
		csrf := p.csrf
		if csrf == "" {
			var err error
			csrf, err = opaque()
			if err != nil {
				d.mu.Unlock()
				http.Error(w, "Connection unavailable", 503)
				return
			}
		}
		p.csrf = csrf
		d.pending[ticket] = p
		d.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "demo_csrf", Value: csrf, Path: d.basePath + "/connect", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 300})
		_ = pageTemplate.Execute(w, map[string]string{"Action": d.basePath + "/connect", "Ticket": ticket, "CSRF": csrf})
	case "POST":
		if r.Header.Get("Origin") != d.origin {
			http.Error(w, "Invalid connection origin", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid connection form", 400)
			return
		}
		cookie, err := r.Cookie("demo_csrf")
		csrf := r.PostForm.Get("csrf")
		if err != nil || csrf == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(csrf)) != 1 {
			http.Error(w, "Invalid connection approval", 403)
			return
		}
		d.mu.Lock()
		p, ok := d.pending[r.PostForm.Get("ticket")]
		valid := ok && p.csrf != "" && subtle.ConstantTimeCompare([]byte(p.csrf), []byte(csrf)) == 1
		d.mu.Unlock()
		if !valid {
			http.Error(w, "Invalid or expired connection approval", 403)
			return
		}
		if err := d.save(r.PostForm.Get("ticket"), r.PostForm.Get("username"), r.PostForm.Get("password")); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "demo_csrf", Path: d.basePath + "/connect", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		_, _ = io.WriteString(w, "<!doctype html><title>Connected</title><h1>Test upstream connected</h1><p>Return to your MCP client and call upstream_status twice to verify reuse. Call disconnect_upstream to discard this connection.</p>")
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
	}
}
func run() error {
	resource := os.Getenv("MCP_AUTH_RESOURCE")
	if resource == "" {
		resource = "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp"
	}
	u, err := url.Parse(resource)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return errors.New("MCP_AUTH_RESOURCE must be an HTTPS /mcp URL")
	}
	issuer := os.Getenv("MCP_AUTH_ISSUER")
	if issuer == "" {
		issuer = "https://auth.mcpruntime.org/mcp-auth"
	}
	metadata, err := mcpauth.DiscoverAuthorizationServer(&http.Client{Timeout: 10 * time.Second}, issuer)
	if err != nil {
		return errors.New("authorization server discovery failed")
	}
	verifier := &mcpauth.JWTVerifier{Issuer: issuer, JWKSURL: metadata.JWKSURI, Audience: resource, RequiredScopes: map[string]bool{"tools:read": true}}
	d := newDemo(resource, verifier)
	listener, err := net.Listen("tcp", "127.0.0.1:9099")
	if err != nil {
		return err
	}
	d.upstream = "http://" + listener.Addr().String()
	upstream := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != demoUsername || p != demoPassword {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "service": "fixed-demo-upstream", "credential_source": "session-memory", "oauth_token_forwarded": false})
	})}
	go func() {
		if err := upstream.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print("test upstream stopped")
		}
	}()
	defer upstream.Close()
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
	log.Printf("1Password credential demo listening on :%s; public test credentials only", port)
	s := &http.Server{Addr: ":" + port, Handler: d.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	return s.ListenAndServe()
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
