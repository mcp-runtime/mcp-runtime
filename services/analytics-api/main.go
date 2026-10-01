package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	_ "go.uber.org/automaxprocs"

	"mcp-analytics-api/internal/analytics"
	"mcp-analytics-api/internal/identity"
	"mcp-analytics-api/internal/usage"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/serviceutil"
	"mcp-runtime/pkg/svcboot"
)

type server struct {
	usage     *usage.Service
	events    *analytics.Handler
	authentic platformauth.Authenticator
}

func main() {
	port := serviceutil.EnvOr("PORT", "8085")
	metricsPort := serviceutil.EnvOr("METRICS_PORT", "9095")
	clickhouseAddr := serviceutil.EnvOr("CLICKHOUSE_ADDR", "clickhouse:9000")
	dbName := serviceutil.EnvOr("CLICKHOUSE_DB", "mcp")
	if err := clickhousepkg.ValidateDBName(dbName); err != nil {
		log.Fatalf("invalid CLICKHOUSE_DB: %v", err)
	}

	jwtSecret, err := jwtSecretFromEnv()
	if err != nil {
		log.Fatal(err.Error())
	}

	conn, err := chdriver.Open(&chdriver.Options{
		Addr:        []string{clickhouseAddr},
		Auth:        chdriver.Auth{Database: dbName},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("failed to connect to clickhouse: %v", err)
	}

	platformAPIURL := strings.TrimSpace(os.Getenv("PLATFORM_API_URL"))
	if platformAPIURL == "" {
		platformAPIURL = "http://mcp-platform-api.mcp-platform.svc.cluster.local:8080"
	}
	internalToken := strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN"))

	srv := &server{
		usage: &usage.Service{
			DB:     conn,
			DBName: dbName,
			Resolver: &identity.HTTPResolver{
				BaseURL: platformAPIURL,
				Token:   internalToken,
			},
		},
		events: analytics.NewHandler(&clickhousepkg.Client{Conn: conn, DBName: dbName}),
		authentic: platformauth.Authenticator{
			Secret:         jwtSecret,
			Audience:       platformauth.AudienceAnalytics,
			ServiceAPIKeys: serviceAPIKeysFromEnv(),
			AdminAPIKeys:   adminAPIKeysFromEnv(),
			UserKeyResolver: &platformauth.HTTPUserKeyResolver{
				BaseURL: platformAPIURL,
				Token:   internalToken,
			},
		},
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-analytics-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(context.Context) error {
			return conn.Close()
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func jwtSecretFromEnv() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if secret == "" {
		return nil, errors.New("JWT_SECRET is required")
	}
	return []byte(secret), nil
}

func serviceAPIKeysFromEnv() map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range strings.Split(serviceutil.EnvOr("API_KEYS", ""), ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func adminAPIKeysFromEnv() map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range strings.Split(serviceutil.EnvOr("ADMIN_API_KEYS", ""), ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}
