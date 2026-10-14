package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/golang-jwt/jwt/v4"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"mcp-runtime/pkg/events"
)

func TestHandleEventsRejectsWhitespaceOnlyRequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "source",
			body: `{"source":"   ","event_type":"tool.call","payload":{"ok":true}}`,
		},
		{
			name: "event type",
			body: `{"source":"mcp-gateway","event_type":"   ","payload":{"ok":true}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := &ingestServer{}
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(tc.body))
			recorder := httptest.NewRecorder()

			server.handleEvents(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if !strings.Contains(recorder.Body.String(), `"error":"missing_fields"`) {
				t.Fatalf("body = %q, want missing_fields", recorder.Body.String())
			}
		})
	}
}

func TestIngestPreservesCrossTeamAuthorityAndActor(t *testing.T) {
	writer := &recordingEventWriter{}
	server := &ingestServer{writer: writer, topic: "mcp.events"}
	body := `{"source":"server-a-gateway","event_type":"mcp.request","payload":{"team_id":"resource-a","resource_team_id":"resource-a","subject_team_id":"actor-b","namespace":"team-a","trace_marker":"cross-team"}}`
	rec := httptest.NewRecorder()
	server.handleEvents(rec, httptest.NewRequest("POST", "/events", strings.NewReader(body)))
	if rec.Code != 202 || len(writer.messages) != 1 {
		t.Fatalf("status=%d messages=%d", rec.Code, len(writer.messages))
	}
	var envelope events.Envelope
	if err := json.Unmarshal(writer.messages[0].Value, &envelope); err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["subject_team_id"] != "actor-b" || payload["resource_team_id"] != "resource-a" || payload["team_id"] != "resource-a" || payload["namespace"] != "team-a" {
		t.Fatalf("actor/authority conflated in Kafka transport: %v", payload)
	}
}

func TestHandleReadyWithoutKafkaReturnsServiceUnavailable(t *testing.T) {
	t.Parallel()

	server := &ingestServer{}
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()

	server.handleReady(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), `"error":"kafka_unavailable"`) {
		t.Fatalf("body = %q, want kafka_unavailable", recorder.Body.String())
	}
}

func TestKafkaWriterRequiresAllReplicas(t *testing.T) {
	t.Parallel()

	writer := newKafkaWriter([]string{"kafka:9092"}, "mcp.events")
	if writer.RequiredAcks != kafka.RequireAll {
		t.Fatalf("RequiredAcks = %v, want kafka.RequireAll", writer.RequiredAcks)
	}
}

func TestHandleEventsPropagatesTraceContextToKafka(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(previous)
	})

	traceID := trace.TraceID{32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     trace.SpanID{2, 4, 6, 8, 10, 12, 14, 16},
		TraceFlags: trace.FlagsSampled,
	}))

	writer := &recordingEventWriter{}
	server := &ingestServer{writer: writer, topic: "mcp.events"}
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(`{"source":"mcp-gateway","event_type":"mcp.request","payload":{"ok":true,"team_id":"resource-a","resource_team_id":"resource-a","subject_team_id":"actor-b"}}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()

	server.handleEvents(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if len(writer.messages) != 1 {
		t.Fatalf("written messages = %d, want 1", len(writer.messages))
	}
	traceparent := kafkaHeaderValue(writer.messages[0].Headers, "traceparent")
	if !strings.Contains(traceparent, traceID.String()) {
		t.Fatalf("traceparent = %q, want trace ID %s", traceparent, traceID)
	}
	var written struct {
		TraceID string `json:"trace_id"`
		Payload struct {
			TeamID         string `json:"team_id"`
			ResourceTeamID string `json:"resource_team_id"`
			SubjectTeamID  string `json:"subject_team_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(writer.messages[0].Value, &written); err != nil {
		t.Fatalf("unmarshal written event: %v", err)
	}
	if written.TraceID != traceID.String() {
		t.Fatalf("stored trace_id = %q, want %s", written.TraceID, traceID)
	}
	if written.Payload.TeamID != "resource-a" || written.Payload.ResourceTeamID != "resource-a" || written.Payload.SubjectTeamID != "actor-b" {
		t.Fatalf("cross-team attribution changed while propagating trace: %+v", written.Payload)
	}
}

func TestAuthRejectsJWKSOnlyBearerToken(t *testing.T) {
	t.Parallel()

	issuer := newTestIngestJWTIssuer(t)
	jwks, err := keyfunc.Get(issuer.server.URL+"/keys", keyfunc.Options{})
	if err != nil {
		t.Fatalf("keyfunc.Get() error = %v", err)
	}
	server := &ingestServer{jwks: jwks}
	called := false
	handler := server.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Header.Set("Authorization", "Bearer "+issuer.sign(t, jwt.MapClaims{
		"iss": "https://issuer.example.com",
		"aud": "mcp-runtime",
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("handler should not be called when issuer and audience are both unset")
	}
}

type recordingEventWriter struct {
	messages []kafka.Message
}

func (w *recordingEventWriter) WriteMessages(_ context.Context, messages ...kafka.Message) error {
	w.messages = append(w.messages, messages...)
	return nil
}

func kafkaHeaderValue(headers []kafka.Header, key string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Key, key) {
			return string(header.Value)
		}
	}
	return ""
}

type testIngestJWTIssuer struct {
	privateKey *rsa.PrivateKey
	server     *httptest.Server
}

func newTestIngestJWTIssuer(t *testing.T) *testIngestJWTIssuer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	issuer := &testIngestJWTIssuer{privateKey: privateKey}
	issuer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{ingestRSAJWK(&privateKey.PublicKey)},
		})
	}))
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *testIngestJWTIssuer) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(i.privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func ingestRSAJWK(publicKey *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
	}
}
