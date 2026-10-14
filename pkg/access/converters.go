package access

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// convertToGrant converts an unstructured object to MCPAccessGrant.
func convertToGrant(obj interface{}) (*MCPAccessGrant, error) {
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}

	grant := &MCPAccessGrant{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.UnstructuredContent(), grant); err != nil {
		return nil, fmt.Errorf("failed to convert to MCPAccessGrant: %w", err)
	}
	return grant, nil
}

// convertToGrantList converts an unstructured list to MCPAccessGrantList.
func convertToGrantList(obj interface{}) (*MCPAccessGrantList, error) {
	unstructuredList, ok := obj.(*unstructured.UnstructuredList)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}

	list := &MCPAccessGrantList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: APIGroup + "/" + APIVersion,
			Kind:       "MCPAccessGrantList",
		},
		ListMeta: metav1.ListMeta{
			ResourceVersion:    unstructuredList.GetResourceVersion(),
			Continue:           unstructuredList.GetContinue(),
			RemainingItemCount: unstructuredList.GetRemainingItemCount(),
		},
		Items: make([]MCPAccessGrant, 0, len(unstructuredList.Items)),
	}

	for _, u := range unstructuredList.Items {
		grant := &MCPAccessGrant{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), grant); err != nil {
			return nil, fmt.Errorf("failed to convert item to MCPAccessGrant: %w", err)
		}
		list.Items = append(list.Items, *grant)
	}

	return list, nil
}

// convertToSession converts an unstructured object to MCPAgentSession.
func convertToSession(obj interface{}) (*MCPAgentSession, error) {
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}

	session := &MCPAgentSession{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.UnstructuredContent(), session); err != nil {
		return nil, fmt.Errorf("failed to convert to MCPAgentSession: %w", err)
	}
	return session, nil
}

// convertToSessionList converts an unstructured list to MCPAgentSessionList.
func convertToSessionList(obj interface{}) (*MCPAgentSessionList, error) {
	unstructuredList, ok := obj.(*unstructured.UnstructuredList)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}

	list := &MCPAgentSessionList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: APIGroup + "/" + APIVersion,
			Kind:       "MCPAgentSessionList",
		},
		ListMeta: metav1.ListMeta{
			ResourceVersion:    unstructuredList.GetResourceVersion(),
			Continue:           unstructuredList.GetContinue(),
			RemainingItemCount: unstructuredList.GetRemainingItemCount(),
		},
		Items: make([]MCPAgentSession, 0, len(unstructuredList.Items)),
	}

	for _, u := range unstructuredList.Items {
		session := &MCPAgentSession{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), session); err != nil {
			return nil, fmt.Errorf("failed to convert item to MCPAgentSession: %w", err)
		}
		list.Items = append(list.Items, *session)
	}

	return list, nil
}

// GrantSummary provides a simplified view of a grant for UI display.
type GrantSummary struct {
	Name               string           `json:"name"`
	Namespace          string           `json:"namespace"`
	ServerRef          ServerReference  `json:"serverRef"`
	Subject            SubjectRef       `json:"subject"`
	MaxTrust           TrustLevel       `json:"maxTrust"`
	AllowedSideEffects []ToolSideEffect `json:"allowedSideEffects,omitempty"`
	ExpiresAt          *metav1.Time     `json:"expiresAt,omitempty"`
	Disabled           bool             `json:"disabled"`
	Age                string           `json:"age"`
}

// ToGrantSummary converts an MCPAccessGrant to a GrantSummary.
func ToGrantSummary(grant MCPAccessGrant) GrantSummary {
	return GrantSummary{
		Name:               grant.Name,
		Namespace:          grant.Namespace,
		ServerRef:          grant.Spec.ServerRef,
		Subject:            grant.Spec.Subject,
		MaxTrust:           grant.Spec.MaxTrust,
		AllowedSideEffects: append([]ToolSideEffect(nil), grant.Spec.AllowedSideEffects...),
		ExpiresAt:          grant.Spec.ExpiresAt,
		Disabled:           grant.Spec.Disabled,
		Age:                grant.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
	}
}

// SessionSummary provides a simplified view of a session for UI display.
type SessionSummary struct {
	Name            string          `json:"name"`
	Namespace       string          `json:"namespace"`
	ServerRef       ServerReference `json:"serverRef"`
	Subject         SubjectRef      `json:"subject"`
	ConsentedTrust  TrustLevel      `json:"consentedTrust"`
	Revoked         bool            `json:"revoked"`
	ExpiresAt       *metav1.Time    `json:"expiresAt,omitempty"`
	Age             string          `json:"age"`
	PolicyVersion   string          `json:"policyVersion,omitempty"`
	ServerUID       string          `json:"serverUID,omitempty"`
	GrantName       string          `json:"grantName,omitempty"`
	GrantNamespace  string          `json:"grantNamespace,omitempty"`
	RevokedAt       string          `json:"revokedAt,omitempty"`
	RevocationCause string          `json:"revocationCause,omitempty"`
}

// ToSessionSummary converts an MCPAgentSession to a SessionSummary.
func ToSessionSummary(session MCPAgentSession) SessionSummary {
	summary := SessionSummary{
		Name:            session.Name,
		Namespace:       session.Namespace,
		ServerRef:       session.Spec.ServerRef,
		Subject:         session.Spec.Subject,
		ConsentedTrust:  session.Spec.ConsentedTrust,
		Revoked:         session.Spec.Revoked,
		ExpiresAt:       session.Spec.ExpiresAt,
		Age:             session.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
		PolicyVersion:   session.Spec.PolicyVersion,
		ServerUID:       session.Annotations[ServerUIDAnnotation],
		GrantName:       session.Annotations["mcpruntime.org/access-grant-name"],
		GrantNamespace:  session.Annotations["mcpruntime.org/access-grant-namespace"],
		RevokedAt:       session.Annotations["mcpruntime.org/session-revoked-at"],
		RevocationCause: session.Annotations["mcpruntime.org/session-revocation-cause"],
	}
	return summary
}

// ToJSON converts a grant or session to JSON bytes.
func ToJSON(obj interface{}) ([]byte, error) {
	return json.MarshalIndent(obj, "", "  ")
}
