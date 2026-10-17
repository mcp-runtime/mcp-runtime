package v1alpha1

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"mcp-runtime/pkg/access/specvalidation"
	"time"
)

// Admission deliberately retains legacy wildcard grants and empty side-effect
// lists. The stricter platform apply boundary shares the same rule owner.
func (r *MCPAccessGrant) validate() error {
	spec := specvalidation.Grant{ServerName: r.Spec.ServerRef.Name, Subject: specvalidation.Subject{HumanID: r.Spec.Subject.HumanID, AgentID: r.Spec.Subject.AgentID, TeamID: r.Spec.Subject.TeamID}, MaxTrust: string(r.Spec.MaxTrust)}
	if r.Spec.Mode != "" {
		mode := string(r.Spec.Mode)
		spec.Mode = &mode
	}
	if r.Spec.ExpiresAt != nil {
		spec.ExpiresAt = &r.Spec.ExpiresAt.Time
	}
	for _, effect := range r.Spec.AllowedSideEffects {
		spec.AllowedSideEffects = append(spec.AllowedSideEffects, string(effect))
	}
	for _, rule := range r.Spec.ToolRules {
		spec.ToolRules = append(spec.ToolRules, specvalidation.ToolRule{Name: rule.Name, Decision: string(rule.Decision), RequiredTrust: string(rule.RequiredTrust)})
	}
	issues := specvalidation.ValidateGrant(spec, specvalidation.Admission, time.Time{}).FieldErrors()
	if len(issues) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPAccessGrant"}, r.Name, issues)
}

func (r *MCPAgentSession) validate() error {
	spec := specvalidation.Session{ServerName: r.Spec.ServerRef.Name, Subject: specvalidation.Subject{HumanID: r.Spec.Subject.HumanID, AgentID: r.Spec.Subject.AgentID, TeamID: r.Spec.Subject.TeamID}, ConsentedTrust: string(r.Spec.ConsentedTrust)}
	if ref := r.Spec.UpstreamTokenSecretRef; ref != nil {
		spec.UpstreamTokenSecretRef = &specvalidation.SecretKeyRef{Name: ref.Name, Key: ref.Key}
	}
	issues := specvalidation.ValidateSession(spec, specvalidation.Admission).FieldErrors()
	if len(issues) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPAgentSession"}, r.Name, issues)
}
