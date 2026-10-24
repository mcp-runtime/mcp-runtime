// The sign-in half of the gateway. mcp-auth treats it as a plain OAuth 2.0
// identity provider for one resource: it sends the browser to /login/authorize,
// later exchanges the code at /login/token and reads an opaque subject from
// /login/userinfo.
//
// The person's header values go from the browser to this service only. They are
// validated at the upstream, kept in the gateway's memory, and never reach
// mcp-auth, which sees nothing but the opaque subject.
package credential

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	requestTTL   = 5 * time.Minute
	codeTTL      = time.Minute
	tokenTTL     = time.Minute
	maxPending   = 200
	maxAttempts  = 5
	maxFieldSize = 1024
)

// LoginConfig describes one service's sign-in.
type LoginConfig struct {
	// ClientID is the OAuth client id mcp-auth presents.
	ClientID string
	// Origin is this service's public origin, for the same-origin check.
	Origin string
	// BasePath is the public path /login/* lives under.
	BasePath string
	// CallbackURL is the only redirect_uri accepted: mcp-auth's identity callback.
	CallbackURL string
	// Fields are the inputs shown, in order.
	Fields []FormField
	// Authenticate validates submitted values at the upstream and stores them,
	// returning the opaque subject. It reports ErrRejected or ErrUnavailable.
	Authenticate func(ctx context.Context, values map[string]string) (string, error)
	Now          func() time.Time
}

type loginPending struct {
	redirect, state, challenge, csrf, binding string
	attempts                                  int
	expires                                   time.Time
}

type loginGrant struct {
	subject, challenge, redirect string
	expires                      time.Time
}

type loginToken struct {
	subject string
	expires time.Time
}

// LoginProvider serves the sign-in endpoints for one service.
type LoginProvider struct {
	cfg      LoginConfig
	secure   bool
	mu       sync.Mutex
	requests map[string]loginPending
	codes    map[string]loginGrant
	tokens   map[string]loginToken
}

// NewLoginProvider builds a provider.
func NewLoginProvider(cfg LoginConfig) *LoginProvider {
	return &LoginProvider{
		cfg: cfg, secure: strings.HasPrefix(cfg.Origin, "https://"),
		requests: map[string]loginPending{}, codes: map[string]loginGrant{}, tokens: map[string]loginToken{},
	}
}

func (p *LoginProvider) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}

func opaque() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func cookieName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "gw_login_" + hex.EncodeToString(sum[:6])
}

// ServeHTTP expects the request path relative to BasePath: /login/<endpoint>.
func (p *LoginProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/login/") {
	case "authorize":
		p.authorize(w, r)
	case "approve":
		p.approve(w, r)
	case "token":
		p.token(w, r)
	case "userinfo":
		p.userinfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *LoginProvider) prune() {
	now := p.now()
	for k, v := range p.requests {
		if !now.Before(v.expires) {
			delete(p.requests, k)
		}
	}
	for k, v := range p.codes {
		if !now.Before(v.expires) {
			delete(p.codes, k)
		}
	}
	for k, v := range p.tokens {
		if !now.Before(v.expires) {
			delete(p.tokens, k)
		}
	}
}

func (p *LoginProvider) securePage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	// There is deliberately no form-action directive. A browser applies it to the
	// whole redirect chain of a form submission: approve redirects to mcp-auth's
	// callback, which redirects on to the MCP client's own redirect_uri (a loopback
	// URL or an app scheme this service cannot know), and any form-action list
	// short of that blocks the last hop. mcp-auth's own sign-in pages omit it for
	// the same reason. Scripts, framing and base-URI stay locked down.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
}

var page = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
<style>
:root{color-scheme:light dark}
body{font:16px/1.5 system-ui,sans-serif;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center;background:Canvas;color:CanvasText}
main{width:100%;max-width:26rem;padding:1.5rem}
h1{font-size:1.4rem;margin:0 0 1rem}
form{margin:0;padding:1rem;border:1px solid GrayText;border-radius:.5rem}
label{display:block;margin:.5rem 0 .25rem;font-size:.9rem}
input[type=text],input[type=password]{width:100%;box-sizing:border-box;padding:.5rem;font:inherit;border:1px solid GrayText;border-radius:.25rem;background:Field;color:FieldText}
button{margin-top:.75rem;width:100%;padding:.6rem;font:inherit;cursor:pointer;border:1px solid GrayText;border-radius:.25rem;background:ButtonFace;color:ButtonText}
.error{padding:.6rem .75rem;margin:0 0 1rem;border:1px solid #b00020;border-radius:.25rem;color:#b00020}
.note{font-size:.9rem;margin:0 0 .75rem}
</style>
</head>
<body>
<main>
<h1>Sign in</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="{{.Action}}" autocomplete="off">
<input type="hidden" name="request" value="{{.Request}}">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="note">Enter the values this service needs. Your MCP client receives its token only after the service accepts them.</p>
{{range .Fields}}
<label for="f-{{.Name}}">{{.Label}}</label>
{{if .Secret}}<input type="password" id="f-{{.Name}}" name="{{.Name}}" autocomplete="current-password" maxlength="1024" required>
{{else}}<input type="text" id="f-{{.Name}}" name="{{.Name}}" autocomplete="username" autocapitalize="off" spellcheck="false" maxlength="1024" required>
{{end}}
{{end}}
<button type="submit">Sign in</button>
</form>
</main>
</body>
</html>
`))

type pageView struct {
	Action, Request, CSRF, Error string
	Fields                       []FormField
}

func (p *LoginProvider) render(w http.ResponseWriter, id string, req loginPending, status int, message string) {
	p.securePage(w)
	w.WriteHeader(status)
	_ = page.Execute(w, pageView{Action: p.cfg.BasePath + "/login/approve", Request: id, CSRF: req.csrf, Error: message, Fields: p.cfg.Fields})
}

func (p *LoginProvider) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if q.Get("client_id") != p.cfg.ClientID || q.Get("redirect_uri") != p.cfg.CallbackURL || q.Get("response_type") != "code" ||
		q.Get("state") == "" || len(q.Get("state")) > 1024 || q.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != 32 {
		http.Error(w, "invalid sign-in request", http.StatusBadRequest)
		return
	}
	id, e1 := opaque()
	csrf, e2 := opaque()
	binding, e3 := opaque()
	if e1 != nil || e2 != nil || e3 != nil {
		http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
		return
	}
	req := loginPending{redirect: q.Get("redirect_uri"), state: q.Get("state"), challenge: q.Get("code_challenge"), csrf: csrf, binding: binding, expires: p.now().Add(requestTTL)}
	p.mu.Lock()
	p.prune()
	if len(p.requests) >= maxPending {
		p.mu.Unlock()
		http.Error(w, "too many pending sign-ins", http.StatusTooManyRequests)
		return
	}
	p.requests[id] = req
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName(id), Value: binding, Path: p.cfg.BasePath + "/login", Secure: p.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(requestTTL.Seconds())})
	p.render(w, id, req, http.StatusOK, "")
}

func (p *LoginProvider) approve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p.securePage(w)
	if r.Header.Get("Origin") != p.cfg.Origin {
		http.Error(w, "invalid sign-in origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid sign-in form", http.StatusBadRequest)
		return
	}
	id := r.PostForm.Get("request")
	p.mu.Lock()
	p.prune()
	req, ok := p.requests[id]
	if !ok {
		p.mu.Unlock()
		http.Error(w, "sign-in expired; restart the MCP connection", http.StatusGone)
		return
	}
	cookie, cookieErr := r.Cookie(cookieName(id))
	if cookieErr != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(req.binding)) != 1 ||
		subtle.ConstantTimeCompare([]byte(req.csrf), []byte(r.PostForm.Get("csrf"))) != 1 {
		p.mu.Unlock()
		http.Error(w, "sign-in must finish in the browser that started it", http.StatusForbidden)
		return
	}
	if req.attempts >= maxAttempts {
		p.mu.Unlock()
		http.Error(w, "too many failed attempts; restart sign-in", http.StatusTooManyRequests)
		return
	}
	req.attempts++
	p.requests[id] = req
	p.mu.Unlock()

	values := make(map[string]string, len(p.cfg.Fields))
	for _, f := range p.cfg.Fields {
		v := r.PostForm.Get(f.Name)
		if v == "" || len(v) > maxFieldSize {
			p.render(w, id, req, http.StatusUnauthorized, "Sign-in failed. Check your details and try again.")
			return
		}
		values[f.Name] = v
	}
	subject, err := p.cfg.Authenticate(r.Context(), values)
	switch {
	case errors.Is(err, ErrRejected):
		p.render(w, id, req, http.StatusUnauthorized, "Sign-in failed. Check your details and try again.")
		return
	case err != nil:
		p.render(w, id, req, http.StatusServiceUnavailable, "Sign-in is temporarily unavailable. Try again shortly.")
		return
	}
	code, cerr := opaque()
	if cerr != nil {
		http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	// Consume under the lock: parallel submissions cannot approve twice.
	if _, exists := p.requests[id]; !exists {
		http.Error(w, "sign-in already completed", http.StatusGone)
		return
	}
	if len(p.codes) >= maxPending {
		http.Error(w, "sign-in limit reached", http.StatusTooManyRequests)
		return
	}
	delete(p.requests, id)
	p.codes[code] = loginGrant{subject: subject, challenge: req.challenge, redirect: req.redirect, expires: p.now().Add(codeTTL)}
	http.SetCookie(w, &http.Cookie{Name: cookieName(id), Path: p.cfg.BasePath + "/login", Secure: p.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	location, _ := url.Parse(req.redirect)
	query := location.Query()
	query.Set("code", code)
	query.Set("state", req.state)
	location.RawQuery = query.Encode()
	http.Redirect(w, r, location.String(), http.StatusSeeOther)
}

func (p *LoginProvider) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	if r.PostForm.Get("client_id") != p.cfg.ClientID || r.PostForm.Get("grant_type") != "authorization_code" || len(verifier) < 43 || len(verifier) > 128 {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	code := r.PostForm.Get("code")
	g, ok := p.codes[code]
	if !ok || r.PostForm.Get("redirect_uri") != g.redirect || subtle.ConstantTimeCompare([]byte(challenge), []byte(g.challenge)) != 1 {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	access, err := opaque()
	if err != nil || len(p.tokens) >= maxPending {
		http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	delete(p.codes, code) // a code works once
	p.tokens[access] = loginToken{subject: g.subject, expires: p.now().Add(tokenTTL)}
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(tokenTTL.Seconds()), "scope": "profile"})
}

func (p *LoginProvider) userinfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	t, ok := p.tokens[parts[1]]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// An opaque reference to the stored values, never a value or a token.
	_ = json.NewEncoder(w).Encode(map[string]string{"sub": t.subject})
}
