package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/golang-jwt/jwt/v4"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota

	"mcp-runtime/pkg/events"
	"mcp-runtime/pkg/serviceutil"
)

type eventWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type ingestServer struct {
	writer       eventWriter
	brokers      []string
	topic        string
	apiKeys      map[string]struct{}
	jwks         *keyfunc.JWKS
	oidcIssuer   string
	oidcAudience string
}

// main initializes and starts the MCP Sentinel Ingest service.
// It sets up Kafka producer connection, configures authentication, initializes tracing,
// sets up HTTP routes, and starts the server on the configured port.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	port := serviceutil.EnvOr("PORT", "8081")
	metricsPort := serviceutil.EnvOr("METRICS_PORT", "9091")
	brokers := strings.Split(serviceutil.EnvOr("KAFKA_BROKERS", "kafka:9092"), ",")
	topic := serviceutil.EnvOr("KAFKA_TOPIC", "mcp.events")

	apiKeys := map[string]struct{}{}
	ingestKeys := serviceutil.EnvOr("INGEST_API_KEYS", serviceutil.EnvOr("API_KEYS", ""))
	for _, key := range strings.Split(ingestKeys, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			apiKeys[key] = struct{}{}
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
	jwks := (*keyfunc.JWKS)(nil)
	if jwksURL != "" {
		var err error
		jwks, err = keyfunc.Get(jwksURL, keyfunc.Options{RefreshInterval: 10 * time.Minute})
		if err != nil {
			log.Fatalf("failed to load JWKS: %v", err)
		}
	}

	writer := newKafkaWriter(brokers, topic)

	server := &ingestServer{
		writer:       writer,
		brokers:      brokers,
		topic:        topic,
		apiKeys:      apiKeys,
		jwks:         jwks,
		oidcIssuer:   oidcIssuer,
		oidcAudience: oidcAudience,
	}

	if len(apiKeys) == 0 && jwks == nil {
		log.Printf("WARNING: ingest authentication is DISABLED — no INGEST_API_KEYS and no OIDC_JWKS_URL configured; /events will accept unauthenticated requests")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/ready", server.handleReady)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.Handle("/events", server.auth(http.HandlerFunc(server.handleEvents)))

	shutdown, err := serviceutil.InitTracer("mcp-ingest")
	if err != nil {
		log.Printf("otel init failed: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}()
	}

	metricsShutdown, metricsErrs := serviceutil.StartMetricsServer(metricsPort)
	go func() {
		if err, ok := <-metricsErrs; ok {
			log.Printf("metrics server stopped: %v", err)
		}
	}()

	log.Printf("mcp-ingest listening on :%s", port)
	requestMetrics := serviceutil.DefaultRequestMetrics()
	handler := otelhttp.NewHandler(serviceutil.LogRequests(requestMetrics.Middleware("mcp-ingest", mux)), "http.server")
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	_ = metricsShutdown(shutdownCtx)
	_ = writer.Close()
}

func newKafkaWriter(brokers []string, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		BatchTimeout: 200 * time.Millisecond,
		RequiredAcks: kafka.RequireAll,
	}
}

const ingestEventMaxBytes = 1 << 20 // 1MB

func (s *ingestServer) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.checkKafkaReady(ctx); err != nil {
		serviceutil.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "kafka_unavailable",
		})
		return
	}

	serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *ingestServer) checkKafkaReady(ctx context.Context) error {
	var lastErr error
	for _, broker := range s.brokers {
		broker = strings.TrimSpace(broker)
		if broker == "" {
			continue
		}
		conn, err := kafka.DialContext(ctx, "tcp", broker)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no Kafka brokers configured")
}

// handleEvents handles POST /events requests.
// It validates incoming MCP events, enriches them with metadata,
// and produces them to the configured Kafka topic.
// Returns success/error response based on validation and publishing status.
func (s *ingestServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, ingestEventMaxBytes)
	var payload events.Envelope
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		serviceutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}

	payload.Source = strings.TrimSpace(payload.Source)
	payload.EventType = strings.TrimSpace(payload.EventType)
	if err := payload.Validate(); err != nil {
		serviceutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_fields"})
		return
	}
	payload.EnsureTimestamp(time.Now().UTC())

	spanOpts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindProducer)}
	if s.topic != "" {
		spanOpts = append(spanOpts, trace.WithAttributes(attribute.String("kafka.topic", s.topic)))
	}
	writeCtx, span := otel.Tracer("mcp-ingest").Start(r.Context(), "kafka.produce", spanOpts...)
	payload.SetTraceID(serviceutil.TraceIDFromContext(writeCtx))

	raw, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.End()
		serviceutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode_failed"})
		return
	}
	err = s.writer.WriteMessages(writeCtx, kafka.Message{
		Value:   raw,
		Headers: serviceutil.InjectKafkaHeaders(writeCtx, nil),
	})
	if err != nil {
		span.RecordError(err)
		span.End()
		serviceutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue_failed"})
		return
	}
	span.End()

	serviceutil.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// auth is middleware that enforces API key authentication.
// It checks for x-api-key from INGEST_API_KEYS (or legacy API_KEYS) or supports optional OIDC JWT validation.
// If no API keys are configured, authentication is bypassed.
func (s *ingestServer) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.apiKeys) == 0 && s.jwks == nil {
			next.ServeHTTP(w, r)
			return
		}

		if len(s.apiKeys) > 0 {
			apiKey := strings.TrimSpace(r.Header.Get("x-api-key"))
			if apiKey != "" {
				if _, ok := s.apiKeys[apiKey]; ok {
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		token := serviceutil.ExtractBearer(r.Header.Get("authorization"))
		if token != "" && s.jwks != nil {
			parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}))
			parsed, err := parser.Parse(token, s.jwks.Keyfunc)
			if err == nil && parsed.Valid {
				if s.oidcIssuer == "" && s.oidcAudience == "" {
					serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
					return
				}
				if s.oidcIssuer != "" || s.oidcAudience != "" {
					claims, ok := parsed.Claims.(jwt.MapClaims)
					if !ok {
						serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
						return
					}
					if s.oidcIssuer != "" && claims["iss"] != s.oidcIssuer {
						serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
						return
					}
					if s.oidcAudience != "" {
						if !serviceutil.AudienceMatches(claims["aud"], s.oidcAudience) {
							serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
							return
						}
					}
				}
				next.ServeHTTP(w, r)
				return
			}
		}

		serviceutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}
