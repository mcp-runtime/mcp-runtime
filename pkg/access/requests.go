package access

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/pkg/access/specvalidation"
	"mcp-runtime/pkg/mcpdefaults"
)

// GrantApplyRequest is the platform apply contract shared by the CLI and API.
type GrantApplyRequest struct {
	Mode               *GrantMode       `json:"mode,omitempty"`
	Name               string           `json:"name"`
	Namespace          string           `json:"namespace"`
	ServerRef          ServerReference  `json:"serverRef"`
	Subject            SubjectRef       `json:"subject"`
	MaxTrust           TrustLevel       `json:"maxTrust"`
	AllowedSideEffects []ToolSideEffect `json:"allowedSideEffects"`
	PolicyVersion      string           `json:"policyVersion"`
	Disabled           *bool            `json:"disabled,omitempty"`
	ExpiresAt          *metav1.Time     `json:"expiresAt,omitempty"`
	ToolRules          []ToolRule       `json:"toolRules"`
}

// SessionApplyRequest is the administrative platform session input contract.
type SessionApplyRequest struct {
	Name           string          `json:"name"`
	Namespace      string          `json:"namespace"`
	ServerRef      ServerReference `json:"serverRef"`
	Subject        SubjectRef      `json:"subject"`
	ConsentedTrust TrustLevel      `json:"consentedTrust"`
	ExpiresAt      *metav1.Time    `json:"expiresAt"`
	Revoked        *bool           `json:"revoked,omitempty"`
	PolicyVersion  string          `json:"policyVersion"`
	GrantName      string          `json:"grantName,omitempty"`
	GrantNamespace string          `json:"grantNamespace,omitempty"`
}

func normalizeApplyIdentity(name, namespace *string, server *ServerReference, subject *SubjectRef, version *string) {
	*name = strings.TrimSpace(*name)
	*namespace = strings.TrimSpace(*namespace)
	if *namespace == "" {
		*namespace = DefaultMCPResourceNamespace
	}
	server.Name = ServerName(strings.TrimSpace(string(server.Name)))
	server.Namespace = Namespace(strings.TrimSpace(string(server.Namespace)))
	subject.HumanID = HumanID(strings.TrimSpace(string(subject.HumanID)))
	subject.AgentID = AgentID(strings.TrimSpace(string(subject.AgentID)))
	subject.TeamID = TeamID(strings.TrimSpace(string(subject.TeamID)))
	*version = strings.TrimSpace(*version)
	if *version == "" {
		*version = mcpdefaults.PolicyVersion
	}
}
func validateApplyNames(name, namespace string, server ServerReference) error {
	for _, item := range []struct{ field, value string }{{"name", name}, {"namespace", namespace}, {"serverRef.name", string(server.Name)}} {
		if err := ValidateResourceName(item.field, item.value); err != nil {
			return err
		}
	}
	return ValidateOptionalResourceName("serverRef.namespace", string(server.Namespace))
}

// ValidateGrantApplyRequest preserves HTTP apply normalization and validates all
// non-authority fields. Live server, team, agent and grant authorization remains
// the runtime API's responsibility.
func ValidateGrantApplyRequest(req *GrantApplyRequest, now time.Time) error {
	var mode *string
	if req.Mode != nil {
		value := string(*req.Mode)
		mode = &value
	}
	if err := specvalidation.ModeError(mode, specvalidation.Apply); err != nil {
		return err
	}
	normalizeApplyIdentity(&req.Name, &req.Namespace, &req.ServerRef, &req.Subject, &req.PolicyVersion)
	req.MaxTrust = TrustLevel(strings.TrimSpace(string(req.MaxTrust)))
	if err := validateApplyNames(req.Name, req.Namespace, req.ServerRef); err != nil {
		return err
	}
	spec := specvalidation.Grant{ServerName: string(req.ServerRef.Name), Subject: validationSubject(req.Subject), Mode: mode, MaxTrust: string(req.MaxTrust)}
	if req.ExpiresAt != nil {
		spec.ExpiresAt = &req.ExpiresAt.Time
	}
	for i, effect := range req.AllowedSideEffects {
		req.AllowedSideEffects[i] = ToolSideEffect(strings.TrimSpace(string(effect)))
		spec.AllowedSideEffects = append(spec.AllowedSideEffects, string(req.AllowedSideEffects[i]))
	}
	for i, rule := range req.ToolRules {
		req.ToolRules[i].Name = strings.TrimSpace(rule.Name)
		req.ToolRules[i].Decision = PolicyDecision(strings.TrimSpace(string(rule.Decision)))
		req.ToolRules[i].RequiredTrust = TrustLevel(strings.TrimSpace(string(rule.RequiredTrust)))
		spec.ToolRules = append(spec.ToolRules, specvalidation.ToolRule{Name: req.ToolRules[i].Name, Decision: string(req.ToolRules[i].Decision), RequiredTrust: string(req.ToolRules[i].RequiredTrust)})
	}
	return specvalidation.ValidateGrant(spec, specvalidation.Apply, now).FirstError()
}

// ValidateSessionApplyRequest does not reject past expiry: admin session apply
// also imports or revokes historical sessions, preserving the existing contract.
func ValidateSessionApplyRequest(req *SessionApplyRequest) error {
	normalizeApplyIdentity(&req.Name, &req.Namespace, &req.ServerRef, &req.Subject, &req.PolicyVersion)
	req.ConsentedTrust = TrustLevel(strings.TrimSpace(string(req.ConsentedTrust)))
	if err := validateApplyNames(req.Name, req.Namespace, req.ServerRef); err != nil {
		return err
	}
	return specvalidation.ValidateSession(specvalidation.Session{ServerName: string(req.ServerRef.Name), Subject: validationSubject(req.Subject), ConsentedTrust: string(req.ConsentedTrust)}, specvalidation.Apply).FirstError()
}
func validationSubject(subject SubjectRef) specvalidation.Subject {
	return specvalidation.Subject{HumanID: string(subject.HumanID), AgentID: string(subject.AgentID), TeamID: string(subject.TeamID)}
}
