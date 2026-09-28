// Package policy provides shared gateway policy types used by both the operator
// and the MCP proxy. This ensures contract compatibility between the operator-rendered
// policy and the proxy-consumed policy.
package policy

import "strings"

// SchemaVersion is the current gateway policy contract schema version. It is
// document-level metadata distinct from the authorization PolicyVersion: it
// identifies the compatibility of the rendered JSON contract itself, not the
// grant/session policy generation. Bump it only when the rendered JSON shape
// changes in a way the consumer must understand.
const SchemaVersion = "v1"

// SchemaVersionGrantExpiry is stamped on documents in which any grant sets
// expires_at. Grant expiry is a field the consumer must understand: a gateway
// that predates it would decode the document, drop the unknown field, and keep
// honoring expired grants. Under v2 such a gateway rejects the document and
// reports the reload failure instead. Documents without expiring grants stay
// at SchemaVersion so they remain readable by older gateways.
const SchemaVersionGrantExpiry = "v2"

// supportedSchemaVersions enumerates the schema versions a consumer is able to
// activate. Documents carrying any other version fail validation and are
// rejected before activation.
var supportedSchemaVersions = map[string]struct{}{
	SchemaVersion:            {},
	SchemaVersionGrantExpiry: {},
}

// RequiredSchemaVersion returns the lowest schema version that can carry doc.
func RequiredSchemaVersion(doc *Document) string {
	if doc != nil {
		for _, grant := range doc.Grants {
			if strings.TrimSpace(grant.ExpiresAt) != "" {
				return SchemaVersionGrantExpiry
			}
		}
	}
	return SchemaVersion
}

// ServerName identifies an MCP server in a rendered gateway policy.
type ServerName string

// Namespace identifies the Kubernetes namespace that owns policy resources.
type Namespace string

// HumanID identifies an authenticated human principal.
type HumanID string

// AgentID identifies an authenticated agent or OAuth client principal.
type AgentID string

// TeamID identifies a stable platform team principal.
type TeamID string

// SessionID identifies an MCP agent session binding.
type SessionID string

// ToolName identifies an MCP tool in a rendered gateway policy.
type ToolName string

// Document is the root gateway policy document that contains all policy configuration.
type Document struct {
	// SchemaVersion identifies the compatibility of the rendered JSON contract.
	SchemaVersion string `json:"schema_version"`
	// Revision is a deterministic SHA-256 digest of the canonical rendered
	// policy content. It is computed with SchemaVersion included and with
	// Revision and GeneratedAt excluded, so identical policy content always
	// produces the same revision regardless of when it was generated.
	Revision string `json:"revision"`
	// GeneratedAt is informational only and must not affect Revision.
	GeneratedAt string    `json:"generated_at,omitempty"`
	Server      Server    `json:"server"`
	Auth        *Auth     `json:"auth,omitempty"`
	Policy      *Config   `json:"policy,omitempty"`
	Session     *Session  `json:"session,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	Grants      []Grant   `json:"grants,omitempty"`
	Sessions    []Binding `json:"sessions,omitempty"`
}

// Server identifies the MCP server this policy applies to.
type Server struct {
	Name      ServerName `json:"name"`
	Namespace Namespace  `json:"namespace"`
	TeamID    TeamID     `json:"team_id,omitempty"`
	Cluster   string     `json:"cluster,omitempty"`
}

// Auth configures authentication settings for the gateway.
type Auth struct {
	TokenHeader string `json:"token_header,omitempty"`
	IssuerURL   string `json:"issuer_url,omitempty"`
	Audience    string `json:"audience,omitempty"`
	TrustDomain string `json:"trust_domain,omitempty"`
}

// Config contains policy enforcement configuration.
type Config struct {
	Mode            string `json:"mode,omitempty"`
	DefaultDecision string `json:"default_decision,omitempty"`
	EnforceOn       string `json:"enforce_on,omitempty"`
	PolicyVersion   string `json:"policy_version,omitempty"`
}

// Session configures session management settings.
type Session struct {
	Required            bool   `json:"required,omitempty"`
	Store               string `json:"store,omitempty"`
	MaxLifetime         string `json:"max_lifetime,omitempty"`
	IdleTimeout         string `json:"idle_timeout,omitempty"`
	UpstreamTokenHeader string `json:"upstream_token_header,omitempty"`
}

// Tool describes an MCP tool and its trust requirements.
type Tool struct {
	Name          ToolName          `json:"name"`
	Description   string            `json:"description,omitempty"`
	RequiredTrust string            `json:"required_trust,omitempty"`
	SideEffect    string            `json:"side_effect,omitempty"`
	RiskLevel     string            `json:"risk_level,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// Grant defines access grants for subjects (humans/agents).
type Grant struct {
	Name               string       `json:"name"`
	Namespace          Namespace    `json:"namespace,omitempty"`
	HumanID            HumanID      `json:"human_id,omitempty"`
	AgentID            AgentID      `json:"agent_id,omitempty"`
	TeamID             TeamID       `json:"team_id,omitempty"`
	MaxTrust           string       `json:"max_trust,omitempty"`
	AllowedSideEffects []string     `json:"allowed_side_effects,omitempty"`
	PolicyVersion      string       `json:"policy_version,omitempty"`
	Disabled           bool         `json:"disabled,omitempty"`
	ExpiresAt          string       `json:"expires_at,omitempty"`
	ToolRules          []ToolAccess `json:"tool_rules,omitempty"`
}

// Binding represents an agent session binding.
type Binding struct {
	Name             SessionID `json:"name"`
	Namespace        Namespace `json:"namespace,omitempty"`
	HumanID          HumanID   `json:"human_id,omitempty"`
	AgentID          AgentID   `json:"agent_id,omitempty"`
	TeamID           TeamID    `json:"team_id,omitempty"`
	ConsentedTrust   string    `json:"consented_trust,omitempty"`
	Revoked          bool      `json:"revoked,omitempty"`
	ExpiresAt        string    `json:"expires_at,omitempty"`
	PolicyVersion    string    `json:"policy_version,omitempty"`
	UpstreamTokenRef string    `json:"upstream_token_ref,omitempty"`
}

// ToolAccess defines access rules for a specific tool.
type ToolAccess struct {
	Name          ToolName `json:"name"`
	Decision      string   `json:"decision,omitempty"`
	RequiredTrust string   `json:"required_trust,omitempty"`
}

func ToolRiskLevel(policy *Document, toolName string) string {
	if policy == nil || toolName == "" {
		return ""
	}
	for _, tool := range policy.Tools {
		if string(tool.Name) != toolName {
			continue
		}
		return NormalizeRiskLevel(tool.RiskLevel, tool.RequiredTrust, tool.SideEffect)
	}
	return ""
}
