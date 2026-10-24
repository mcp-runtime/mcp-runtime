package credential

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Fakes for tests only: a stand-in mcp-auth and a fake upstream MCP server. All
// credentials are fake.

const (
	tResource = "https://mcp.example.test/demo/mcp"
	tSentinel = "SENTINEL-SECRET-VALUE"
)

// newKey returns a fresh RSA key.
func newKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func b64(v any) string {
	data, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(data)
}

// signJWT signs claims with RS256.
func signJWT(t testing.TB, key *rsa.PrivateKey, kid, typ string, claims map[string]any) string {
	t.Helper()
	msg := b64(map[string]any{"alg": "RS256", "kid": kid, "typ": typ}) + "." + b64(claims)
	digest := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// FakeAS stands in for mcp-auth: JWKS, access tokens and the credential exchange.
type FakeAS struct {
	Server *httptest.Server
	Key    *rsa.PrivateKey
	t      testing.TB

	mu         sync.Mutex
	revoked    map[string]bool
	failStatus int
	lastForm   url.Values
	Exchanges  atomic.Int64
}

func NewFakeAS(t testing.TB) *FakeAS {
	a := &FakeAS{Key: newKey(t), t: t, revoked: map[string]bool{}}
	a.Server = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.Server.Close)
	return a
}

func (a *FakeAS) Issuer() string { return a.Server.URL }

func (a *FakeAS) Revoke(sid string) { a.mu.Lock(); a.revoked[sid] = true; a.mu.Unlock() }

// FailExchange makes the token endpoint answer with status (0 restores it).
func (a *FakeAS) FailExchange(status int) { a.mu.Lock(); a.failStatus = status; a.mu.Unlock() }

func (a *FakeAS) LastForm() url.Values { a.mu.Lock(); defer a.mu.Unlock(); return a.lastForm }

// TokenOpts customises an access token. Zero values mean "valid default".
type TokenOpts struct {
	Subject, Resource, Scope, SessionID, Issuer string
	NoSession                                   bool
	ExpiresIn                                   time.Duration
	Key                                         *rsa.PrivateKey
}

// Token mints an access token the way mcp-auth would.
func (a *FakeAS) Token(o TokenOpts) string {
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&o.Subject, "alice")
	def(&o.Resource, tResource)
	def(&o.Scope, "tools:read")
	def(&o.SessionID, "session-1")
	def(&o.Issuer, a.Issuer())
	if o.ExpiresIn == 0 {
		o.ExpiresIn = time.Minute
	}
	key := o.Key
	if key == nil {
		key = a.Key
	}
	now := time.Now()
	claims := map[string]any{"iss": o.Issuer, "sub": o.Subject, "aud": o.Resource, "scope": o.Scope,
		"iat": now.Unix(), "exp": now.Add(o.ExpiresIn).Unix(), "jti": fmt.Sprint(now.UnixNano())}
	if !o.NoSession {
		claims["sid"] = o.SessionID
	}
	return signJWT(a.t, key, "as-key", "at+jwt", claims)
}

func (a *FakeAS) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/jwks.json":
		pub := a.Key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "as-key",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	case "/token":
		a.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *FakeAS) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	a.mu.Lock()
	a.lastForm = r.PostForm
	fail := a.failStatus
	a.mu.Unlock()
	a.Exchanges.Add(1)
	if fail != 0 {
		w.WriteHeader(fail)
		return
	}
	form := r.PostForm
	parts := strings.Split(form.Get("subject_token"), ".")
	var subject map[string]any
	if len(parts) != 3 {
		w.WriteHeader(400)
		return
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &subject)
	sid, _ := subject["sid"].(string)
	a.mu.Lock()
	revoked := a.revoked[sid]
	a.mu.Unlock()
	if revoked || form.Get("requested_token_type") != "urn:mcp-auth:token-type:credential-access-assertion" {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	clientID := form.Get("client_id")
	now := time.Now()
	assertion := signJWT(a.t, a.Key, "as-key", "credential-access+jwt", map[string]any{
		"iss": a.Issuer(), "sub": subject["sub"], "aud": clientID, "azp": clientID,
		"resource": form.Get("audience"), "sid": sid, "scope": subject["scope"], "token_use": "credential_access",
		"jti": fmt.Sprint(now.UnixNano()), "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": assertion, "token_type": "N_A", "expires_in": 60,
		"issued_token_type": "urn:mcp-auth:token-type:credential-access-assertion"})
}

// Record is one request seen by the fake upstream.
type Record struct {
	Method string
	URL    string
	Header http.Header
	Body   string
}

// FakeUpstream is a fake upstream MCP server that records exactly what it receives.
type FakeUpstream struct {
	Server *httptest.Server
	mu     sync.Mutex
	recs   []Record
	// Behavior overrides the default responder when set.
	Behavior func(w http.ResponseWriter, r *http.Request)
}

func NewFakeUpstream(t testing.TB) *FakeUpstream {
	u := &FakeUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.recs = append(u.recs, Record{r.Method, r.URL.RequestURI(), r.Header.Clone(), string(body)})
		n, behavior := len(u.recs), u.Behavior
		u.mu.Unlock()
		if behavior != nil {
			behavior(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.Contains(string(body), "initialize") {
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("upstream-session-%d", n))
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	t.Cleanup(u.Server.Close)
	return u
}

func (u *FakeUpstream) Records() []Record {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Record(nil), u.recs...)
}

func nil2ctx() context.Context { return context.Background() }
