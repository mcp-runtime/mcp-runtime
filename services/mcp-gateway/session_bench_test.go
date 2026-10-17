package main

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"mcp-runtime/pkg/identity"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

// TLS already verified the ingress certificate before this per-request path.
// Crypto/handshake cost is measured separately by BenchmarkSessionMTLSTransport.
func BenchmarkSessionCertificateAuthorization(b *testing.B) {
	for _, count := range []int{1, 100, 10000} {
		for _, outcome := range []string{"live", "revoked", "expired", "stale", "foreign-spiffe"} {
			b.Run(fmt.Sprintf("sessions=%d/%s", count, outcome), func(b *testing.B) {
				cache := &gatewaySessionSync{sessions: map[string]sessionstate.Session{}, lastSync: time.Now(), staleWindow: time.Hour}
				for i := 0; i < count; i++ {
					row := testSyncSession(fmt.Sprintf("session-%d", i))
					cache.sessions[row.ID] = row
				}
				name := fmt.Sprintf("session-%d", count-1)
				row := cache.sessions["team/"+name]
				wantReason := ""
				switch outcome {
				case "revoked":
					row.Binding.Revoked, wantReason = true, "session_revoked"
				case "expired":
					row.Binding.ExpiresAt, wantReason = time.Now().Add(-time.Hour).Format(time.RFC3339), "session_expired"
				case "stale":
					cache.lastSync, wantReason = time.Now().Add(-2*time.Hour), "policy_unavailable"
				case "foreign-spiffe":
					wantReason = "invalid_spiffe_identity"
				}
				cache.sessions[row.ID] = row
				server := &gatewayServer{sessionSync: cache, trustedProxySPIFFE: "spiffe://benchmark.test/ingress"}
				doc := &policy.Document{Auth: &policy.Auth{TrustDomain: "benchmark.test"}, Session: &policy.Session{Store: "platform"}}
				request := httptest.NewRequest("POST", "https://localhost/mcp", nil)
				request.TLS = verifiedProxyTLS(server.trustedProxySPIFFE)
				trustDomain := "benchmark.test"
				if outcome == "foreign-spiffe" {
					trustDomain = "foreign.test"
				}
				request.Header.Set(defaultVerifiedSPIFFEHeader, identity.SessionSPIFFEID(trustDomain, "team", name))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					actor, reason := server.authenticateAdapterCertificate(request, doc)
					if reason != wantReason || reason == "" && actor.SessionID != name {
						b.Fatalf("actor=%+v reason=%q, want %q", actor, reason, wantReason)
					}
				}
			})
		}
	}
}
