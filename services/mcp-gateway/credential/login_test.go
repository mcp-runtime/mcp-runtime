package credential

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	origin   = "https://auth.example.com"
	callback = "https://auth.example.com/mcp-auth/identity/callback"
	verifier = "verifier-verifier-verifier-verifier-verifier-12345"
)

type harness struct {
	t     *testing.T
	p     *LoginProvider
	now   time.Time
	calls int
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, now: time.Now()}
	h.p = NewLoginProvider(LoginConfig{
		ClientID: "login-demo", Origin: origin, BasePath: "/demo/mcp", CallbackURL: callback,
		Fields: []FormField{{Name: "username", Label: "Username"}, {Name: "api_token", Label: "Token", Secret: true}},
		Now:    func() time.Time { return h.now },
		Authenticate: func(_ context.Context, v map[string]string) (string, error) {
			h.calls++
			switch v["api_token"] {
			case "good":
				return "sub-" + v["username"], nil
			case "down":
				return "", ErrUnavailable
			}
			return "", ErrRejected
		},
	})
	return h
}

func challengeFor(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (h *harness) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.p.ServeHTTP(w, r)
	return w
}

func authorizeURL(mutate func(url.Values)) string {
	q := url.Values{"client_id": {"login-demo"}, "redirect_uri": {callback}, "response_type": {"code"}, "state": {"gateway.abc"},
		"code_challenge": {challengeFor(verifier)}, "code_challenge_method": {"S256"}, "scope": {"profile"}}
	if mutate != nil {
		mutate(q)
	}
	return "/login/authorize?" + q.Encode()
}

var hiddenField = regexp.MustCompile(`name="(request|csrf)" value="([^"]+)"`)

// start opens the sign-in page and returns the form values and the binding cookie.
func (h *harness) start() (request, csrf string, cookie *http.Cookie) {
	h.t.Helper()
	w := h.do(httptest.NewRequest(http.MethodGet, authorizeURL(nil), nil))
	if w.Code != http.StatusOK {
		h.t.Fatalf("authorize status %d: %s", w.Code, w.Body.String())
	}
	for _, m := range hiddenField.FindAllStringSubmatch(w.Body.String(), -1) {
		if m[1] == "request" {
			request = m[2]
		} else {
			csrf = m[2]
		}
	}
	cookies := w.Result().Cookies()
	if request == "" || csrf == "" || len(cookies) != 1 {
		h.t.Fatalf("missing form state: request=%q csrf=%q cookies=%d", request, csrf, len(cookies))
	}
	return request, csrf, cookies[0]
}

func (h *harness) approve(request, csrf string, cookie *http.Cookie, origin string, values map[string]string) *httptest.ResponseRecorder {
	form := url.Values{"request": {request}, "csrf": {csrf}}
	for k, v := range values {
		form.Set(k, v)
	}
	r := httptest.NewRequest(http.MethodPost, "/login/approve", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return h.do(r)
}

func (h *harness) exchange(code, verifier string) *httptest.ResponseRecorder {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"login-demo"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {callback}}
	r := httptest.NewRequest(http.MethodPost, "/login/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return h.do(r)
}

var good = map[string]string{"username": "alice", "api_token": "good"}

func codeFrom(t *testing.T, w *httptest.ResponseRecorder) (code, state string) {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("approve status %d: %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Scheme+"://"+loc.Host+loc.Path != callback {
		t.Fatalf("redirected to %s", loc)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}

func TestFullSignInIssuesAnOpaqueSubjectAndNeverEchoesValues(t *testing.T) {
	h := newHarness(t)
	w := h.do(httptest.NewRequest(http.MethodGet, authorizeURL(nil), nil))
	body := w.Body.String()
	for _, want := range []string{`Username`, `Token`, `type="password"`, `name="username"`, `action="/demo/mcp/login/approve"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-in page lacks %q", want)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "base-uri 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	// form-action would block the redirect on to the MCP client's redirect_uri.
	if strings.Contains(csp, "form-action") {
		t.Errorf("CSP restricts form-action, which breaks the redirect chain back to the client: %q", csp)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/demo/mcp/login" || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("binding cookie is not locked down: %+v", cookie)
	}

	request, csrf, cookie := h.start()
	code, state := codeFrom(t, h.approve(request, csrf, cookie, origin, good))
	if state != "gateway.abc" || code == "" {
		t.Fatalf("code=%q state=%q", code, state)
	}
	tok := h.exchange(code, verifier)
	if tok.Code != http.StatusOK || strings.Contains(tok.Body.String(), "good") {
		t.Fatalf("token exchange: %d %s", tok.Code, tok.Body.String())
	}
	var granted struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(tok.Body.Bytes(), &granted)
	r := httptest.NewRequest(http.MethodGet, "/login/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+granted.AccessToken)
	info := h.do(r)
	if info.Code != http.StatusOK || strings.TrimSpace(info.Body.String()) != `{"sub":"sub-alice"}` {
		t.Fatalf("userinfo: %d %s", info.Code, info.Body.String())
	}
	if h.exchange(code, verifier).Code != http.StatusBadRequest {
		t.Fatal("an authorization code worked twice")
	}
}

func TestAuthorizeRejectsMalformedRequests(t *testing.T) {
	h := newHarness(t)
	for name, mutate := range map[string]func(url.Values){
		"wrong client":    func(q url.Values) { q.Set("client_id", "other") },
		"wrong redirect":  func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") },
		"no state":        func(q url.Values) { q.Del("state") },
		"plain challenge": func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"short challenge": func(q url.Values) { q.Set("code_challenge", "abc") },
		"wrong type":      func(q url.Values) { q.Set("response_type", "token") },
	} {
		if w := h.do(httptest.NewRequest(http.MethodGet, authorizeURL(mutate), nil)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, w.Code)
		}
	}
	if w := h.do(httptest.NewRequest(http.MethodPost, authorizeURL(nil), nil)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST authorize: %d", w.Code)
	}
}

func TestApproveRejectsForgedOrUnboundSubmissions(t *testing.T) {
	h := newHarness(t)
	request, csrf, cookie := h.start()
	for name, run := range map[string]func() *httptest.ResponseRecorder{
		"wrong origin": func() *httptest.ResponseRecorder {
			return h.approve(request, csrf, cookie, "https://evil.example", good)
		},
		"no origin": func() *httptest.ResponseRecorder { return h.approve(request, csrf, cookie, "", good) },
		"no cookie": func() *httptest.ResponseRecorder { return h.approve(request, csrf, nil, origin, good) },
		"wrong cookie": func() *httptest.ResponseRecorder {
			return h.approve(request, csrf, &http.Cookie{Name: cookie.Name, Value: "other"}, origin, good)
		},
		"wrong csrf":      func() *httptest.ResponseRecorder { return h.approve(request, "nope", cookie, origin, good) },
		"unknown request": func() *httptest.ResponseRecorder { return h.approve("missing", csrf, cookie, origin, good) },
	} {
		w := run()
		if w.Code != http.StatusForbidden && w.Code != http.StatusGone {
			t.Errorf("%s: status %d", name, w.Code)
		}
	}
	if h.calls != 0 {
		t.Fatalf("the upstream was asked %d times for forged submissions", h.calls)
	}
}

func TestWrongValuesReshowTheFormWithoutEchoingThemAndAreLimited(t *testing.T) {
	h := newHarness(t)
	request, csrf, cookie := h.start()
	bad := map[string]string{"username": "alice", "api_token": "secret-wrong-value"}
	for i := 0; i < maxAttempts; i++ {
		w := h.approve(request, csrf, cookie, origin, bad)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Sign-in failed") || strings.Contains(w.Body.String(), "secret-wrong-value") {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := h.approve(request, csrf, cookie, origin, good); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a sixth attempt was allowed: %d", w.Code)
	}
}

func TestUnavailableUpstreamAndMissingFields(t *testing.T) {
	h := newHarness(t)
	request, csrf, cookie := h.start()
	if w := h.approve(request, csrf, cookie, origin, map[string]string{"username": "a", "api_token": "down"}); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "temporarily unavailable") {
		t.Fatalf("unavailable upstream: %d", w.Code)
	}
	calls := h.calls
	if w := h.approve(request, csrf, cookie, origin, map[string]string{"username": "a"}); w.Code != http.StatusUnauthorized || h.calls != calls {
		t.Fatalf("a missing field reached the upstream or was accepted: %d", w.Code)
	}
}

func TestCodesAreBoundToPKCEAndExpire(t *testing.T) {
	h := newHarness(t)
	request, csrf, cookie := h.start()
	code, _ := codeFrom(t, h.approve(request, csrf, cookie, origin, good))
	if w := h.exchange(code, verifier+"-different"); w.Code != http.StatusBadRequest {
		t.Fatalf("a wrong PKCE verifier worked: %d", w.Code)
	}
	if w := h.exchange(code, verifier); w.Code != http.StatusOK {
		t.Fatalf("the right verifier failed after a wrong attempt: %d", w.Code)
	}

	request, csrf, cookie = h.start()
	code, _ = codeFrom(t, h.approve(request, csrf, cookie, origin, good))
	h.now = h.now.Add(2 * codeTTL)
	if w := h.exchange(code, verifier); w.Code != http.StatusBadRequest {
		t.Fatalf("an expired code worked: %d", w.Code)
	}
}

func TestSignInRequestCannotBeCompletedTwiceOrAfterExpiry(t *testing.T) {
	h := newHarness(t)
	request, csrf, cookie := h.start()
	codeFrom(t, h.approve(request, csrf, cookie, origin, good))
	if w := h.approve(request, csrf, cookie, origin, good); w.Code != http.StatusGone {
		t.Fatalf("a completed sign-in was reused: %d", w.Code)
	}
	request, csrf, cookie = h.start()
	h.now = h.now.Add(2 * requestTTL)
	if w := h.approve(request, csrf, cookie, origin, good); w.Code != http.StatusGone {
		t.Fatalf("an expired sign-in was accepted: %d", w.Code)
	}
}

func TestUserinfoNeedsAValidShortLivedToken(t *testing.T) {
	h := newHarness(t)
	for _, header := range []string{"", "Bearer", "Basic abc", "Bearer unknown-token"} {
		r := httptest.NewRequest(http.MethodGet, "/login/userinfo", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if w := h.do(r); w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d", header, w.Code)
		}
	}
	if w := h.do(httptest.NewRequest(http.MethodGet, "/login/other", nil)); w.Code != http.StatusNotFound {
		t.Errorf("unknown endpoint: %d", w.Code)
	}
}
