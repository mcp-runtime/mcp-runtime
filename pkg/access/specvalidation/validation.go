// Package specvalidation owns the shared structural grant/session contract.
// It has no dependency on the CRDs or access manager, so both admission and
// platform request boundaries can use it without an import cycle.
package specvalidation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Boundary preserves the existing contracts: admission permits legacy wildcard
// grants and empty side-effect lists; platform apply requires explicit intent.
type Boundary uint8

const (
	Admission Boundary = iota
	Apply
)

type Subject struct{ HumanID, AgentID, TeamID string }
type ToolRule struct{ Name, Decision, RequiredTrust string }
type SecretKeyRef struct{ Name, Key string }
type Grant struct {
	ServerName         string
	Subject            Subject
	Mode               *string
	MaxTrust           string
	ExpiresAt          *time.Time
	AllowedSideEffects []string
	ToolRules          []ToolRule
}
type Session struct {
	ServerName             string
	Subject                Subject
	ConsentedTrust         string
	UpstreamTokenSecretRef *SecretKeyRef
}

type violation struct {
	field   *field.Error
	message string
}

// Result retains Kubernetes field causes and the existing platform error text.
type Result []violation

func (r Result) FieldErrors() field.ErrorList {
	out := make(field.ErrorList, 0, len(r))
	for _, v := range r {
		out = append(out, v.field)
	}
	return out
}
func (r Result) FirstError() error {
	if len(r) == 0 {
		return nil
	}
	return errors.New(r[0].message)
}
func (r *Result) add(e *field.Error, message string) { *r = append(*r, violation{e, message}) }
func ValidTrust(value string) bool                   { return value == "low" || value == "medium" || value == "high" }
func ValidSideEffect(value string) bool {
	return value == "read" || value == "write" || value == "destructive"
}
func ValidDecision(value string) bool { return value == "allow" || value == "deny" }

// TeamIDField validates a stable identifier without normalizing persisted data.
// Request boundaries trim their input before calling the same predicate.
func TeamIDField(path *field.Path, value string) *field.Error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if trimmed != value || strings.ContainsAny(trimmed, " \t\r\n") {
		return field.Invalid(path, value, "teamID must be a stable identifier without whitespace")
	}
	if len(trimmed) > 128 {
		return field.TooLong(path, value, 128)
	}
	return nil
}
func ModeError(mode *string, boundary Boundary) error {
	if mode == nil || (*mode == "" && boundary == Admission) || *mode == "enforce" || *mode == "monitor" {
		return nil
	}
	return errors.New("mode must be enforce or monitor")
}
func validateSubject(out *Result, subject Subject, required bool) {
	path := field.NewPath("spec", "subject")
	if required && strings.TrimSpace(subject.HumanID) == "" && strings.TrimSpace(subject.AgentID) == "" && strings.TrimSpace(subject.TeamID) == "" {
		message := "one of subject.humanID, subject.agentID, or subject.teamID is required"
		out.add(field.Required(path, message), message)
	}
	if err := TeamIDField(path.Child("teamID"), subject.TeamID); err != nil {
		message := "subject.teamID must not contain whitespace"
		if err.Type == field.ErrorTypeTooLong {
			message = "subject.teamID must be at most 128 characters"
		}
		out.add(err, message)
	}
}

// ValidateGrant does not default or mutate the input. Normalization belongs to
// the platform request boundary; admission validates the submitted spec as-is.
func ValidateGrant(spec Grant, boundary Boundary, now time.Time) Result {
	var out Result
	path := field.NewPath("spec")
	if strings.TrimSpace(spec.ServerName) == "" {
		out.add(field.Required(path.Child("serverRef", "name"), "serverRef.name is required"), "serverRef.name is required")
	}
	validateSubject(&out, spec.Subject, boundary == Apply)
	if err := ModeError(spec.Mode, boundary); err != nil {
		out.add(field.NotSupported(path.Child("mode"), *spec.Mode, []string{"enforce", "monitor"}), err.Error())
	}
	if boundary == Apply {
		if spec.MaxTrust != "" && !ValidTrust(spec.MaxTrust) {
			out.add(field.NotSupported(path.Child("maxTrust"), spec.MaxTrust, []string{"low", "medium", "high"}), "maxTrust must be low, medium, or high")
		}
		if spec.ExpiresAt != nil && !spec.ExpiresAt.After(now) {
			out.add(field.Invalid(path.Child("expiresAt"), *spec.ExpiresAt, "must be in the future"), "expiresAt must be in the future")
		}
		if len(spec.AllowedSideEffects) == 0 {
			out.add(field.Required(path.Child("allowedSideEffects"), "at least one allowed side effect is required"), "at least one allowed side effect is required")
		}
	}
	effects := make(map[string]struct{}, len(spec.AllowedSideEffects))
	for i, effect := range spec.AllowedSideEffects {
		p := path.Child("allowedSideEffects").Index(i)
		if strings.TrimSpace(effect) == "" {
			out.add(field.Required(p, "allowed side effect is required"), fmt.Sprintf("allowedSideEffects[%d] is required", i))
			continue
		}
		if !ValidSideEffect(effect) {
			out.add(field.NotSupported(p, effect, []string{"read", "write", "destructive"}), fmt.Sprintf("allowedSideEffects[%d] must be read, write, or destructive", i))
			continue
		}
		if _, ok := effects[effect]; ok {
			out.add(field.Duplicate(p, effect), fmt.Sprintf("allowedSideEffects[%d] is a duplicate", i))
		}
		effects[effect] = struct{}{}
	}
	names := make(map[string]struct{}, len(spec.ToolRules))
	for i, rule := range spec.ToolRules {
		p := path.Child("toolRules").Index(i)
		if strings.TrimSpace(rule.Name) == "" {
			out.add(field.Required(p.Child("name"), "tool rule name is required"), fmt.Sprintf("toolRules[%d].name is required", i))
			continue
		}
		if boundary == Apply {
			if !ValidDecision(rule.Decision) {
				out.add(field.NotSupported(p.Child("decision"), rule.Decision, []string{"allow", "deny"}), fmt.Sprintf("toolRules[%d].decision must be allow or deny", i))
			}
			if rule.RequiredTrust != "" && !ValidTrust(rule.RequiredTrust) {
				out.add(field.NotSupported(p.Child("requiredTrust"), rule.RequiredTrust, []string{"low", "medium", "high"}), fmt.Sprintf("toolRules[%d].requiredTrust must be low, medium, or high", i))
			}
		} else {
			if strings.TrimSpace(rule.Decision) == "" {
				out.add(field.Required(p.Child("decision"), "tool rule decision is required"), fmt.Sprintf("toolRules[%d].decision is required", i))
			}
			if _, ok := names[rule.Name]; ok {
				out.add(field.Duplicate(p.Child("name"), rule.Name), fmt.Sprintf("toolRules[%d].name is a duplicate", i))
			}
		}
		names[rule.Name] = struct{}{}
	}
	return out
}

func ValidateSession(spec Session, boundary Boundary) Result {
	var out Result
	path := field.NewPath("spec")
	if strings.TrimSpace(spec.ServerName) == "" {
		out.add(field.Required(path.Child("serverRef", "name"), "serverRef.name is required"), "serverRef.name is required")
	}
	validateSubject(&out, spec.Subject, true)
	if boundary == Apply && spec.ConsentedTrust != "" && !ValidTrust(spec.ConsentedTrust) {
		out.add(field.NotSupported(path.Child("consentedTrust"), spec.ConsentedTrust, []string{"low", "medium", "high"}), "consentedTrust must be low, medium, or high")
	}
	if boundary == Admission && spec.UpstreamTokenSecretRef != nil {
		ref := spec.UpstreamTokenSecretRef
		if strings.TrimSpace(ref.Name) == "" {
			out.add(field.Required(path.Child("upstreamTokenSecretRef", "name"), "secret name is required"), "upstreamTokenSecretRef.name is required")
		}
		if strings.TrimSpace(ref.Key) == "" {
			out.add(field.Required(path.Child("upstreamTokenSecretRef", "key"), "secret key is required"), "upstreamTokenSecretRef.key is required")
		}
	}
	return out
}
