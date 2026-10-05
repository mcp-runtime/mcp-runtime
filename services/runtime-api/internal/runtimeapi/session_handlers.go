package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpaccess "mcp-runtime/pkg/access"
)

func validateSessionRequest(req *accessSessionRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Namespace = runtimeaccess.DefaultAccessNamespace(req.Namespace)
	req.ServerRef.Name = mcpaccess.ServerName(strings.TrimSpace(string(req.ServerRef.Name)))
	req.ServerRef.Namespace = mcpaccess.Namespace(strings.TrimSpace(string(req.ServerRef.Namespace)))
	req.Subject.HumanID = mcpaccess.HumanID(strings.TrimSpace(string(req.Subject.HumanID)))
	req.Subject.AgentID = mcpaccess.AgentID(strings.TrimSpace(string(req.Subject.AgentID)))
	req.Subject.TeamID = mcpaccess.TeamID(strings.TrimSpace(string(req.Subject.TeamID)))
	req.PolicyVersion = runtimeaccess.DefaultPolicyVersion(req.PolicyVersion)
	req.ConsentedTrust = runtimeaccess.NormalizeTrust(req.ConsentedTrust)
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
	if req.ConsentedTrust != "" && !runtimeaccess.ValidTrust(req.ConsentedTrust) {
		return errors.New("consentedTrust must be low, medium, or high")
	}
	return nil
}

func (s *AccessService) handleRuntimeSessionList(w http.ResponseWriter, r *http.Request) {
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
	sessions, err := s.accessMgr.ListSessions(ctx, namespace)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	p, filterByPrincipal := principalFromContext(ctx)
	filterByPrincipal = filterByPrincipal && p.Role != roleAdmin

	summaries := make([]mcpaccess.SessionSummary, 0, len(sessions.Items))
	for _, sess := range sessions.Items {
		if filterByPrincipal && !principalCanReadSessionSubject(p, sess) {
			continue
		}
		summaries = append(summaries, mcpaccess.ToSessionSummary(sess))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": summaries})
}

func (s *AccessService) handleRuntimeSessionApply(w http.ResponseWriter, r *http.Request) {
	if s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	var req accessSessionRequest
	r.Body = http.MaxBytesReader(w, r.Body, accessApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if p, ok := principalFromContext(r.Context()); ok && p.Role != roleAdmin && strings.TrimSpace(req.Namespace) == "" {
		req.Namespace = strings.TrimSpace(p.Namespace)
	}
	if err := validateSessionRequest(&req); err != nil {
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
			log.Printf("runtime session: assert MCPServer ref failed: %v", err)
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
	grantAnnotations, grant, err := s.sessionGrantLink(ctx, req)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if grant != nil {
		if strings.TrimSpace(string(req.Subject.TeamID)) != strings.TrimSpace(string(targetServer.Spec.TeamID)) {
			if err := s.validateCrossTeamSubject(ctx, req.Subject); err != nil {
				writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
				return
			}
		}
		if grant.Spec.ExpiresAt != nil && (req.ExpiresAt == nil || req.ExpiresAt.After(grant.Spec.ExpiresAt.Time)) {
			req.ExpiresAt = grant.Spec.ExpiresAt.DeepCopy()
		}
		req.ConsentedTrust = capTrust(req.ConsentedTrust, grant.Spec.MaxTrust)
	}
	if err := requireActiveAgent(ctx, s.identity, string(req.Subject.AgentID), string(req.Subject.TeamID)); err != nil {
		writeAgentDirectoryError(w, err)
		return
	}

	revoked, err := s.sessionRevokedForApply(ctx, req)
	if err != nil {
		log.Printf("read session state %s/%s failed: %v", req.Namespace, req.Name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to read session state")
		return
	}

	session := &mcpaccess.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:        req.Name,
			Namespace:   runtimeaccess.DefaultAccessNamespace(req.Namespace),
			Annotations: grantAnnotations,
		},
		Spec: mcpaccess.MCPAgentSessionSpec{
			ServerRef:      req.ServerRef,
			Subject:        req.Subject,
			ConsentedTrust: req.ConsentedTrust,
			ExpiresAt:      req.ExpiresAt,
			Revoked:        revoked,
			PolicyVersion:  runtimeaccess.DefaultPolicyVersion(req.PolicyVersion),
		},
	}
	applied, err := s.accessMgr.ApplySession(ctx, session)
	if err != nil {
		writeK8sApplyError(w, "session", session.Namespace, session.Name, err)
		return
	}
	if err := requireActiveAgent(ctx, s.identity, string(applied.Spec.Subject.AgentID), string(applied.Spec.Subject.TeamID)); err != nil {
		if revokeErr := s.accessMgr.RevokeSession(ctx, applied.Name, applied.Namespace); revokeErr != nil {
			log.Printf("revoke session for inactive agent %s/%s failed: %v", applied.Namespace, applied.Name, revokeErr)
		}
		writeAgentDirectoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"session": mcpaccess.ToSessionSummary(*applied)})
}

func (s *AccessService) sessionGrantLink(ctx context.Context, req accessSessionRequest) (map[string]string, *mcpaccess.MCPAccessGrant, error) {
	grantName := strings.TrimSpace(req.GrantName)
	if grantName == "" {
		return nil, nil, nil
	}
	grant, err := s.accessMgr.GetGrant(ctx, grantName, runtimeaccess.DefaultAccessNamespace(req.Namespace))
	if err != nil || grant == nil || grant.Spec.Disabled || (grant.Spec.ExpiresAt != nil && !grant.Spec.ExpiresAt.After(time.Now())) {
		return nil, nil, errors.New("grantName does not identify an active grant in this namespace")
	}
	if string(grant.Spec.ServerRef.Name) != string(req.ServerRef.Name) ||
		(grant.Spec.ServerRef.Namespace != "" && string(grant.Spec.ServerRef.Namespace) != string(req.ServerRef.Namespace)) ||
		(grant.Spec.Subject.HumanID != "" && grant.Spec.Subject.HumanID != req.Subject.HumanID) ||
		(grant.Spec.Subject.AgentID != "" && grant.Spec.Subject.AgentID != req.Subject.AgentID) ||
		(grant.Spec.Subject.TeamID != "" && grant.Spec.Subject.TeamID != req.Subject.TeamID) {
		return nil, nil, errors.New("grantName does not match the session server and subject")
	}
	return map[string]string{
		adapterGrantNameAnnotation:      grant.Name,
		adapterGrantNamespaceAnnotation: grant.Namespace,
	}, grant, nil
}

func (s *AccessService) sessionRevokedForApply(ctx context.Context, req accessSessionRequest) (bool, error) {
	if req.Revoked != nil {
		return *req.Revoked, nil
	}
	existing, err := s.accessMgr.GetSession(ctx, req.Name, runtimeaccess.DefaultAccessNamespace(req.Namespace))
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return existing.Spec.Revoked, nil
}
