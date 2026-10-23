package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const brokerClientID = "mcp-auth-demo"

func (d *demo) callbackURL() string { return strings.TrimRight(d.issuer, "/") + "/identity/callback" }
func loginCookie(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "demo_login_" + hex.EncodeToString(sum[:6])
}
func (d *demo) browserBound(r *http.Request, id string, p authorization) bool {
	cookie, err := r.Cookie(loginCookie(id))
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(p.binding)) == 1
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Sign in to the upstream service · MCP</title><style>body{font:17px system-ui;background:#f6f8fb;color:#17243a;max-width:580px;margin:6vh auto;padding:24px}main{background:white;border:1px solid #dce2eb;border-radius:16px;padding:32px}label{display:block;margin-top:20px}input{box-sizing:border-box;display:block;width:100%;padding:12px;font:inherit;margin-top:6px;border:1px solid #a6b1c3;border-radius:6px}button{margin-top:24px;background:#2456c9;color:white;border:0;padding:13px 20px;font:inherit;border-radius:6px}.note{font-size:14px;color:#526078}.error{color:#a32020}</style><main><h1>Sign in to the upstream service</h1><p>Select the saved Login in 1Password, then sign in. Your MCP client receives its OAuth token only after the upstream accepts these credentials.</p><p class="note">Public demo only: use <code>demo-user</code> and <code>demo-password</code>. Never enter real service credentials. 1Password controls its own unlock prompt; the browser extension can reuse an existing unlock.</p>{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<form method="post" action="{{.Action}}"><input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><label for="username">Upstream username</label><input id="username" name="username" autocomplete="username" required maxlength="128"><label for="password">Upstream password</label><input id="password" name="password" type="password" autocomplete="current-password" required maxlength="1024"><button type="submit">Sign in and authorize MCP client</button></form></main></html>`))

func (d *demo) renderLogin(w http.ResponseWriter, id string, p authorization, status int, message string) {
	securePage(w)
	w.WriteHeader(status)
	_ = loginPage.Execute(w, map[string]string{"Action": d.basePath + "/login/approve", "Request": id, "CSRF": p.csrf, "Error": message})
}

func (d *demo) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	q := r.URL.Query()
	decoded, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if q.Get("client_id") != brokerClientID || q.Get("redirect_uri") != d.callbackURL() || q.Get("response_type") != "code" || q.Get("state") == "" || len(q.Get("state")) > 1024 || q.Get("code_challenge_method") != "S256" || err != nil || len(decoded) != 32 || strings.Contains(q.Get("scope"), "openid") {
		http.Error(w, "invalid upstream authorization request", 400)
		return
	}
	id, e1 := opaque()
	csrf, e2 := opaque()
	binding, e3 := opaque()
	if e1 != nil || e2 != nil || e3 != nil {
		http.Error(w, "login unavailable", 503)
		return
	}
	p := authorization{redirect: q.Get("redirect_uri"), state: q.Get("state"), challenge: q.Get("code_challenge"), csrf: csrf, binding: binding, expires: d.now().Add(5 * time.Minute)}
	d.mu.Lock()
	d.prune()
	if len(d.requests) >= 100 {
		d.mu.Unlock()
		http.Error(w, "too many pending logins", 429)
		return
	}
	d.requests[id] = p
	d.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: loginCookie(id), Value: binding, Path: d.basePath + "/login", Secure: strings.HasPrefix(d.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	d.renderLogin(w, id, p, 200, "")
}

func (d *demo) approve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	securePage(w)
	if r.Header.Get("Origin") != d.origin {
		http.Error(w, "invalid login origin", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid login form", 400)
		return
	}
	id := r.PostForm.Get("request")
	d.mu.Lock()
	d.prune()
	p, ok := d.requests[id]
	if !ok {
		d.mu.Unlock()
		http.Error(w, "login expired; restart the MCP connection", 410)
		return
	}
	if !d.browserBound(r, id, p) || subtle.ConstantTimeCompare([]byte(p.csrf), []byte(r.PostForm.Get("csrf"))) != 1 {
		d.mu.Unlock()
		http.Error(w, "login must finish in the browser that started it", 403)
		return
	}
	if p.attempts >= 5 {
		d.mu.Unlock()
		http.Error(w, "too many failed attempts; restart sign-in", 429)
		return
	}
	p.attempts++
	d.requests[id] = p
	d.mu.Unlock()
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if err := d.validateUpstream(r.Context(), username, password); err != nil {
		d.renderLogin(w, id, p, 401, "The upstream rejected this Login or is unavailable. Check the dedicated test credentials.")
		return
	}
	subject, e1 := opaque()
	code, e2 := opaque()
	if e1 != nil || e2 != nil {
		http.Error(w, "login unavailable", 503)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	// Recheck and consume under the lock; parallel submissions cannot approve twice.
	if _, exists := d.requests[id]; !exists {
		http.Error(w, "login already completed", 410)
		return
	}
	if len(d.logins) >= 100 || len(d.codes) >= 100 {
		http.Error(w, "demo connection limit reached", 429)
		return
	}
	delete(d.requests, id)
	d.logins[subject] = login{username: username, password: password, expires: d.now().Add(time.Duration(d.config.CredentialTTL) * time.Second)}
	d.codes[code] = providerCode{subject: subject, challenge: p.challenge, redirect: p.redirect, expires: d.now().Add(time.Minute)}
	http.SetCookie(w, &http.Cookie{Name: loginCookie(id), Path: d.basePath + "/login", Secure: strings.HasPrefix(d.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	location, _ := url.Parse(p.redirect)
	query := location.Query()
	query.Set("code", code)
	query.Set("state", p.state)
	location.RawQuery = query.Encode()
	http.Redirect(w, r, location.String(), http.StatusSeeOther)
}

func (d *demo) providerToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid token request", 400)
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	if r.PostForm.Get("client_id") != brokerClientID || r.PostForm.Get("grant_type") != "authorization_code" || len(verifier) < 43 || len(verifier) > 128 {
		http.Error(w, "invalid token request", 400)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	code := r.PostForm.Get("code")
	p, ok := d.codes[code]
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !ok || r.PostForm.Get("redirect_uri") != p.redirect || subtle.ConstantTimeCompare([]byte(challenge), []byte(p.challenge)) != 1 {
		http.Error(w, "invalid authorization grant", 400)
		return
	}
	token, err := opaque()
	if err != nil {
		http.Error(w, "token unavailable", 503)
		return
	}
	if len(d.tokens) >= 100 {
		http.Error(w, "demo token limit reached", 429)
		return
	}
	delete(d.codes, code)
	d.tokens[token] = providerToken{subject: p.subject, expires: d.now().Add(time.Minute)}
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 60, "scope": "profile"})
}

func (d *demo) userinfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.WriteHeader(401)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	token, ok := d.tokens[parts[1]]
	if !ok {
		w.WriteHeader(401)
		return
	}
	// The identity is an opaque credential-session reference, never a password
	// or an upstream token. mcp-auth prefixes it for this resource's login method.
	_ = json.NewEncoder(w).Encode(map[string]string{"sub": token.subject})
}
