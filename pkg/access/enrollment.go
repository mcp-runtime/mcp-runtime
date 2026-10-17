package access

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"mcp-runtime/pkg/sessionstate"
)

// CompleteSessionEnrollment removes only pending-enrollment metadata. It must
// not replace a stale session binding and undo a concurrent revocation.
func (m *Manager) CompleteSessionEnrollment(ctx context.Context, name, namespace, uid string) error {
	if m.sessions != nil {
		backend, ok := m.sessions.(interface {
			CompleteSessionEnrollment(context.Context, string, string, string) error
		})
		if !ok {
			return fmt.Errorf("conditional enrollment completion is unavailable")
		}
		return backend.CompleteSessionEnrollment(ctx, name, namespace, uid)
	}
	session, err := m.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(session.UID) != uid || session.Spec.Revoked || session.Spec.ExpiresAt == nil || !session.Spec.ExpiresAt.After(time.Now()) {
		return apierrors.NewConflict(sessionGVR.GroupResource(), name, fmt.Errorf("session enrollment is no longer active"))
	}
	if session.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] != "true" {
		return nil
	}
	delete(session.Annotations, sessionstate.AdapterEnrollmentPendingAnnotation)
	obj, err := toUnstructured(session, "MCPAgentSession")
	if err != nil {
		return err
	}
	// Update retains the resourceVersion read above, so a concurrent revoke
	// causes a conflict instead of being overwritten on the explicit Kube path.
	_, err = m.dynamic.Resource(sessionGVR).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// DeletePendingSession checks owner, immutable identity and pending state in
// the same mutation that removes the session. Completed enrollments survive.
func (m *Manager) DeletePendingSession(ctx context.Context, name, namespace, uid, humanID string) error {
	if m.sessions != nil {
		backend, ok := m.sessions.(interface {
			DeletePendingSession(context.Context, string, string, string, string) error
		})
		if !ok {
			return fmt.Errorf("conditional pending-session cleanup is unavailable")
		}
		return backend.DeletePendingSession(ctx, name, namespace, uid, humanID)
	}
	session, err := m.GetSession(ctx, name, namespace)
	if err != nil {
		return err
	}
	if string(session.UID) != uid || humanID == "" || string(session.Spec.Subject.HumanID) != humanID || session.Annotations[sessionstate.AdapterEnrollmentPendingAnnotation] != "true" {
		return apierrors.NewConflict(sessionGVR.GroupResource(), name, fmt.Errorf("session is not a pending enrollment owned by the caller"))
	}
	opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{}}
	if session.UID != "" {
		identity := types.UID(uid)
		opts.Preconditions.UID = &identity
	}
	if session.ResourceVersion != "" {
		opts.Preconditions.ResourceVersion = &session.ResourceVersion
	}
	return m.dynamic.Resource(sessionGVR).Namespace(namespace).Delete(ctx, name, opts)
}
