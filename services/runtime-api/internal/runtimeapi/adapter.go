package runtimeapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	sentinelaccess "mcp-runtime/pkg/access"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
)

// Adapter-session bounds: the issued session's lifetime is constrained so a
// compromised cached identity has a bounded blast radius. The defaults are
// generous enough for a long-running agent process to avoid hot-path refreshes
// but short enough to cycle credentials within a working day.
const (
	adapterSessionDefaultTTL = time.Hour
	adapterSessionMaxTTL     = 24 * time.Hour
	// adapterSessionRefreshBuffer is the minimum remaining lifetime an existing
	// session must have to be reused. Anything under this triggers a fresh
	// session so the caller does not race expiry mid-conversation.
	adapterSessionRefreshBuffer     = 30 * time.Second
	adapterSessionRequestMaxBytes   = 16 << 10
	adapterGrantNameAnnotation      = "mcpruntime.org/access-grant-name"
	adapterGrantNamespaceAnnotation = "mcpruntime.org/access-grant-namespace"
)

// adapterSessionRequest is the input contract for POST /api/runtime/adapter/sessions.
type adapterSessionRequest struct {
	ServerName     string `json:"serverName"`
	Namespace      string `json:"namespace"`
	AgentID        string `json:"agentID"`
	RequestedTrust string `json:"requestedTrust,omitempty"`
	RequestedTTL   string `json:"requestedTTL,omitempty"`
}

// adapterSessionResponse is the body returned on success.
type adapterSessionResponse struct {
	Name           string    `json:"name"`
	Namespace      string    `json:"namespace"`
	HumanID        string    `json:"humanID"`
	AgentID        string    `json:"agentID"`
	TeamID         string    `json:"teamID,omitempty"`
	ServerName     string    `json:"serverName"`
	ConsentedTrust string    `json:"consentedTrust"`
	PolicyVersion  string    `json:"policyVersion"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Reused         bool      `json:"reused"`
	TrustDomain    string    `json:"trustDomain,omitempty"`
}

// HandleAdapterSession issues (or reuses) an MCPAgentSession for an adapter
// call. The platform — not the adapter — picks the matching grant, caps the
// trust at the grant's ceiling, and writes the session resource. The adapter
// then uses the returned session to enroll a certificate for every
// request to the runtime gateway.
//
// Errors:
//   - 400 when body decoding or input validation fails
//   - 401 when no Principal is on the request (auth middleware should reject first)
//   - 403 when no matching enabled grant is found, or the principal lacks the team
//   - 503 when Kubernetes is unavailable
func (s *AccessService) HandleAdapterSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	var req adapterSessionRequest
	r.Body = http.MaxBytesReader(w, r.Body, adapterSessionRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.ServerName = strings.TrimSpace(req.ServerName)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.AgentID = strings.TrimSpace(req.AgentID)

	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "no principal on request")
		return
	}

	if req.ServerName == "" {
		writeAPIError(w, http.StatusBadRequest, "serverName is required")
		return
	}
	if req.AgentID == "" {
		writeAPIError(w, http.StatusBadRequest, "agentID is required")
		return
	}
	if req.Namespace == "" {
		// Default to the principal's primary namespace so single-team callers
		// don't have to pass it on every request.
		req.Namespace = strings.TrimSpace(principal.Namespace)
	}
	if req.Namespace == "" {
		writeAPIError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	humanID := strings.TrimSpace(principal.Subject)
	if humanID == "" {
		humanID = strings.TrimSpace(principal.Email)
	}
	if humanID == "" {
		writeAPIError(w, http.StatusUnauthorized, "principal has no subject or email")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	teamIDs := adapterPrincipalTeamIDs(principal)
	defaultTeamID := defaultAdapterSessionTeamID(principal, req.Namespace, teamIDs)
	if s.identity == nil || !s.identity.Configured() {
		writeAPIError(w, http.StatusServiceUnavailable, "platform identity is unavailable for adapter validation")
		return
	}
	agent, found, err := s.identity.GetAgent(ctx, req.AgentID)
	if err != nil {
		log.Printf("adapter session: resolve agent %q failed: %v", req.AgentID, err)
		writeAPIError(w, http.StatusServiceUnavailable, "failed to validate adapter agent")
		return
	}
	if !found || strings.TrimSpace(agent.ID) != req.AgentID || strings.TrimSpace(agent.Status) != "active" {
		writeAPIError(w, http.StatusForbidden, "adapter agent is unknown or inactive")
		return
	}
	if !containsString(teamIDs, strings.TrimSpace(agent.TeamID)) {
		writeAPIError(w, http.StatusForbidden, "adapter agent does not belong to a caller team")
		return
	}

	requestedTrust, err := parseAdapterTrust(req.RequestedTrust)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	requestedTTL, err := parseAdapterTTL(req.RequestedTTL)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	grant, teamID, err := s.selectAdapterGrant(ctx, req.Namespace, req.ServerName, humanID, req.AgentID, teamIDs, defaultTeamID, false)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := requireActiveAgent(ctx, s.identity, req.AgentID, teamID); err != nil {
		writeAgentDirectoryError(w, err)
		return
	}

	consentedTrust := capTrust(requestedTrust, grant.Spec.MaxTrust)
	policyVersion := runtimeaccess.DefaultPolicyVersion(grant.Spec.PolicyVersion)
	sessionName := adapterSessionName(humanID, req.AgentID, teamID, req.ServerName)
	expiresAt := time.Now().UTC().Add(requestedTTL)
	if grant.Spec.ExpiresAt != nil && grant.Spec.ExpiresAt.Before(&metav1.Time{Time: expiresAt}) {
		expiresAt = grant.Spec.ExpiresAt.Time.UTC()
	}

	// Reuse an existing session when its identity, policy version, and trust
	// still match and it has enough remaining lifetime to be useful.
	existing, _ := s.accessMgr.GetSession(ctx, sessionName, req.Namespace)
	if existing != nil && adapterSessionReusable(existing, policyVersion, consentedTrust) && adapterSessionWithinGrant(existing, grant) && adapterSessionLinkedToGrant(existing, grant) {
		writeJSON(w, http.StatusOK, adapterSessionResponse{
			Name:           existing.Name,
			Namespace:      existing.Namespace,
			HumanID:        string(existing.Spec.Subject.HumanID),
			AgentID:        string(existing.Spec.Subject.AgentID),
			TeamID:         string(existing.Spec.Subject.TeamID),
			ServerName:     string(existing.Spec.ServerRef.Name),
			ConsentedTrust: string(existing.Spec.ConsentedTrust),
			PolicyVersion:  existing.Spec.PolicyVersion,
			ExpiresAt:      existing.Spec.ExpiresAt.Time,
			Reused:         true,
			TrustDomain:    strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN")),
		})
		return
	}

	session := &sentinelaccess.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionName,
			Namespace: runtimeaccess.DefaultAccessNamespace(req.Namespace),
			Annotations: map[string]string{
				adapterGrantNameAnnotation:      grant.Name,
				adapterGrantNamespaceAnnotation: grant.Namespace,
			},
		},
		Spec: sentinelaccess.MCPAgentSessionSpec{
			ServerRef: sentinelaccess.ServerReference{
				Name:      sentinelaccess.ServerName(req.ServerName),
				Namespace: sentinelaccess.Namespace(runtimeaccess.DefaultAccessNamespace(req.Namespace)),
			},
			Subject: sentinelaccess.SubjectRef{
				HumanID: sentinelaccess.HumanID(humanID),
				AgentID: sentinelaccess.AgentID(req.AgentID),
				TeamID:  sentinelaccess.TeamID(teamID),
			},
			ConsentedTrust: consentedTrust,
			ExpiresAt:      &metav1.Time{Time: expiresAt},
			PolicyVersion:  policyVersion,
		},
	}
	applied, err := s.accessMgr.ApplySession(ctx, session)
	if err != nil {
		log.Printf("adapter session apply %s/%s failed: %v", session.Namespace, session.Name, err)
		writeK8sApplyError(w, "adapter session", session.Namespace, session.Name, err)
		return
	}
	// Close the race where deactivation finishes its session scan after this
	// handler's first lookup but before the session is persisted.
	if err := requireActiveAgent(ctx, s.identity, req.AgentID, teamID); err != nil {
		if revokeErr := s.accessMgr.RevokeSession(ctx, applied.Name, applied.Namespace); revokeErr != nil {
			log.Printf("revoke session for inactive agent %s/%s failed: %v", applied.Namespace, applied.Name, revokeErr)
		}
		writeAgentDirectoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adapterSessionResponse{
		Name:           applied.Name,
		Namespace:      applied.Namespace,
		HumanID:        string(applied.Spec.Subject.HumanID),
		AgentID:        string(applied.Spec.Subject.AgentID),
		TeamID:         string(applied.Spec.Subject.TeamID),
		ServerName:     string(applied.Spec.ServerRef.Name),
		ConsentedTrust: string(applied.Spec.ConsentedTrust),
		PolicyVersion:  applied.Spec.PolicyVersion,
		ExpiresAt:      applied.Spec.ExpiresAt.Time,
		Reused:         false,
		TrustDomain:    strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN")),
	})
}

func adapterSessionLinkedToGrant(session *sentinelaccess.MCPAgentSession, grant *sentinelaccess.MCPAccessGrant) bool {
	if session == nil || grant == nil || session.Annotations == nil {
		return false
	}
	return session.Annotations[adapterGrantNameAnnotation] == grant.Name &&
		session.Annotations[adapterGrantNamespaceAnnotation] == grant.Namespace
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func adapterSessionWithinGrant(session *sentinelaccess.MCPAgentSession, grant *sentinelaccess.MCPAccessGrant) bool {
	if session == nil || session.Spec.ExpiresAt == nil {
		return false
	}
	return grant == nil || grant.Spec.ExpiresAt == nil || !session.Spec.ExpiresAt.After(grant.Spec.ExpiresAt.Time)
}

func adapterPrincipalTeamIDs(p principal) []string {
	if len(p.Teams) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(p.Teams))
	out := make([]string, 0, len(p.Teams))
	for _, team := range p.Teams {
		id := strings.TrimSpace(team.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func defaultAdapterSessionTeamID(p principal, namespace string, teamIDs []string) string {
	if team, ok := p.TeamForNamespace(namespace); ok {
		return strings.TrimSpace(team.ID)
	}
	if len(teamIDs) == 1 {
		return teamIDs[0]
	}
	return ""
}

// selectAdapterGrant lists enabled MCPAccessGrants in namespace whose serverRef
// matches and whose subject either matches the caller exactly or leaves the
// field empty (wildcard). When multiple grants match, the one with the highest
// MaxTrust wins; ties are broken by oldest creationTimestamp so the result is
// deterministic across replicas.
func (s *AccessService) selectAdapterGrant(ctx context.Context, namespace, serverName, humanID, agentID string, teamIDs []string, defaultTeamID string, allowAnyTeam bool) (*sentinelaccess.MCPAccessGrant, string, error) {
	if s == nil || s.accessMgr == nil {
		return nil, "", fmt.Errorf("kubernetes not available")
	}
	grants, err := s.accessMgr.ListGrants(ctx, namespace)
	if err != nil {
		return nil, "", fmt.Errorf("list grants in %s: %w", namespace, err)
	}
	type grantMatch struct {
		grant  sentinelaccess.MCPAccessGrant
		teamID string
	}
	var matches []grantMatch
	for _, g := range grants.Items {
		if g.Spec.Disabled || (g.Spec.ExpiresAt != nil && !g.Spec.ExpiresAt.After(time.Now())) {
			continue
		}
		if string(g.Spec.ServerRef.Name) != serverName {
			continue
		}
		teamID, ok := matchingAdapterGrantTeamID(g.Spec.Subject, humanID, agentID, teamIDs, defaultTeamID, allowAnyTeam)
		if !ok {
			continue
		}
		matches = append(matches, grantMatch{grant: g, teamID: teamID})
	}
	if len(matches) == 0 {
		return nil, "", fmt.Errorf("no enabled MCPAccessGrant in %s matches server=%q humanID=%q agentID=%q",
			namespace, serverName, humanID, agentID)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		ti := trustRank(matches[i].grant.Spec.MaxTrust)
		tj := trustRank(matches[j].grant.Spec.MaxTrust)
		if ti != tj {
			return ti > tj
		}
		return matches[i].grant.CreationTimestamp.Time.Before(matches[j].grant.CreationTimestamp.Time)
	})
	g := matches[0]
	return &g.grant, g.teamID, nil
}

func matchingAdapterGrantTeamID(subj sentinelaccess.SubjectRef, humanID, agentID string, teamIDs []string, defaultTeamID string, allowAnyTeam bool) (string, bool) {
	if subj.HumanID != "" && string(subj.HumanID) != humanID {
		return "", false
	}
	if subj.AgentID != "" && string(subj.AgentID) != agentID {
		return "", false
	}
	grantTeamID := strings.TrimSpace(string(subj.TeamID))
	if grantTeamID == "" {
		return defaultTeamID, true
	}
	for _, teamID := range teamIDs {
		if teamID == grantTeamID {
			return grantTeamID, true
		}
	}
	// Admin status authorizes grant management, not impersonation of a caller
	// from a team that is absent from the authenticated identity.
	return "", false
}

// adapterSessionName derives a deterministic resource name from the caller's
// identity so repeated calls with the same identity converge on a single
// MCPAgentSession (and re-use it as long as it remains valid).
func adapterSessionName(humanID, agentID, teamID, serverName string) string {
	h := sha256.New()
	h.Write([]byte(humanID))
	h.Write([]byte{0})
	h.Write([]byte(agentID))
	h.Write([]byte{0})
	h.Write([]byte(teamID))
	h.Write([]byte{0})
	h.Write([]byte(serverName))
	digest := hex.EncodeToString(h.Sum(nil))[:16]
	return "adapter-" + digest
}

// adapterSessionReusable reports whether an existing session can be returned
// to the caller as-is without writing to Kubernetes. Reuse fails closed: if
// any condition is unmet we issue a fresh session.
func adapterSessionReusable(s *sentinelaccess.MCPAgentSession, policyVersion string, consentedTrust sentinelaccess.TrustLevel) bool {
	if s == nil || s.Spec.Revoked {
		return false
	}
	if s.Spec.PolicyVersion != policyVersion {
		return false
	}
	if string(s.Spec.ConsentedTrust) != string(consentedTrust) {
		return false
	}
	if s.Spec.ExpiresAt == nil {
		return false
	}
	if time.Until(s.Spec.ExpiresAt.Time) <= adapterSessionRefreshBuffer {
		return false
	}
	return true
}

// parseAdapterTrust normalises the caller's requested trust level. Empty
// requests default to "low" (least privilege) so callers explicitly opt in to
// higher trust. Unknown values are rejected.
func parseAdapterTrust(raw string) (sentinelaccess.TrustLevel, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "":
		return sentinelaccess.TrustLow, nil
	case string(sentinelaccess.TrustLow), string(sentinelaccess.TrustMedium), string(sentinelaccess.TrustHigh):
		return sentinelaccess.TrustLevel(raw), nil
	default:
		return "", fmt.Errorf("requestedTrust %q is not a known trust level (use low, medium, or high)", raw)
	}
}

func parseAdapterTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return adapterSessionDefaultTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("requestedTTL %q is not a valid duration: %w", raw, err)
	}
	if d <= 0 {
		return 0, errors.New("requestedTTL must be greater than zero")
	}
	if d > adapterSessionMaxTTL {
		return adapterSessionMaxTTL, nil
	}
	return d, nil
}

// capTrust returns the requested trust capped at the grant's max trust.
// Empty grant maxTrust is treated as "no ceiling" — the grant author has not
// asserted a cap and we accept whatever the caller asked for (or the default).
func capTrust(requested, max sentinelaccess.TrustLevel) sentinelaccess.TrustLevel {
	if max == "" {
		return requested
	}
	if trustRank(requested) > trustRank(max) {
		return max
	}
	return requested
}

func trustRank(t sentinelaccess.TrustLevel) int {
	switch t {
	case sentinelaccess.TrustLow:
		return 1
	case sentinelaccess.TrustMedium:
		return 2
	case sentinelaccess.TrustHigh:
		return 3
	default:
		return -1
	}
}
