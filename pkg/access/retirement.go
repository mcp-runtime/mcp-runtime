package access

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ServerRetiringAnnotation prevents issuing access or republishing a server
// while a failed retirement is awaiting a retry.
const ServerRetiringAnnotation = "mcpruntime.org/retiring"

// ServerUIDAnnotation fences delayed authorization writes from an earlier
// deployment when the same namespace/name is published again.
const ServerUIDAnnotation = "mcpruntime.org/server-uid"

func (m *Manager) bindServerUID(ctx context.Context, object metav1.Object, ref ServerReference) error {
	if strings.TrimSpace(string(ref.Namespace)) == "" {
		ref.Namespace = Namespace(object.GetNamespace())
	}
	server, err := m.GetMCPServerRef(ctx, ref)
	if IsMCPServerNotFoundForRef(err) {
		// Preserve the manager's low-level import behavior. The API validates
		// existence before calling Apply; a previously bound import stays fenced.
		return nil
	}
	if err != nil {
		return err
	}
	if server.UID == "" {
		return nil
	}
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if bound := annotations[ServerUIDAnnotation]; bound != "" && bound != string(server.UID) {
		return fmt.Errorf("authorization belongs to a retired server deployment")
	}
	annotations[ServerUIDAnnotation] = string(server.UID)
	object.SetAnnotations(annotations)
	return nil
}

// RetireServerAccess invalidates and removes all authorization referencing the
// exact server identity, including resources held in other namespaces. It does
// not remove agents or audit history. A failure is safe to retry: all records are
// invalidated before any record is removed, and missing records are tolerated.
func (m *Manager) RetireServerAccess(ctx context.Context, namespace, name string) error {
	grants, err := m.ListGrants(ctx, "")
	if err != nil {
		return err
	}
	sessions, err := m.ListSessions(ctx, "")
	if err != nil {
		return err
	}
	matches := func(objectNamespace string, ref ServerReference) bool {
		refNamespace := strings.TrimSpace(string(ref.Namespace))
		if refNamespace == "" {
			refNamespace = objectNamespace
		}
		return refNamespace == namespace && string(ref.Name) == name
	}
	for _, grant := range grants.Items {
		if matches(grant.Namespace, grant.Spec.ServerRef) && !grant.Spec.Disabled {
			patch := map[string]interface{}{"metadata": map[string]interface{}{"uid": grant.UID, "resourceVersion": grant.ResourceVersion}, "spec": map[string]interface{}{"disabled": true}}
			if err := m.patchGrant(ctx, grant.Name, grant.Namespace, patch); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("disable server grant: %w", err)
			}
		}
	}
	for _, session := range sessions.Items {
		if matches(session.Namespace, session.Spec.ServerRef) && !session.Spec.Revoked {
			patch := map[string]interface{}{"metadata": map[string]interface{}{"uid": session.UID, "resourceVersion": session.ResourceVersion}, "spec": map[string]interface{}{"revoked": true}}
			if err := m.patchSession(ctx, session.Name, session.Namespace, patch); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("revoke server session: %w", err)
			}
		}
	}
	for _, session := range sessions.Items {
		if matches(session.Namespace, session.Spec.ServerRef) {
			uid := session.UID
			if err := m.dynamic.Resource(sessionGVR).Namespace(session.Namespace).Delete(ctx, session.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	for _, grant := range grants.Items {
		if matches(grant.Namespace, grant.Spec.ServerRef) {
			uid := grant.UID
			if err := m.dynamic.Resource(grantGVR).Namespace(grant.Namespace).Delete(ctx, grant.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
