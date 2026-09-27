package v1alpha1

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPAccessGrantValidateRequiresToolDecision(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef: ServerReference{Name: "payments"},
			Subject:   SubjectRef{HumanID: "user-1"},
			ToolRules: []ToolRule{
				{Name: "refund_invoice"},
			},
		},
	}

	err := grant.validate()
	if err == nil {
		t.Fatal("expected validation error for missing tool rule decision")
	}
	if !strings.Contains(err.Error(), "toolRules[0].decision") {
		t.Fatalf("expected decision validation error, got %v", err)
	}
}

func TestMCPAccessGrantValidateRejectsInvalidAllowedSideEffect(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef:          ServerReference{Name: "payments"},
			Subject:            SubjectRef{HumanID: "user-1"},
			AllowedSideEffects: []ToolSideEffect{"read", "erase"},
		},
	}

	err := grant.validate()
	if err == nil {
		t.Fatal("expected validation error for invalid allowed side effect")
	}
	if !strings.Contains(err.Error(), "allowedSideEffects[1]") {
		t.Fatalf("expected allowedSideEffects validation error, got %v", err)
	}
}

func TestMCPAccessGrantValidateAllowsTeamOnlySubject(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef: ServerReference{Name: "payments"},
			Subject:   SubjectRef{TeamID: "team-acme"},
		},
	}

	if err := grant.validate(); err != nil {
		t.Fatalf("expected team-only subject to validate, got %v", err)
	}
}

func TestMCPAccessGrantValidateAllowsWildcardSubject(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef: ServerReference{Name: "payments"},
		},
	}

	if err := grant.validate(); err != nil {
		t.Fatalf("expected empty subject to remain a wildcard grant, got %v", err)
	}
}

func TestMCPAccessGrantValidateCreateWarnsOnWildcardSubject(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef: ServerReference{Name: "payments"},
		},
	}

	warnings, err := grant.ValidateCreate()
	if err != nil {
		t.Fatalf("expected wildcard grant to validate, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one wildcard warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "wildcard grant") {
		t.Fatalf("expected wildcard warning, got %q", warnings[0])
	}
}

func TestMCPAccessGrantValidateRejectsWhitespaceTeamID(t *testing.T) {
	grant := &MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant"},
		Spec: MCPAccessGrantSpec{
			ServerRef: ServerReference{Name: "payments"},
			Subject:   SubjectRef{TeamID: "team acme"},
		},
	}

	err := grant.validate()
	if err == nil {
		t.Fatal("expected validation error for whitespace teamID")
	}
	if !strings.Contains(err.Error(), "subject.teamID") {
		t.Fatalf("expected subject.teamID validation error, got %v", err)
	}
}

func TestMCPAgentSessionValidateAllowsExpiredSessionState(t *testing.T) {
	session := &MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session"},
		Spec: MCPAgentSessionSpec{
			ServerRef:      ServerReference{Name: "payments"},
			Subject:        SubjectRef{AgentID: "ops-agent"},
			ConsentedTrust: TrustLevelMedium,
			ExpiresAt:      &metav1.Time{Time: time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)},
		},
	}

	if err := session.validate(); err != nil {
		t.Fatalf("expired sessions should remain valid persisted state: %v", err)
	}
}

func TestMCPServerValidateRequiresToolSideEffect(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server"},
		Spec: MCPServerSpec{
			Image:            "example.com/server",
			PublicPathPrefix: "server",
			Tools: []ToolConfig{
				{Name: "read_file", RequiredTrust: TrustLevelLow},
			},
		},
	}

	err := server.validate()
	if err == nil {
		t.Fatal("expected validation error for missing tool sideEffect")
	}
	if !strings.Contains(err.Error(), "tools[0].sideEffect") {
		t.Fatalf("expected sideEffect validation error, got %v", err)
	}
}

func TestMCPServerValidatePublicPathPrefix(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server"},
		Spec: MCPServerSpec{
			Image:            "example.com/server",
			PublicPathPrefix: "///",
		},
	}

	err := server.validate()
	if err == nil {
		t.Fatal("expected validation error for invalid publicPathPrefix")
	}
	if !strings.Contains(err.Error(), "publicPathPrefix") {
		t.Fatalf("expected publicPathPrefix validation error, got %v", err)
	}
}

func TestMCPServerDefault(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
		Spec: MCPServerSpec{
			Image: "example.com/mcp-server",
		},
	}

	server.Default()

	if server.Spec.ImageTag != "latest" {
		t.Fatalf("expected imageTag default, got %q", server.Spec.ImageTag)
	}
	if server.Spec.Replicas == nil || *server.Spec.Replicas != 1 {
		t.Fatalf("expected replicas default, got %v", server.Spec.Replicas)
	}
	if server.Spec.Port != 8088 {
		t.Fatalf("expected port default, got %d", server.Spec.Port)
	}
	if server.Spec.ServicePort != 80 {
		t.Fatalf("expected service port default, got %d", server.Spec.ServicePort)
	}
	if server.Spec.IngressPath != "/test-server/mcp" {
		t.Fatalf("expected ingressPath default, got %q", server.Spec.IngressPath)
	}
	if server.Spec.PublicPathPrefix != "test-server" {
		t.Fatalf("expected publicPathPrefix default, got %q", server.Spec.PublicPathPrefix)
	}
	if server.Spec.IngressClass != "traefik" {
		t.Fatalf("expected ingressClass default, got %q", server.Spec.IngressClass)
	}
}

func TestMCPServerRejectsRemovedMTLSAuthMode(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
		Spec: MCPServerSpec{
			Image: "example.com/mcp-server",
			Auth:  &AuthConfig{Mode: AuthMode("mtls")},
			Gateway: &GatewayConfig{
				Enabled: true,
			},
		},
	}

	if err := server.validate(); err == nil || !strings.Contains(err.Error(), "auth.mode mtls was removed") {
		t.Fatalf("validate error = %v, want removed auth.mode migration error", err)
	}
}

func TestMCPServerDefaultWithOptions(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
		Spec: MCPServerSpec{
			Image:     "example.com/mcp-server",
			Gateway:   &GatewayConfig{Enabled: true},
			Analytics: &AnalyticsConfig{},
		},
	}

	server.DefaultWithOptions(MCPServerDefaultOptions{
		DefaultIngressHost:        "mcp.example.com",
		DefaultAnalyticsIngestURL: "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events",
	})

	if server.Spec.IngressHost != "mcp.example.com" {
		t.Fatalf("expected ingressHost default from options, got %q", server.Spec.IngressHost)
	}
	if server.Spec.Analytics == nil || server.Spec.Analytics.IngestURL != "http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events" {
		t.Fatalf("expected analytics ingest URL default from options, got %#v", server.Spec.Analytics)
	}
}

func TestMCPServerDefaultGatewayAuthTeamHeader(t *testing.T) {
	server := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
		Spec: MCPServerSpec{
			Image:   "example.com/mcp-server",
			Gateway: &GatewayConfig{Enabled: true},
		},
	}

	server.Default()

	if server.Spec.Auth == nil {
		t.Fatal("expected auth defaults")
	}
	if server.Spec.Auth.TeamIDHeader != defaultAuthTeamIDHeader {
		t.Fatalf("expected teamIDHeader default, got %q", server.Spec.Auth.TeamIDHeader)
	}
}

func TestMCPServerDefaultImageTagForHostPortImages(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "sets latest when hostport image has no tag",
			image: "10.43.109.51:5000/example-python-2025-11-25",
			want:  "latest",
		},
		{
			name:  "does not set imageTag when hostport image already has tag",
			image: "10.43.109.51:5000/example-python-2025-11-25:52c916f",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
				Spec: MCPServerSpec{
					Image: tt.image,
				},
			}

			server.Default()
			if server.Spec.ImageTag != tt.want {
				t.Fatalf("imageTag = %q, want %q", server.Spec.ImageTag, tt.want)
			}
		})
	}
}

func TestMCPServerValidateCanaryRollout(t *testing.T) {
	server := &MCPServer{
		Spec: MCPServerSpec{
			Image: "example.com/server",
			Rollout: &RolloutConfig{
				Strategy: RolloutStrategyCanary,
			},
		},
	}

	err := server.validate()
	if err == nil {
		t.Fatal("expected validation error for missing canaryReplicas")
	}
	if !strings.Contains(err.Error(), "canaryReplicas") {
		t.Fatalf("expected canaryReplicas validation error, got %v", err)
	}
	if !strings.Contains(err.Error(), "spec.replicas") {
		t.Fatalf("expected replicas validation error, got %v", err)
	}
}

func TestMCPServerValidateOAuthIssuer(t *testing.T) {
	server := &MCPServer{
		Spec: MCPServerSpec{
			Image:   "example.com/server",
			Gateway: &GatewayConfig{Enabled: true},
			Auth:    &AuthConfig{Mode: AuthModeOAuth},
		},
	}

	err := server.ValidateResolvedAuth()
	if err == nil {
		t.Fatal("expected validation error for missing OAuth issuer")
	}
	if !strings.Contains(err.Error(), "auth.issuerURL") {
		t.Fatalf("expected auth.issuerURL validation error, got %v", err)
	}
}

func TestMCPServerValidateRolloutValues(t *testing.T) {
	server := &MCPServer{
		Spec: MCPServerSpec{
			Image: "example.com/server",
			Rollout: &RolloutConfig{
				MaxUnavailable: "bad-value",
			},
		},
	}

	err := server.validate()
	if err == nil {
		t.Fatal("expected validation error for invalid rollout value")
	}
	if !strings.Contains(err.Error(), "rollout.maxUnavailable") {
		t.Fatalf("expected rollout.maxUnavailable validation error, got %v", err)
	}
}

func TestMCPServerValidateIngressRequirements(t *testing.T) {
	server := &MCPServer{
		Spec: MCPServerSpec{
			Image:       "example.com/server",
			IngressPath: "/server",
		},
	}

	err := server.validate()
	if err == nil {
		t.Fatal("expected validation error for missing ingressHost")
	}
	if !strings.Contains(err.Error(), "ingressHost") {
		t.Fatalf("expected ingressHost validation error, got %v", err)
	}

	server = &MCPServer{
		Spec: MCPServerSpec{
			Image:       "example.com/server",
			IngressHost: "example.com",
		},
	}
	err = server.validate()
	if err == nil {
		t.Fatal("expected validation error for missing ingressPath")
	}
	if !strings.Contains(err.Error(), "ingressPath") {
		t.Fatalf("expected ingressPath validation error, got %v", err)
	}
}

// Standalone OAuth resource servers (gateway disabled) validate their own
// tokens and are supported; only the removed mtls mode is rejected.
func TestMCPServerValidateAllowsStandaloneOAuthServer(t *testing.T) {
	server := &MCPServer{
		Spec: MCPServerSpec{
			Image:            "example.com/server",
			PublicPathPrefix: "server",
			Gateway:          &GatewayConfig{Enabled: false},
			Auth:             &AuthConfig{Mode: AuthModeOAuth, IssuerURL: "https://auth.example.com/mcp-auth", Audience: "https://mcp.example.com/server/mcp"},
		},
	}
	if err := server.validate(); err != nil {
		t.Fatalf("standalone OAuth server rejected: %v", err)
	}
}
