// Request authorization: verify the bearer, then ask mcp-auth for a
// credential-access assertion so revocation applies on the next request.
//
// the resource-server SDK (auth-client/go/mcpauth).
//
// For each request: verify the bearer token (signature, issuer, audience,
// expiry, scopes), then ask mcp-auth for a credential-access assertion.
// mcp-auth re-checks the resource client, the subject token and that the login
// session is still active, so revocation takes effect on the next request.
// Nothing is cached across requests. The assertion proves approval only; it
// contains no credentials.
package credential

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
)

const (
	// AssertionType is the RFC 8693 token type of a credential-access assertion.
	AssertionType = "urn:mcp-auth:token-type:credential-access-assertion"
	// AssertionJWTType is the JOSE typ of the assertion.
	AssertionJWTType = "credential-access+jwt"
)

// Identity is a verified (issuer, subject) pair.
type Identity struct{ Issuer, Subject string }

// Failure is an authorisation outcome the gateway turns into an HTTP response.
type Failure struct {
	Status  int
	Code    string
	Message string
	Headers map[string]string
}

func (f *Failure) Error() string { return f.Code }

// Result is a successful authorisation.
type Result struct {
	Identity      Identity
	SessionExpiry time.Time
}

type perService struct {
	svc       Service
	metadata  string
	verifier  *mcpauth.JWTVerifier
	assertion *mcpauth.JWTVerifier
	exchange  *mcpauth.TokenExchangeClient
}

// Authorizer authorises requests for every configured service.
type Authorizer struct {
	services map[string]*perService
	mu       sync.Mutex
	seen     map[string]time.Time
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("redirects are not followed")
}

// NewAuthorizer builds an Authorizer. metadataURL returns each service's
// protected-resource metadata URL.
func NewAuthorizer(cfg *Config, metadataURL func(Service) string) *Authorizer {
	client := &http.Client{Timeout: cfg.ASTimeout, CheckRedirect: refuseRedirect}
	a := &Authorizer{services: map[string]*perService{}, seen: map[string]time.Time{}}
	for _, s := range cfg.Services {
		required := map[string]bool{}
		for _, scope := range s.RequiredScopes {
			required[scope] = true
		}
		a.services[s.ID] = &perService{
			svc:      s,
			metadata: metadataURL(s),
			verifier: &mcpauth.JWTVerifier{JWKSURL: cfg.JWKSURI, Issuer: cfg.Issuer, Audience: s.ResourceURL,
				RequiredScopes: required, HTTPClient: client},
			assertion: &mcpauth.JWTVerifier{JWKSURL: cfg.JWKSURI, Issuer: cfg.Issuer, Audience: s.Client.ClientID,
				HTTPClient: client, TokenTypes: []string{AssertionJWTType}, RequireType: true},
			exchange: &mcpauth.TokenExchangeClient{
				Endpoint:           cfg.TokenEndpoint,
				HTTPClient:         client,
				ClientAuth:         &mcpauth.PrivateKeyJWTClientAuth{ClientID: s.Client.ClientID, PrivateKey: s.Client.PrivateKey, KeyID: s.Client.KeyID},
				AllowInsecure:      cfg.AllowInsecureHTTP,
				RequestedTokenType: AssertionType,
				// Cache deliberately nil: every request must be authorised afresh.
			},
		}
	}
	return a
}

func (a *Authorizer) consume(jti string, expires time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, exp := range a.seen {
		if !exp.After(now) {
			delete(a.seen, k)
		}
	}
	if _, dup := a.seen[jti]; dup {
		return false
	}
	a.seen[jti] = expires
	return true
}

func numeric(v any) (float64, bool) { f, ok := v.(float64); return f, ok }

// Authorize authorises one request for a service.
func (a *Authorizer) Authorize(ctx context.Context, serviceID, authorization string) (Result, *Failure) {
	p := a.services[serviceID]
	scopes := p.svc.RequiredScopes
	challenge := mcpauth.UnauthorizedHeaders(p.metadata, scopes)
	invalid := mcpauth.UnauthorizedHeadersForError(p.metadata, nil, "invalid_token", "token is invalid")
	denied := mcpauth.UnauthorizedHeadersForError(p.metadata, scopes, "insufficient_scope", "required scope is missing")

	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return Result{}, &Failure{401, "unauthorized", "authentication required", challenge}
	}
	token := parts[1]
	claims, err := p.verifier.VerifyContext(ctx, token)
	if err != nil {
		if strings.Contains(err.Error(), ": scope") {
			return Result{}, &Failure{403, "insufficient_scope", "required scope is missing", denied}
		}
		return Result{}, &Failure{401, "invalid_token", "token is invalid", invalid}
	}
	sid, _ := claims.Raw["sid"].(string)
	if sid == "" {
		return Result{}, &Failure{401, "invalid_token", "token has no session", invalid}
	}

	issued, err := p.exchange.ExchangeContext(ctx, token, p.svc.ResourceURL, nil)
	if err != nil {
		var ee *mcpauth.ExchangeError
		if errors.As(err, &ee) {
			switch ee.Status {
			case 400: // invalid_grant: revoked or ended session, or a rejected subject token
				return Result{}, &Failure{401, "invalid_token", "session is not active", invalid}
			case 403:
				return Result{}, &Failure{403, "insufficient_scope", "access denied", denied}
			case 401:
				// invalid_client: this gateway's own credentials at mcp-auth are wrong,
				// which is an operator problem, not the user's.
				slog.Error("mcp-auth rejected this gateway's resource client", "service", serviceID)
			}
		}
		return Result{}, &Failure{503, "authorization_unavailable", "authorization service error", nil}
	}
	bad := &Failure{502, "authorization_invalid", "authorization response rejected", nil}
	grant, err := p.assertion.VerifyContext(ctx, issued.AccessToken)
	if err != nil {
		return Result{}, bad
	}
	if grant.Subject != claims.Subject || grant.Raw["sid"] != sid ||
		grant.Raw["resource"] != p.svc.ResourceURL || grant.Raw["token_use"] != "credential_access" {
		return Result{}, bad
	}
	jti, _ := grant.Raw["jti"].(string)
	exp, ok := numeric(grant.Raw["exp"])
	if jti == "" || !ok || !a.consume(jti, time.Unix(int64(exp), 0)) {
		return Result{}, bad
	}
	sessionExpiry := time.Now()
	if e, ok := numeric(claims.Raw["exp"]); ok {
		sessionExpiry = time.Unix(int64(e), 0)
	}
	return Result{Identity: Identity{Issuer: grant.Issuer, Subject: grant.Subject}, SessionExpiry: sessionExpiry}, nil
}
