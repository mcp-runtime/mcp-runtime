package credential

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"

	"mcp-runtime/pkg/credentialheaders"
)

const sessionHeader = "Mcp-Session-Id"

// ErrRejected means the upstream refused the credentials. ErrUnavailable means
// the upstream could not be asked. Neither carries upstream or credential text.
var (
	ErrRejected    = errors.New("upstream credential validation failed")
	ErrUnavailable = errors.New("upstream validation unavailable")
)

// forwardedRequest is the allowlist of client request headers sent upstream: any
// other header is dropped, which removes caller credentials, cookies, spoofed
// identity and forwarding headers, hop-by-hop headers and destination overrides
// without relying on a deny list.
var forwardedRequest = []string{"Accept", "Content-Type", "Mcp-Protocol-Version", "Last-Event-Id"}

// returnedResponse is the allowlist of upstream response headers returned.
var returnedResponse = []string{"Content-Type", "Cache-Control", "Retry-After", "Mcp-Protocol-Version"}

// Gateway implements http.Handler for every configured service.
type Gateway struct {
	cfg      *Config
	store    *Store
	authz    *Authorizer
	binder   *Binder
	upstream *http.Client
	checker  *http.Client
	mux      *http.ServeMux
}

// New builds the gateway. transport may be nil; tests inject one.
func New(cfg *Config, store *Store, transport http.RoundTripper) *Gateway {
	if transport == nil {
		transport = &http.Transport{
			Proxy:                 nil, // never route through an environment proxy
			DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext,
			TLSHandshakeTimeout:   cfg.ConnectTimeout,
			ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
			DisableCompression:    true, // we ask for identity encoding and stream bytes unchanged
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g := &Gateway{
		cfg: cfg, store: store, binder: NewBinder(cfg.SessionKey), mux: http.NewServeMux(),
		// An upstream redirect must never carry injected credentials elsewhere.
		upstream: &http.Client{Transport: transport, CheckRedirect: noRedirect},
		checker: &http.Client{
			Timeout:       cfg.ConnectTimeout + cfg.ResponseHeaderTimeout,
			Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext, TLSHandshakeTimeout: cfg.ConnectTimeout, ResponseHeaderTimeout: cfg.ResponseHeaderTimeout},
			CheckRedirect: noRedirect,
		},
	}
	g.authz = NewAuthorizer(cfg, g.metadataURL)
	g.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	g.mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ready"}) })
	for i := range cfg.Services {
		svc := cfg.Services[i]
		verifier := &mcpauth.JWTVerifier{Issuer: cfg.Issuer, Audience: svc.ResourceURL, RequiredScopes: toSet(svc.RequiredScopes)}
		meta := mcpauth.NewProtectedResourceMetadata(verifier, svc.ResourceURL, cfg.Issuer)
		provider := g.loginProvider(svc)
		seen := map[string]bool{}
		for _, path := range g.servicePaths(svc) {
			g.mux.HandleFunc(path, g.mcpHandler(svc))
			// The sign-in endpoints sit under the MCP path, so one route prefix
			// reaches both from the ingress.
			g.mux.Handle(path+"/login/", http.StripPrefix(path, provider))
			wk := "/.well-known/oauth-protected-resource" + strings.TrimRight(path, "/")
			if !seen[wk] {
				seen[wk] = true
				g.mux.HandleFunc(wk, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					writeJSON(w, 200, meta)
				})
			}
		}
	}
	return g
}

func toSet(values []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return m
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.mux.ServeHTTP(w, r) }

func (g *Gateway) metadataURL(s Service) string {
	u, _ := url.Parse(s.ResourceURL)
	u.Path = "/.well-known/oauth-protected-resource" + strings.TrimRight(u.Path, "/")
	return u.String()
}

// servicePaths: the canonical path plus the path left after an ingress strips
// the configured public prefix.
func (g *Gateway) servicePaths(s Service) []string {
	paths := []string{s.ResourcePath}
	if p := g.cfg.PublicPathPrefix; p != "" && strings.HasPrefix(s.ResourcePath, p+"/") {
		paths = append(paths, strings.TrimPrefix(s.ResourcePath, p))
	}
	return paths
}

// LoginClientID is the OAuth client id mcp-auth presents to a service's sign-in
// endpoints. The connector entry for the service must use it.
func LoginClientID(serviceID string) string { return "login-" + serviceID }

// SubjectPrefix is what mcp-auth puts before the opaque subject the service's
// sign-in returns. The login method for the service must use it as its
// subject_prefix, because the gateway stores values under the prefixed subject.
func SubjectPrefix(serviceID string) string { return "form-" + serviceID + ":" }

func (g *Gateway) loginProvider(s Service) *LoginProvider {
	resource, _ := url.Parse(s.ResourceURL)
	return NewLoginProvider(LoginConfig{
		ClientID:    LoginClientID(s.ID),
		Origin:      resource.Scheme + "://" + resource.Host,
		BasePath:    s.ResourcePath,
		CallbackURL: g.cfg.Issuer + "/identity/callback",
		Fields:      s.FormFields,
		Authenticate: func(ctx context.Context, values map[string]string) (string, error) {
			return g.authenticateForm(ctx, s, SubjectPrefix(s.ID), values)
		},
	})
}

// authenticateForm validates submitted values at the upstream and, only when it
// accepts them, keeps them in memory under subjectPrefix plus a fresh random
// subject. It returns the random part, which is what the sign-in hands to
// mcp-auth. Rejected values yield ErrRejected, an unreachable upstream
// ErrUnavailable; nothing is stored in either case.
func (g *Gateway) authenticateForm(ctx context.Context, s Service, subjectPrefix string, submitted map[string]string) (string, error) {
	values := make(map[string]string, len(s.FormFields))
	for _, f := range s.FormFields {
		values[f.Name] = submitted[f.Name]
	}
	rendered, err := render(s.Headers, values)
	if err != nil {
		return "", ErrRejected
	}
	if err := g.validateUpstream(ctx, s, rendered); err != nil {
		return "", err
	}
	random, err := NewSubject()
	if err != nil {
		return "", ErrUnavailable
	}
	g.store.Put(g.cfg.Issuer, subjectPrefix+random, s.ID, values)
	return random, nil
}

// render turns stored field values into the upstream headers, refusing any value
// that is not safe to send.
func render(headers []Header, values map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		value, ok := values[h.Field]
		if !ok || credentialheaders.ValidateInjectedValue(value) != nil {
			return nil, errors.New("invalid credential value")
		}
		out[h.Name] = value
	}
	return out, nil
}

// validateUpstream sends rendered headers to the service's validation URL.
// Response bodies are never logged or returned.
func (g *Gateway) validateUpstream(ctx context.Context, s Service, values map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ValidationURL, nil)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Cache-Control", "no-store")
	for name, value := range values {
		req.Header.Set(name, value)
	}
	resp, err := g.checker.Do(req)
	if err != nil {
		slog.Warn("credential gateway could not reach the upstream validation endpoint", "service", s.ID, "cause", transportCause(err))
		return ErrUnavailable
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("credential gateway upstream refused the validation request", "service", s.ID, "status", resp.StatusCode)
		return ErrRejected
	}
	return nil
}

// transportCause names why a request failed without quoting the error, which
// can contain the URL. It is for operators diagnosing an unreachable upstream.
func transportCause(err error) string {
	var dns *net.DNSError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var op *net.OpError
	switch {
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid):
		return "tls-certificate"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "connect-timeout"
		}
		return "connect-refused-or-unroutable"
	case errors.As(err, &op):
		return "network-" + op.Op
	default:
		return "transport"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, description string, extra map[string]string) {
	for k, v := range extra {
		w.Header().Set(k, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func (g *Gateway) mcpHandler(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodGet, http.MethodDelete:
		default:
			w.Header().Set("Allow", "POST, GET, DELETE")
			fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		g.handle(w, r, svc)
	}
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request, svc Service) {
	// 1. Authenticate this request on its own; initialize never authorises later calls.
	auth, failure := g.authz.Authorize(r.Context(), svc.ID, r.Header.Get("Authorization"))
	if failure != nil {
		fail(w, failure.Status, failure.Code, failure.Message, failure.Headers)
		return
	}
	id := auth.Identity

	// 2. Bound the body before touching stored values.
	if r.ContentLength > g.cfg.MaxRequestBytes {
		fail(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", nil)
		return
	}

	// 3. A session id is valid only for the user and service that opened it.
	var upstreamSession string
	if presented := r.Header.Get(sessionHeader); presented != "" {
		unwrapped, ok := g.binder.Unwrap(id.Issuer, id.Subject, svc.ID, presented)
		if !ok {
			fail(w, http.StatusForbidden, "session_binding_mismatch", "session does not belong to this caller", nil)
			return
		}
		upstreamSession = unwrapped
	} else if r.Method == http.MethodDelete {
		fail(w, http.StatusBadRequest, "missing_session", "Mcp-Session-Id is required", nil)
		return
	}

	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, g.cfg.MaxRequestBytes))
		if err != nil {
			fail(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", nil)
			return
		}
	}

	// 4. The values this sign-in stored for this service, and nothing else. They
	// are gone after a gateway restart or when they expire; the client is told to
	// authorize again, which is what brings the sign-in page back.
	stored, ok := g.store.Get(id.Issuer, id.Subject, svc.ID)
	if !ok {
		slog.Info("no stored credentials for this sign-in", "service", svc.ID)
		fail(w, http.StatusUnauthorized, "credentials_expired", "the stored credentials are gone; sign in again",
			mcpauth.UnauthorizedHeadersForError(g.metadataURL(svc), nil, "invalid_token", "sign in again"))
		return
	}
	injected, err := render(svc.Headers, stored)
	if err != nil {
		fail(w, http.StatusBadGateway, "credential_unavailable", "stored credentials could not be used", nil)
		return
	}

	// 5. Allowlisted client headers, then the injected credential headers.
	var reader io.Reader
	if len(body) > 0 {
		reader = strings.NewReader(string(body))
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, svc.UpstreamURL, reader)
	if err != nil {
		fail(w, http.StatusBadGateway, "upstream_invalid", "upstream request could not be built", nil)
		return
	}
	for _, name := range forwardedRequest {
		if v := r.Header.Get(name); v != "" {
			out.Header.Set(name, v)
		}
	}
	out.Header.Set("Accept-Encoding", "identity")
	if upstreamSession != "" {
		out.Header.Set(sessionHeader, upstreamSession)
	}
	for name, value := range injected {
		out.Header.Set(name, value)
	}

	resp, err := g.upstream.Do(out)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away
		}
		fail(w, http.StatusBadGateway, "upstream_unreachable", "upstream MCP server is unreachable", nil)
		return
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		fail(w, http.StatusBadGateway, "upstream_redirect", "upstream redirects are not followed", nil)
		return
	case resp.StatusCode == http.StatusUnauthorized:
		// The upstream no longer accepts what the person entered (a rotated or
		// revoked token). Forget it so the next request asks them to sign in again.
		g.store.Delete(id.Issuer, id.Subject, svc.ID)
		fail(w, http.StatusBadGateway, "upstream_credentials_rejected", "upstream rejected the stored credentials; sign in again", nil)
		return
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		fail(w, http.StatusBadGateway, "upstream_invalid", "upstream used an unsupported content encoding", nil)
		return
	}
	for _, name := range returnedResponse {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if returned := resp.Header.Get(sessionHeader); returned != "" {
		wrapped := g.binder.Wrap(id.Issuer, id.Subject, svc.ID, returned)
		if wrapped == "" {
			fail(w, http.StatusBadGateway, "upstream_invalid", "upstream returned an invalid session id", nil)
			return
		}
		w.Header().Set(sessionHeader, wrapped)
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)
	stream(r.Context(), w, resp.Body)
}

// stream copies the upstream body, flushing after every read so SSE events are
// delivered as they arrive. A client disconnect cancels the request context,
// which closes the upstream connection.
func stream(ctx context.Context, w http.ResponseWriter, body io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil || ctx.Err() != nil {
			return
		}
	}
}
