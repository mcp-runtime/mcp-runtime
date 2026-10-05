package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpaccess "mcp-runtime/pkg/access"
)

func validateGrantRequest(req *accessGrantRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Namespace = runtimeaccess.DefaultAccessNamespace(req.Namespace)
	req.ServerRef.Name = mcpaccess.ServerName(strings.TrimSpace(string(req.ServerRef.Name)))
	req.ServerRef.Namespace = mcpaccess.Namespace(strings.TrimSpace(string(req.ServerRef.Namespace)))
	req.Subject.HumanID = mcpaccess.HumanID(strings.TrimSpace(string(req.Subject.HumanID)))
	req.Subject.AgentID = mcpaccess.AgentID(strings.TrimSpace(string(req.Subject.AgentID)))
	req.Subject.TeamID = mcpaccess.TeamID(strings.TrimSpace(string(req.Subject.TeamID)))
	req.PolicyVersion = runtimeaccess.DefaultPolicyVersion(req.PolicyVersion)
	req.MaxTrust = runtimeaccess.NormalizeTrust(req.MaxTrust)
	if err := mcpaccess.ValidateResourceName("name", req.Name); err != nil {
		return err
	}
	if err := mcpaccess.ValidateResourceName("namespace", req.Namespace); err != nil {
		return err
	}
	if err := mcpaccess.ValidateResourceName("serverRef.name", string(req.ServerRef.Name)); err != nil {
		return err
	}
	if err := mcpaccess.ValidateOptionalResourceName("serverRef.namespace", string(req.ServerRef.Namespace)); err != nil {
		return err
	}
	if req.Subject.HumanID == "" && req.Subject.AgentID == "" && req.Subject.TeamID == "" {
		return errors.New("one of subject.humanID, subject.agentID, or subject.teamID is required")
	}
	if err := runtimeaccess.ValidateTeamIDValue("subject.teamID", string(req.Subject.TeamID)); err != nil {
		return err
	}
	if req.MaxTrust != "" && !runtimeaccess.ValidTrust(req.MaxTrust) {
		return errors.New("maxTrust must be low, medium, or high")
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return errors.New("expiresAt must be in the future")
	}
	if len(req.AllowedSideEffects) == 0 {
		return errors.New("at least one allowed side effect is required")
	}
	seenSideEffects := map[mcpaccess.ToolSideEffect]struct{}{}
	for i := range req.AllowedSideEffects {
		req.AllowedSideEffects[i] = runtimeaccess.NormalizeSideEffect(req.AllowedSideEffects[i])
		if req.AllowedSideEffects[i] == "" {
			return fmt.Errorf("allowedSideEffects[%d] is required", i)
		}
		if !runtimeaccess.ValidSideEffect(req.AllowedSideEffects[i]) {
			return fmt.Errorf("allowedSideEffects[%d] must be read, write, or destructive", i)
		}
		if _, ok := seenSideEffects[req.AllowedSideEffects[i]]; ok {
			return fmt.Errorf("allowedSideEffects[%d] is a duplicate", i)
		}
		seenSideEffects[req.AllowedSideEffects[i]] = struct{}{}
	}
	for i := range req.ToolRules {
		req.ToolRules[i].Name = strings.TrimSpace(req.ToolRules[i].Name)
		req.ToolRules[i].Decision = mcpaccess.PolicyDecision(strings.TrimSpace(string(req.ToolRules[i].Decision)))
		req.ToolRules[i].RequiredTrust = runtimeaccess.NormalizeTrust(req.ToolRules[i].RequiredTrust)
		if req.ToolRules[i].Name == "" {
			return fmt.Errorf("toolRules[%d].name is required", i)
		}
		if !runtimeaccess.ValidDecision(req.ToolRules[i].Decision) {
			return fmt.Errorf("toolRules[%d].decision must be allow or deny", i)
		}
		if req.ToolRules[i].RequiredTrust != "" && !runtimeaccess.ValidTrust(req.ToolRules[i].RequiredTrust) {
			return fmt.Errorf("toolRules[%d].requiredTrust must be low, medium, or high", i)
		}
	}
	return nil
}

func (s *AccessService) handleRuntimeGrantList(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	namespace, err := s.scopedNamespaceForPrincipal(r.Context(), r.URL.Query().Get("namespace"))
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	grants, err := s.accessMgr.ListGrants(ctx, namespace)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list grants")
		return
	}

	p, filterByPrincipal := principalFromContext(ctx)
	filterByPrincipal = filterByPrincipal && p.Role != roleAdmin

	summaries := make([]mcpaccess.GrantSummary, 0, len(grants.Items))
	for _, g := range grants.Items {
		if filterByPrincipal && !principalCanReadGrantSubject(p, g) {
			continue
		}
		summaries = append(summaries, mcpaccess.ToGrantSummary(g))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"grants": summaries})
}

func (s *AccessService) handleRuntimeGrantApply(w http.ResponseWriter, r *http.Request) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	var req accessGrantRequest
	r.Body = http.MaxBytesReader(w, r.Body, accessApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if p, ok := principalFromContext(r.Context()); ok && p.Role != roleAdmin && strings.TrimSpace(req.Namespace) == "" {
		req.Namespace = strings.TrimSpace(p.Namespace)
	}
	if err := validateGrantRequest(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	scopedNamespace, err := s.scopedAccessWriteNamespaceForPrincipal(r.Context(), req.Namespace)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	req.Namespace = scopedNamespace
	if err := runtimeaccess.BindAccessServerRefNamespace(req.Namespace, &req.ServerRef); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	targetServer, err := s.accessMgr.GetMCPServerRef(ctx, req.ServerRef)
	if err != nil {
		if mcpaccess.IsMCPServerNotFoundForRef(err) {
			writeAPIError(w, http.StatusBadRequest, err.Error())
		} else {
			log.Printf("runtime grant: assert MCPServer ref failed: %v", err)
			writeAPIError(w, http.StatusInternalServerError, "failed to verify server reference")
		}
		return
	}
	if !s.principalCanAdministerAccessServer(r.Context(), *targetServer) {
		writeAPIError(w, http.StatusForbidden, "forbidden server")
		return
	}
	if err := s.bindAccessSubjectTeamID(ctx, req.Namespace, targetServer.Spec.TeamID, &req.Subject); err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	crossTeam := strings.TrimSpace(string(req.Subject.TeamID)) != strings.TrimSpace(string(targetServer.Spec.TeamID))
	if crossTeam {
		if err := s.validateCrossTeamSubject(ctx, req.Subject); err != nil {
			writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := validateCrossTeamGrantExpiry(&req); err != nil {
			status := http.StatusUnprocessableEntity
			if errors.Is(err, errInvalidCrossTeamGrantMaxTTL) {
				status = http.StatusInternalServerError
			}
			writeAPIError(w, status, err.Error())
			return
		}
	}
	if err := requireActiveAgent(ctx, s.identity, string(req.Subject.AgentID), string(req.Subject.TeamID)); err != nil {
		writeAgentDirectoryError(w, err)
		return
	}
	disabled, err := s.grantDisabledForApply(ctx, req)
	if err != nil {
		log.Printf("read grant state %s/%s failed: %v", req.Namespace, req.Name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to read grant state")
		return
	}
	existingGrant, existingErr := s.accessMgr.GetGrant(ctx, req.Name, req.Namespace)
	if existingErr != nil && !apierrors.IsNotFound(existingErr) {
		writeAPIError(w, http.StatusInternalServerError, "failed to inspect existing grant")
		return
	}

	grant := &mcpaccess.MCPAccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: runtimeaccess.DefaultAccessNamespace(req.Namespace),
		},
		Spec: mcpaccess.MCPAccessGrantSpec{
			ServerRef:          req.ServerRef,
			Subject:            req.Subject,
			MaxTrust:           req.MaxTrust,
			AllowedSideEffects: req.AllowedSideEffects,
			PolicyVersion:      runtimeaccess.DefaultPolicyVersion(req.PolicyVersion),
			Disabled:           disabled,
			ExpiresAt:          req.ExpiresAt,
			ToolRules:          req.ToolRules,
		},
	}
	applied, err := s.accessMgr.ApplyGrant(ctx, grant)
	if err != nil {
		writeK8sApplyError(w, "grant", grant.Namespace, grant.Name, err)
		return
	}
	if crossTeam && s.audit != nil {
		action := "grant.cross_team.created"
		if existingGrant != nil {
			action = "grant.cross_team.updated"
		}
		s.writeCrossTeamGrantAudit(ctx, action, *applied, string(targetServer.Spec.TeamID))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"grant": mcpaccess.ToGrantSummary(*applied)})
}

func (s *AccessService) writeCrossTeamGrantAudit(ctx context.Context, action string, grant mcpaccess.MCPAccessGrant, resourceTeamID string) {
	if s == nil || s.audit == nil || strings.TrimSpace(string(grant.Spec.Subject.TeamID)) == strings.TrimSpace(resourceTeamID) {
		return
	}
	p, _ := principalFromContext(ctx)
	expiresAt := ""
	if grant.Spec.ExpiresAt != nil {
		expiresAt = grant.Spec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	message, _ := json.Marshal(map[string]string{
		"grantor":          auditIdentityLabel(p),
		"grantee_team":     string(grant.Spec.Subject.TeamID),
		"subject_human":    string(grant.Spec.Subject.HumanID),
		"subject_agent":    string(grant.Spec.Subject.AgentID),
		"server":           string(grant.Spec.ServerRef.Name),
		"subject_team_id":  string(grant.Spec.Subject.TeamID),
		"resource_team_id": strings.TrimSpace(resourceTeamID),
		"expires_at":       expiresAt,
	})
	s.audit.WriteAudit(ctx, auditEvent{UserID: p.Subject, Action: action, Resource: grant.Name, Namespace: grant.Namespace, Status: "success", Message: string(message), AuthIdentity: auditIdentityLabel(p)})
}

func validateCrossTeamGrantExpiry(req *accessGrantRequest) error {
	if req.ExpiresAt == nil {
		return errors.New("expiresAt is required for cross-team grants")
	}
	maxTTL, err := crossTeamGrantMaxTTL()
	if err != nil {
		return err
	}
	if !crossTeamGrantExpiryValid(req.ExpiresAt.Time, time.Now(), maxTTL) {
		return fmt.Errorf("expiresAt must be within %s", maxTTL)
	}
	return nil
}
