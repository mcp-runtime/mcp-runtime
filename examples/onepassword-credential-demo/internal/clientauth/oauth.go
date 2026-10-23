// Package clientauth connects a local MCP client through the broker's real
// OAuth flow. Desktop credentials are read before the MCP token is issued.
package clientauth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type Options struct {
	Resource, Issuer, Mode, Account, UsernameRef, PasswordRef string
	ApproveConsent                                            bool
	ShowURL                                                   func(string)
	HTTPClient                                                *http.Client
}

func hidden(body, name string) (string, error) {
	pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]+)"`)
	match := pattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return "", errors.New("expected broker login form field is missing")
	}
	return html.UnescapeString(match[1]), nil
}
func readField(ctx context.Context, account, reference string) (string, error) {
	if !strings.HasPrefix(reference, "op://") || strings.ContainsAny(reference, "\r\n") {
		return "", errors.New("configure op:// field references for the dedicated demo Login")
	}
	args := []string{"read", reference}
	if account != "" {
		args = append(args, "--account", account)
	}
	cmd := exec.CommandContext(ctx, "op", args...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "OP_SERVICE_ACCOUNT_TOKEN" || name == "OP_CONNECT_HOST" || name == "OP_CONNECT_TOKEN" || strings.HasPrefix(name, "OP_SESSION_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("1Password access failed; unlock the desktop app and enable CLI integration")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("the 1Password field is empty or unsuitable")
	}
	return value, nil
}
func New(options Options) (*auth.AuthorizationCodeHandler, func(), error) {
	if options.Mode == "" {
		options.Mode = "browser"
	}
	if options.Mode != "browser" && options.Mode != "desktop" {
		return nil, nil, errors.New("credential mode must be browser or desktop")
	}
	if options.Mode == "desktop" && !options.ApproveConsent {
		return nil, nil, errors.New("desktop mode requires --approve-consent to authorize this demo client for tools:read")
	}
	resourceURL, resourceErr := url.Parse(options.Resource)
	issuerURL, issuerErr := url.Parse(options.Issuer)
	if resourceErr != nil || resourceURL.Scheme != "https" || resourceURL.Host == "" || resourceURL.User != nil || resourceURL.RawQuery != "" || resourceURL.Fragment != "" || !strings.HasSuffix(resourceURL.Path, "/mcp") || issuerErr != nil || issuerURL.Scheme != "https" || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return nil, nil, errors.New("configure exact HTTPS gateway and issuer URLs without credentials, query or fragment")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	callback := "http://" + listener.Addr().String() + "/callback"
	results := make(chan *auth.AuthorizationResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("iss") != options.Issuer || query.Get("error") != "" || query.Get("code") == "" || query.Get("state") == "" {
			http.Error(w, "OAuth failed", 400)
			return
		}
		select {
		case results <- &auth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state")}:
			_, _ = io.WriteString(w, "OAuth complete. The upstream Login was validated before this MCP authorization.")
		default:
			http.Error(w, "authorization already received", 409)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	closeServer := func() { _ = server.Close() }
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL:                     callback,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{callback}, ClientName: "1Password upstream credential demo", TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: "tools:read"}},
		Client:                          httpClient,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			if options.Mode == "desktop" {
				if err := desktopLogin(ctx, options, args.URL, callback); err != nil {
					return nil, err
				}
			} else {
				if options.ShowURL == nil {
					return nil, errors.New("browser mode requires a sign-in URL handler")
				}
				options.ShowURL(args.URL)
			}
			select {
			case result := <-results:
				return result, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	if err != nil {
		closeServer()
		return nil, nil, err
	}
	return handler, closeServer, nil
}

// desktopLogin owns the whole broker consent/browser-cookie flow in one local
// HTTP client. It never submits credentials into another browser's login.
// Explicit consent is supplied by the owner choosing --approve-consent.
func desktopLogin(ctx context.Context, options Options, authorizationURL, callback string) error {
	issuer, err := url.Parse(options.Issuer)
	if err != nil {
		return errors.New("invalid issuer")
	}
	resource, err := url.Parse(options.Resource)
	if err != nil {
		return errors.New("invalid resource")
	}
	callbackURL, _ := url.Parse(callback)
	isCallback := func(u *url.URL) bool {
		return u != nil && u.Scheme == callbackURL.Scheme && u.Host == callbackURL.Host && u.EscapedPath() == callbackURL.EscapedPath() && u.User == nil && u.Fragment == ""
	}
	allowed := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && u.User == nil && u.Fragment == "" && ((u.Scheme == "https" && u.Host == issuer.Host) || (u.Scheme == "https" && u.Host == resource.Host) || isCallback(u))
	}
	if !allowed(authorizationURL) {
		return errors.New("unexpected authorization origin")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if options.HTTPClient != nil {
		client.Transport = options.HTTPClient.Transport
	}
	next := authorizationURL
	method := "GET"
	var form url.Values
	usedCredentials := false
	approved := false
	for range 12 {
		if !allowed(next) {
			return errors.New("authorization redirected outside the configured broker and gateway")
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, next, body)
		if err != nil {
			return errors.New("cannot prepare login request")
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			u, _ := url.Parse(next)
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("cannot reach broker or credential gateway")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if readErr != nil || len(data) > 65536 {
			return errors.New("invalid login response")
		}
		u, _ := url.Parse(next)
		if isCallback(u) {
			if resp.StatusCode != 200 {
				return errors.New("OAuth callback failed")
			}
			return nil
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location, err := resp.Location()
			if err != nil {
				return errors.New("invalid login redirect")
			}
			next = location.String()
			method = "GET"
			form = nil
			continue
		}
		if resp.StatusCode != 200 {
			return errors.New("upstream sign-in failed; no MCP token was issued")
		}
		if u.Host == issuer.Host && !approved {
			id, err := hidden(string(data), "consent_id")
			if err != nil {
				return err
			}
			next = strings.TrimRight(options.Issuer, "/") + "/authorize/consent"
			method = "POST"
			form = url.Values{"consent_id": {id}, "decision": {"approve"}}
			approved = true
			continue
		}
		if u.Host == resource.Host && u.Path == resource.Path+"/login/authorize" && !usedCredentials {
			id, err := hidden(string(data), "request")
			if err != nil {
				return err
			}
			csrf, err := hidden(string(data), "csrf")
			if err != nil {
				return err
			}
			username, err := readField(ctx, options.Account, options.UsernameRef)
			if err != nil {
				return err
			}
			password, err := readField(ctx, options.Account, options.PasswordRef)
			if err != nil {
				return err
			}
			next = options.Resource + "/login/approve"
			method = "POST"
			form = url.Values{"request": {id}, "csrf": {csrf}, "username": {username}, "password": {password}}
			usedCredentials = true
			continue
		}
		return fmt.Errorf("unexpected sign-in page; configure the resource-specific credential login method")
	}
	return errors.New("too many login redirects")
}
