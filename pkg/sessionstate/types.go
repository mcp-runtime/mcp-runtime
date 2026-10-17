// Package sessionstate defines the versioned platform-owned session sync
// contract. Gateways only read their authenticated server's state.
package sessionstate

import (
	"errors"
	"time"

	"mcp-runtime/pkg/policy"
)

const (
	GatewayAudience                    = "mcp-runtime-gateway"
	GatewayPort                        = 8085
	GatewayPath                        = "/internal/gateway/v1/sessions"
	AccessPath                         = "/internal/access/sessions"
	DefaultStaleWindow                 = 10 * time.Minute
	PollInterval                       = 500 * time.Millisecond
	ChangeLimit                        = 1000
	AdapterEnrollmentPendingAnnotation = "mcpruntime.org/adapter-enrollment-pending"
)

var (
	ErrReset          = errors.New("session sync cursor expired; relist required")
	ErrNotFound       = errors.New("session not found")
	ErrConflict       = errors.New("session already exists with different state")
	ErrInactiveAgent  = errors.New("session agent is inactive or belongs to another team")
	ErrInactiveHuman  = errors.New("session human account is inactive")
	ErrInvalidSession = errors.New("invalid session")
)

// Server is derived from a verified, Pod-bound service-account identity.
type Server struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// Session retains grant linkage and authority separately from the subject team
// in Binding. ID is the immutable namespace/name key; Binding.Name is local.
type Session struct {
	ID              string            `json:"id"`
	UID             string            `json:"uid"`
	Namespace       string            `json:"namespace"`
	ServerNamespace string            `json:"server_namespace"`
	ServerName      string            `json:"server_name"`
	ServerUID       string            `json:"server_uid"`
	GrantNamespace  string            `json:"grant_namespace,omitempty"`
	GrantName       string            `json:"grant_name,omitempty"`
	AuthorityTeamID string            `json:"authority_team_id,omitempty"`
	Binding         policy.Binding    `json:"binding"`
	CreatedAt       time.Time         `json:"created_at"`
	RevokedAt       *time.Time        `json:"revoked_at,omitempty"`
	RevocationCause string            `json:"revocation_cause,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
}

type Filter struct {
	HumanID        string `json:"human_id,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	ServerName     string `json:"server,omitempty"`
	ServerUID      string `json:"server_uid,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	GrantNamespace string `json:"grant_namespace,omitempty"`
	GrantName      string `json:"grant_name,omitempty"`
	After          string `json:"after,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type Page struct {
	Sessions   []Session `json:"sessions"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type Snapshot struct {
	Epoch    string    `json:"epoch"`
	Sequence int64     `json:"sequence"`
	Sessions []Session `json:"sessions"`
}

// Change carries tombstones for revocation/deletion. Kind reserves the channel
// for future grant changes without changing the session protocol.
type Change struct {
	Kind      string   `json:"kind"`
	Sequence  int64    `json:"sequence"`
	Operation string   `json:"operation"`
	ID        string   `json:"id"`
	Session   *Session `json:"session,omitempty"`
}

type Changes struct {
	Epoch    string   `json:"epoch"`
	Sequence int64    `json:"sequence"`
	Changes  []Change `json:"changes"`
	More     bool     `json:"more"`
}

type RevokeRequest struct {
	Revoked bool   `json:"revoked"`
	Cause   string `json:"cause,omitempty"`
}

// EnrollmentRequest identifies the immutable session whose enrollment completes.
type EnrollmentRequest struct {
	UID string `json:"uid"`
}

type BulkRevokeRequest struct {
	Filter Filter `json:"filter"`
	Cause  string `json:"cause"`
}

type BulkRevokeResponse struct {
	Count int64 `json:"count"`
}
