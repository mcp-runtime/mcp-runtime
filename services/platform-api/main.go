package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/golang-jwt/jwt/v4"

	"mcp-platform-api/registry"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/registryauth"
	"mcp-runtime/pkg/serviceutil"
	"mcp-runtime/pkg/svcboot"
)

type apiServer struct {
	apiKeys           map[string]struct{}
	adminAPIKeys      map[string]struct{}
	adminUsers        map[string]struct{}
	jwks              *keyfunc.JWKS
	oidcIssuer        string
	oidcAudience      string
	userKeys          userAPIKeyStore
	registrySigner    *registryauth.Signer
	registryAuth      registryCredentialAuthenticator
	registryAuthz     *registry.AuthzConfig
	registryAuthzOnce sync.Once
	platform          *platformStore
	internalAuthToken string
	authentic         platformauth.Authenticator
}

const defaultPlatformStoreStartupTimeout = 90 * time.Second

type platformStoreOpener func(context.Context, string, []byte) (*platformStore, error)

var (
	platformStoreConnectAttemptTimeout = 10 * time.Second
	platformStoreConnectRetryInterval  = 2 * time.Second
)

func main() {
	if bootstrapOnly, ok := boolEnv("PLATFORM_ADMIN_BOOTSTRAP_ONLY"); ok && bootstrapOnly {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := runPlatformAdminBootstrap(ctx); err != nil {
			log.Fatalf("platform admin bootstrap failed: %v", err)
		}
		log.Printf("platform admin bootstrap complete")
		return
	}

	port := envOr("PORT", "8080")
	metricsPort := envOr("METRICS_PORT", "9090")

	apiKeys := svcboot.APIKeySet(envOr("API_KEYS", ""))
	adminAPIKeys := splitCSVSet(envOr("ADMIN_API_KEYS", ""))
	adminUsers := splitCSVSet(envOr("ADMIN_USERS", ""))
	for entry := range adminUsers {
		normalized := strings.ToLower(strings.TrimSpace(entry))
		if normalized != "" {
			adminUsers[normalized] = struct{}{}
		}
	}

	oidcIssuer := strings.TrimSpace(os.Getenv("OIDC_ISSUER"))
	oidcAudience := strings.TrimSpace(os.Getenv("OIDC_AUDIENCE"))
	jwksURL := strings.TrimSpace(os.Getenv("OIDC_JWKS_URL"))
	if oidcIssuer != "" || oidcAudience != "" || jwksURL != "" {
		if oidcIssuer == "" || oidcAudience == "" {
			log.Fatal("OIDC_ISSUER and OIDC_AUDIENCE are both required when OIDC authentication is configured")
		}
		if jwksURL == "" {
			var err error
			jwksURL, err = serviceutil.DiscoverOIDCJWKSURLWithRetry(context.Background(), oidcIssuer, 5, time.Second)
			if err != nil {
				log.Fatalf("OIDC_JWKS_URL is not configured and issuer discovery failed: %v", err)
			}
		}
	}
	var jwks *keyfunc.JWKS
	if jwksURL != "" {
		var err error
		jwks, err = keyfunc.Get(jwksURL, keyfunc.Options{RefreshInterval: 10 * time.Minute})
		if err != nil {
			log.Fatalf("failed to load JWKS: %v", err)
		}
	}

	server := &apiServer{
		apiKeys:           apiKeys,
		adminAPIKeys:      adminAPIKeys,
		adminUsers:        adminUsers,
		jwks:              jwks,
		oidcIssuer:        oidcIssuer,
		oidcAudience:      oidcAudience,
		registryAuthz:     registry.NewAuthzConfigFromEnv(),
		internalAuthToken: strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN")),
	}

	var store *platformStore
	if dsn := platformDSNFromEnv(); dsn != "" {
		startupTimeout := serviceutil.EnvDuration("PLATFORM_DB_STARTUP_TIMEOUT", defaultPlatformStoreStartupTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
		defer cancel()
		jwtSecret, err := jwtSecretFromEnv()
		if err != nil {
			log.Fatal(err.Error())
		}
		store, err = openPlatformStoreWithRetry(ctx, dsn, jwtSecret, newPlatformStore)
		if err != nil {
			log.Fatalf("failed to initialize platform identity database: %v", err)
		}
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer seedCancel()
		if err := seedPlatformAdminFromEnv(seedCtx, store); err != nil {
			log.Fatalf("failed to seed platform admin: %v", err)
		}
		if err := seedPlatformDevUsersFromEnv(seedCtx, store); err != nil {
			log.Fatalf("failed to seed platform dev users: %v", err)
		}
		server.platform = store
		server.userKeys = store
		server.registryAuth = store
	}

	jwtSecret, err := jwtSecretFromEnv()
	if err != nil {
		log.Fatal(err.Error())
	}
	var userResolver platformauth.UserKeyResolver
	if store != nil {
		userResolver = platformauth.ChainUserKeyResolvers(
			storeAPIKeyResolver{store: store},
			runtimeControlUserKeyResolver(),
		)
	} else {
		userResolver = runtimeControlUserKeyResolver()
	}
	server.authentic = platformauth.Authenticator{
		Secret:          jwtSecret,
		Audience:        platformauth.AudiencePlatform,
		ServiceAPIKeys:  apiKeys,
		AdminAPIKeys:    adminAPIKeys,
		UserKeyResolver: userResolver,
		OIDC:            server.oidcVerifier(),
	}

	keyPath, certPath := os.Getenv("REGISTRY_TOKEN_SIGNING_KEY_FILE"), os.Getenv("REGISTRY_TOKEN_SIGNING_CERT_FILE")
	if keyPath != "" || certPath != "" {
		key, err := os.ReadFile(keyPath) // #nosec G304 G703 -- operator-configured mounted signing Secret path, never request input.
		if err != nil {
			log.Fatal("registry signing key unavailable")
		}
		cert, err := os.ReadFile(certPath) // #nosec G304 G703 -- operator-configured mounted public certificate path, never request input.
		if err != nil {
			log.Fatal("registry signing certificate unavailable")
		}
		server.registrySigner, err = registryauth.NewSigner(key, cert)
		if err != nil {
			log.Fatal("registry signing material invalid")
		}
	}

	mux := http.NewServeMux()
	server.registerRoutes(mux)

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-platform-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(context.Context) error {
			if store != nil {
				return store.Close()
			}
			return nil
		},
	}); err != nil {
		log.Fatal(err)
	}
}

type storeAPIKeyResolver struct {
	store *platformStore
}

func (r storeAPIKeyResolver) ResolveAPIKey(ctx context.Context, rawKey string) (platformauth.Principal, bool, error) {
	if r.store == nil {
		return platformauth.Principal{}, false, nil
	}
	p, ok, err := r.store.AuthenticateUserAPIKey(ctx, rawKey)
	return platformauth.Principal(p), ok, err
}

func runtimeControlUserKeyResolver() platformauth.UserKeyResolver {
	baseURL := strings.TrimSpace(os.Getenv("RUNTIME_CONTROL_URL"))
	if baseURL == "" {
		baseURL = "http://mcp-runtime-api.mcp-platform.svc.cluster.local:8084"
	}
	token := strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN"))
	if token == "" {
		return nil
	}
	return &platformauth.HTTPUserKeyResolver{
		BaseURL: baseURL,
		Token:   token,
	}
}

func (s *apiServer) oidcVerifier() platformauth.OIDCVerifier {
	if s.jwks == nil {
		return nil
	}
	return oidcPrincipalVerifier{
		server: s,
	}
}

type oidcPrincipalVerifier struct {
	server *apiServer
}

func (v oidcPrincipalVerifier) Verify(ctx context.Context, token string) (platformauth.Principal, bool, error) {
	p, ok, err := v.server.authenticateOIDCToken(ctx, token)
	return platformauth.Principal(p), ok, err
}

func (s *apiServer) authenticateOIDCToken(ctx context.Context, token string) (principal, bool, error) {
	if s.jwks == nil {
		return principal{}, false, nil
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}))
	parsed, err := parser.Parse(token, s.jwks.Keyfunc)
	if err != nil || !parsed.Valid {
		return principal{}, false, nil
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return principal{}, false, nil
	}
	if s.oidcIssuer == "" || s.oidcAudience == "" {
		return principal{}, false, nil
	}
	if claims["iss"] != s.oidcIssuer {
		return principal{}, false, nil
	}
	if !serviceutil.AudienceMatches(claims["aud"], s.oidcAudience) {
		return principal{}, false, nil
	}
	sub := strings.TrimSpace(fmt.Sprint(claims["sub"]))
	email := strings.ToLower(strings.TrimSpace(fmt.Sprint(claims["email"])))
	role := roleUser
	if _, ok := s.adminUsers[sub]; ok {
		role = roleAdmin
	}
	if email != "" {
		if _, ok := s.adminUsers[email]; ok {
			role = roleAdmin
		}
	}
	if s.platform != nil {
		u, err := s.platform.EnsureOIDCUser(ctx, oidcProvider(s.oidcIssuer), sub, email, role)
		if err != nil {
			return principal{}, false, err
		}
		p, err := s.platform.PrincipalForUserID(ctx, u.ID)
		if err != nil {
			return principal{}, false, err
		}
		p.AuthType = "oidc_jwt"
		return p, true, nil
	}
	return principal{Role: role, Subject: sub, Email: email, AuthType: "oidc_jwt"}, true, nil
}

func (s *apiServer) authenticateRequest(r *http.Request) (principal, bool, error) {
	p, ok, err := s.authentic.AuthenticateRequest(r)
	return principal(p), ok, err
}

func openPlatformStoreWithRetry(ctx context.Context, dsn string, jwtSecret []byte, open platformStoreOpener) (*platformStore, error) {
	if open == nil {
		open = newPlatformStore
	}
	var lastErr error
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, platformStoreConnectAttemptTimeout)
		store, err := open(attemptCtx, dsn, jwtSecret)
		cancel()
		if err == nil {
			if attempt > 1 {
				log.Printf("platform identity database initialized after %d attempt(s)", attempt)
			}
			return store, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, lastErr
		}
		log.Printf("platform identity database not ready (attempt %d): %v", attempt, err)
		timer := time.NewTimer(platformStoreConnectRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, lastErr
		case <-timer.C:
		}
	}
}

func boolEnv(key string) (bool, bool)   { return serviceutil.BoolEnv(key) }
func envOr(key, fallback string) string { return serviceutil.EnvOr(key, fallback) }

func splitCSVSet(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out[part] = struct{}{}
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	serviceutil.WriteJSON(w, status, payload)
}

func oidcProvider(issuer string) string {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return oidcProviderPrefix + "default"
	}
	return oidcProviderPrefix + issuer
}
