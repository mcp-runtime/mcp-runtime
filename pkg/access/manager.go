package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/mcpdefaults"
)

const (
	APIGroup              = mcpv1alpha1.Group
	APIVersion            = mcpv1alpha1.Version
	AccessGrantResource   = mcpv1alpha1.MCPAccessGrantResource
	AccessSessionResource = mcpv1alpha1.MCPAgentSessionResource
	MCPServerResource     = mcpv1alpha1.MCPServerResource
	// DefaultMCPResourceNamespace is used when a ServerReference or access resource omits a namespace.
	DefaultMCPResourceNamespace = mcpdefaults.MCPServersNamespace
)

var (
	grantGVR   = schema.GroupVersionResource{Group: APIGroup, Version: APIVersion, Resource: AccessGrantResource}
	sessionGVR = schema.GroupVersionResource{Group: APIGroup, Version: APIVersion, Resource: AccessSessionResource}
	serverGVR  = schema.GroupVersionResource{Group: APIGroup, Version: APIVersion, Resource: MCPServerResource}
)

// Manager provides operations for MCPAccessGrant and MCPAgentSession resources.
type Manager struct {
	dynamic   dynamic.Interface
	clientset kubernetes.Interface
	sessions  SessionBackend
}

// SessionBackend owns runtime session persistence. Grants remain Kubernetes
// resources; platform-managed sessions use the platform API instead of CRDs.
type SessionBackend interface {
	ListSessions(context.Context, string) (*MCPAgentSessionList, error)
	GetSession(context.Context, string, string) (*MCPAgentSession, error)
	ApplySession(context.Context, *MCPAgentSession) (*MCPAgentSession, error)
	RevokeSession(context.Context, string, string) error
	UnrevokeSession(context.Context, string, string) error
	DeleteSession(context.Context, string, string) error
}

// WithSessionBackend selects the authoritative session store at construction.
// A configured backend error never falls back to Kubernetes session state.
func (m *Manager) WithSessionBackend(backend SessionBackend) *Manager {
	m.sessions = backend
	return m
}

// NewManager creates a new access resource manager.
func NewManager(dynamic dynamic.Interface, clientset kubernetes.Interface) *Manager {
	return &Manager{
		dynamic:   dynamic,
		clientset: clientset,
	}
}

// ResolveServerRefNamespace returns the namespace to resolve serverRef against.
// Empty or whitespace ref.Namespace defaults to DefaultMCPResourceNamespace.
func ResolveServerRefNamespace(ref ServerReference) Namespace {
	if ns := strings.TrimSpace(string(ref.Namespace)); ns != "" {
		return Namespace(ns)
	}
	return Namespace(DefaultMCPResourceNamespace)
}

// ErrMCPServerNotFound is returned by AssertMCPServerRef when the target MCPServer is missing.
type ErrMCPServerNotFound struct {
	Name, Namespace string
}

func (e *ErrMCPServerNotFound) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("unknown serverRef: MCPServer %q not found in namespace %q", e.Name, e.Namespace)
}

// IsMCPServerNotFoundForRef returns true if err is an *ErrMCPServerNotFound.
func IsMCPServerNotFoundForRef(err error) bool {
	var missing *ErrMCPServerNotFound
	return err != nil && errors.As(err, &missing)
}

// AssertMCPServerRef checks that a server exists and accepts new authorization.
// Use this before creating grants or sessions so the API can reject unknown targets early.
func (m *Manager) AssertMCPServerRef(ctx context.Context, ref ServerReference) error {
	_, err := m.GetActiveMCPServerRef(ctx, ref)
	return err
}

// GetMCPServerRef returns the MCPServer referenced by ref.
func (m *Manager) GetMCPServerRef(ctx context.Context, ref ServerReference) (*mcpv1alpha1.MCPServer, error) {
	name := strings.TrimSpace(string(ref.Name))
	if name == "" {
		return nil, fmt.Errorf("serverRef.name is required")
	}
	ns := ResolveServerRefNamespace(ref)
	obj, err := m.dynamic.Resource(serverGVR).Namespace(string(ns)).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &ErrMCPServerNotFound{Name: name, Namespace: string(ns)}
		}
		return nil, fmt.Errorf("lookup serverRef: %w", err)
	}
	var server mcpv1alpha1.MCPServer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &server); err != nil {
		return nil, fmt.Errorf("decode serverRef: %w", err)
	}
	return &server, nil
}

// GetActiveMCPServerRef resolves an authorization target for issuance. Ordinary
// lookups retain retiring servers so management can still check ownership and
// clean up access after a failed retirement.
func (m *Manager) GetActiveMCPServerRef(ctx context.Context, ref ServerReference) (*mcpv1alpha1.MCPServer, error) {
	server, err := m.GetMCPServerRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if server.Annotations[ServerRetiringAnnotation] == "true" || server.DeletionTimestamp != nil {
		return nil, apierrors.NewConflict(serverGVR.GroupResource(), server.Name, errors.New("server is being retired"))
	}
	return server, nil
}

// ListMCPServers returns all MCPServer resources, optionally filtered by namespace.
func (m *Manager) ListMCPServers(ctx context.Context, namespace string) (*mcpv1alpha1.MCPServerList, error) {
	var obj *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		obj, err = m.dynamic.Resource(serverGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		obj, err = m.dynamic.Resource(serverGVR).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}
	var servers mcpv1alpha1.MCPServerList
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), &servers); err != nil {
		return nil, fmt.Errorf("decode MCPServers: %w", err)
	}
	return &servers, nil
}

// ListGrants returns all MCPAccessGrant resources, optionally filtered by namespace.
func (m *Manager) ListGrants(ctx context.Context, namespace string) (*MCPAccessGrantList, error) {
	var result *MCPAccessGrantList
	var err error

	if namespace != "" {
		var obj interface{}
		obj, err = m.dynamic.Resource(grantGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list grants in namespace %s: %w", namespace, err)
		}
		result, err = convertToGrantList(obj)
	} else {
		var obj interface{}
		obj, err = m.dynamic.Resource(grantGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list grants: %w", err)
		}
		result, err = convertToGrantList(obj)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

// GetGrant returns a specific MCPAccessGrant resource.
func (m *Manager) GetGrant(ctx context.Context, name, namespace string) (*MCPAccessGrant, error) {
	obj, err := m.dynamic.Resource(grantGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get grant %s/%s: %w", namespace, name, err)
	}
	return convertToGrant(obj)
}

// ApplyGrant creates or updates an MCPAccessGrant resource.
func (m *Manager) ApplyGrant(ctx context.Context, grant *MCPAccessGrant) (*MCPAccessGrant, error) {
	if err := m.bindServerUID(ctx, grant, grant.Spec.ServerRef); err != nil {
		return nil, err
	}
	obj, err := toUnstructured(grant, "MCPAccessGrant")
	if err != nil {
		return nil, fmt.Errorf("failed to convert grant %s/%s: %w", grant.Namespace, grant.Name, err)
	}
	created, err := m.applyUnstructured(ctx, grantGVR, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply grant %s/%s: %w", grant.Namespace, grant.Name, err)
	}
	return convertToGrant(created)
}

// DisableGrant disables an MCPAccessGrant by setting spec.disabled to true.
func (m *Manager) DisableGrant(ctx context.Context, name, namespace string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"disabled": true,
		},
	}
	return m.patchGrant(ctx, name, namespace, patch)
}

// EnableGrant enables an MCPAccessGrant by setting spec.disabled to false.
func (m *Manager) EnableGrant(ctx context.Context, name, namespace string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"disabled": false,
		},
	}
	return m.patchGrant(ctx, name, namespace, patch)
}

// DeleteGrant removes an MCPAccessGrant.
func (m *Manager) DeleteGrant(ctx context.Context, name, namespace string) error {
	if err := m.dynamic.Resource(grantGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("delete grant %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (m *Manager) patchGrant(ctx context.Context, name, namespace string, patch map[string]interface{}) error {
	patchData, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = m.dynamic.Resource(grantGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch grant %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ListSessions returns all MCPAgentSession resources, optionally filtered by namespace.
func (m *Manager) ListSessions(ctx context.Context, namespace string) (*MCPAgentSessionList, error) {
	if m.sessions != nil {
		return m.sessions.ListSessions(ctx, namespace)
	}
	var result *MCPAgentSessionList
	var err error

	if namespace != "" {
		var obj interface{}
		obj, err = m.dynamic.Resource(sessionGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list sessions in namespace %s: %w", namespace, err)
		}
		result, err = convertToSessionList(obj)
	} else {
		var obj interface{}
		obj, err = m.dynamic.Resource(sessionGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list sessions: %w", err)
		}
		result, err = convertToSessionList(obj)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

// GetSession returns a specific MCPAgentSession resource.
func (m *Manager) GetSession(ctx context.Context, name, namespace string) (*MCPAgentSession, error) {
	if m.sessions != nil {
		return m.sessions.GetSession(ctx, name, namespace)
	}
	obj, err := m.dynamic.Resource(sessionGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get session %s/%s: %w", namespace, name, err)
	}
	return convertToSession(obj)
}

// ApplySession creates or updates an MCPAgentSession resource.
func (m *Manager) ApplySession(ctx context.Context, session *MCPAgentSession) (*MCPAgentSession, error) {
	if m.sessions != nil {
		return m.sessions.ApplySession(ctx, session)
	}
	if err := m.bindServerUID(ctx, session, session.Spec.ServerRef); err != nil {
		return nil, err
	}
	obj, err := toUnstructured(session, "MCPAgentSession")
	if err != nil {
		return nil, fmt.Errorf("failed to convert session %s/%s: %w", session.Namespace, session.Name, err)
	}
	created, err := m.applyUnstructured(ctx, sessionGVR, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply session %s/%s: %w", session.Namespace, session.Name, err)
	}
	return convertToSession(created)
}

// RevokeSession revokes an MCPAgentSession by setting spec.revoked to true.
func (m *Manager) RevokeSession(ctx context.Context, name, namespace string) error {
	if m.sessions != nil {
		return m.sessions.RevokeSession(ctx, name, namespace)
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"revoked": true,
		},
	}
	return m.patchSession(ctx, name, namespace, patch)
}

// UnrevokeSession clears the revoked flag on an MCPAgentSession.
func (m *Manager) UnrevokeSession(ctx context.Context, name, namespace string) error {
	if m.sessions != nil {
		return m.sessions.UnrevokeSession(ctx, name, namespace)
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"revoked": false,
		},
	}
	return m.patchSession(ctx, name, namespace, patch)
}

// DeleteSession removes an MCPAgentSession.
func (m *Manager) DeleteSession(ctx context.Context, name, namespace string) error {
	if m.sessions != nil {
		return m.sessions.DeleteSession(ctx, name, namespace)
	}
	if err := m.dynamic.Resource(sessionGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("delete session %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (m *Manager) patchSession(ctx context.Context, name, namespace string, patch map[string]interface{}) error {
	patchData, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = m.dynamic.Resource(sessionGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch session %s/%s: %w", namespace, name, err)
	}
	return nil
}

// maxApplyConflictRetries bounds the Get→Update retry loop in applyUnstructured.
// Concurrent toggle endpoints (enable/disable, revoke/unrevoke) and the operator
// status writer can bump the resourceVersion between our Get and Update; retrying
// with a fresh read converges quickly without the client having to refresh.
const maxApplyConflictRetries = 3

// applyUnstructured creates the object or, if it already exists, updates it while
// preserving server-side metadata the API does not set (finalizers, ownerReferences,
// and merged label/annotation maps) so operator-injected state is not dropped.
// Retries on 409 conflicts so concurrent toggles do not surface as a write failure.
func (m *Manager) applyUnstructured(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	resource := m.dynamic.Resource(gvr).Namespace(obj.GetNamespace())
	desiredLabels := obj.GetLabels()
	desiredAnnotations := obj.GetAnnotations()
	desiredFinalizers := obj.GetFinalizers()
	desiredOwnerRefs := obj.GetOwnerReferences()

	var lastErr error
	for attempt := 0; attempt < maxApplyConflictRetries; attempt++ {
		existing, err := resource.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return resource.Create(ctx, obj, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, err
		}
		// Reset the desired metadata each attempt; preserveExistingObjectMeta merges
		// against the latest server state, not whatever a previous attempt produced.
		obj.SetLabels(desiredLabels)
		obj.SetAnnotations(desiredAnnotations)
		obj.SetFinalizers(desiredFinalizers)
		obj.SetOwnerReferences(desiredOwnerRefs)
		obj.SetResourceVersion(existing.GetResourceVersion())
		preserveExistingObjectMeta(existing, obj)
		updated, err := resource.Update(ctx, obj, metav1.UpdateOptions{})
		if err == nil {
			return updated, nil
		}
		if !apierrors.IsConflict(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func preserveExistingObjectMeta(existing, obj *unstructured.Unstructured) {
	obj.SetLabels(mergeStringMap(existing.GetLabels(), obj.GetLabels()))
	obj.SetAnnotations(mergeStringMap(existing.GetAnnotations(), obj.GetAnnotations()))
	obj.SetFinalizers(mergeStringSlice(existing.GetFinalizers(), obj.GetFinalizers()))
	obj.SetOwnerReferences(mergeOwnerReferences(existing.GetOwnerReferences(), obj.GetOwnerReferences()))
}

func mergeStringMap(existing, desired map[string]string) map[string]string {
	if len(existing) == 0 && len(desired) == 0 {
		return nil
	}
	merged := make(map[string]string, len(existing)+len(desired))
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func mergeStringSlice(existing, desired []string) []string {
	if len(existing) == 0 {
		return desired
	}
	seen := make(map[string]struct{}, len(existing)+len(desired))
	merged := make([]string, 0, len(existing)+len(desired))
	for _, value := range existing {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	for _, value := range desired {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	return merged
}

func mergeOwnerReferences(existing, desired []metav1.OwnerReference) []metav1.OwnerReference {
	if len(existing) == 0 {
		return desired
	}
	seen := make(map[string]struct{}, len(existing)+len(desired))
	merged := make([]metav1.OwnerReference, 0, len(existing)+len(desired))
	for _, owner := range existing {
		key := ownerReferenceKey(owner)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, owner)
	}
	for _, owner := range desired {
		key := ownerReferenceKey(owner)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, owner)
	}
	return merged
}

func ownerReferenceKey(owner metav1.OwnerReference) string {
	if owner.UID != "" {
		return string(owner.UID)
	}
	return owner.APIVersion + "/" + owner.Kind + "/" + owner.Name
}

func toUnstructured(obj interface{}, kind string) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetAPIVersion(APIGroup + "/" + APIVersion)
	u.SetKind(kind)
	return u, nil
}

// GetServerPolicy returns the rendered policy for a specific server if available.
func (m *Manager) GetServerPolicy(ctx context.Context, namespace, serverName string) (map[string]interface{}, error) {
	// First, try to find a ConfigMap with the rendered policy
	configMapName := mcpdefaults.GatewayPolicyConfigMapName(serverName)
	configMap, err := m.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("policy not found for server %s/%s: %w", namespace, serverName, err)
	}

	policy := make(map[string]interface{})
	hasPolicyDocument := false
	if policyYAML, ok := configMap.Data["policy.yaml"]; ok {
		policy["yaml"] = policyYAML
		hasPolicyDocument = true
	}
	if policyJSON, ok := configMap.Data["policy.json"]; ok {
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(policyJSON), &decoded); err != nil {
			return nil, fmt.Errorf("failed to parse policy configmap %s/%s policy.json: %w", namespace, configMapName, err)
		}
		for key, value := range decoded {
			policy[key] = value
		}
		policy["json"] = policyJSON
		hasPolicyDocument = true
	}
	if !hasPolicyDocument {
		return nil, fmt.Errorf("policy configmap %s/%s does not contain policy.yaml or policy.json", namespace, configMapName)
	}
	policy["source"] = fmt.Sprintf("configmap/%s/%s", namespace, configMapName)

	return policy, nil
}
