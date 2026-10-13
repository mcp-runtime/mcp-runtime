package update

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/platformrelease"
)

// Platform object actions.
const (
	ObjectActionCreate  = "create"
	ObjectActionUpdate  = "update"
	ObjectActionSkipped = "skipped"
)

// ObjectResult reports one platform ServiceAccount/RBAC object.
type ObjectResult struct {
	Key    string `json:"key"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

const objectFieldManager = "mcp-runtime-update"

// refineObjectPlan reads each release-declared object and keeps only those
// that are missing or differ, so dry-run and apply stay targeted. It also
// blocks a workload that needs a ServiceAccount that neither exists nor will
// be created (for example when --only skipped the platform objects).
func refineObjectPlan(ctx context.Context, cs kubernetes.Interface, plan *Plan) error {
	var needed []platformrelease.PlatformObject
	plan.PlatformObjects = nil
	for _, obj := range plan.allObjects {
		action, err := objectAction(ctx, cs, obj)
		if err != nil {
			return fmt.Errorf("compare %s: %w", obj.Key(), err)
		}
		plan.PlatformObjects = append(plan.PlatformObjects, ObjectResult{Key: obj.Key(), Action: action})
		if action != ObjectActionSkipped {
			needed = append(needed, obj)
		}
	}
	plan.objects = needed

	for i := range plan.Rows {
		row := &plan.Rows[i]
		if row.wiring == nil || row.wiring.serviceAccount == "" || row.Action != ActionUpdate {
			continue
		}
		sa := row.wiring.serviceAccount
		if serviceAccountPlanned(needed, row.Namespace, sa) {
			continue
		}
		if _, err := cs.CoreV1().ServiceAccounts(row.Namespace).Get(ctx, sa, metav1.GetOptions{}); err == nil {
			continue
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("read ServiceAccount %s/%s: %w", row.Namespace, sa, err)
		}
		row.Action = ActionBlocked
		row.Reason = fmt.Sprintf("needs ServiceAccount %s/%s, which does not exist; re-run without --only so update can create the release's platform objects", row.Namespace, sa)
	}
	return nil
}

func serviceAccountPlanned(objs []platformrelease.PlatformObject, namespace, name string) bool {
	for _, o := range objs {
		if o.Kind == "ServiceAccount" && o.Namespace == namespace && o.Name == name {
			return true
		}
	}
	return false
}

func objectAction(ctx context.Context, cs kubernetes.Interface, obj platformrelease.PlatformObject) (string, error) {
	switch obj.Kind {
	case "ServiceAccount":
		want := &corev1.ServiceAccount{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return "", err
		}
		live, err := cs.CoreV1().ServiceAccounts(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ObjectActionCreate, nil
		}
		if err != nil {
			return "", err
		}
		if !reflect.DeepEqual(want.AutomountServiceAccountToken, live.AutomountServiceAccountToken) {
			return ObjectActionUpdate, nil
		}
	case "Role":
		want := &rbacv1.Role{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return "", err
		}
		live, err := cs.RbacV1().Roles(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ObjectActionCreate, nil
		}
		if err != nil {
			return "", err
		}
		if !rulesEqual(want.Rules, live.Rules) {
			return ObjectActionUpdate, nil
		}
	case "ClusterRole":
		want := &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return "", err
		}
		live, err := cs.RbacV1().ClusterRoles().Get(ctx, obj.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ObjectActionCreate, nil
		}
		if err != nil {
			return "", err
		}
		if !rulesEqual(want.Rules, live.Rules) {
			return ObjectActionUpdate, nil
		}
	case "RoleBinding":
		want := &rbacv1.RoleBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return "", err
		}
		live, err := cs.RbacV1().RoleBindings(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ObjectActionCreate, nil
		}
		if err != nil {
			return "", err
		}
		if !reflect.DeepEqual(want.RoleRef, live.RoleRef) || !subjectsEqual(want.Subjects, live.Subjects) {
			return ObjectActionUpdate, nil
		}
	case "ClusterRoleBinding":
		want := &rbacv1.ClusterRoleBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return "", err
		}
		live, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, obj.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ObjectActionCreate, nil
		}
		if err != nil {
			return "", err
		}
		if !reflect.DeepEqual(want.RoleRef, live.RoleRef) || !subjectsEqual(want.Subjects, live.Subjects) {
			return ObjectActionUpdate, nil
		}
	default:
		return "", fmt.Errorf("unsupported kind %q", obj.Kind)
	}
	return ObjectActionSkipped, nil
}

func rulesEqual(a, b []rbacv1.PolicyRule) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func subjectsEqual(a, b []rbacv1.Subject) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// applyPlatformObjects creates or corrects the planned objects. They are
// additive identity/permission grants and are not rolled back with images.
func applyPlatformObjects(ctx context.Context, cs kubernetes.Interface, plan *Plan, progress func(string)) ([]ObjectResult, error) {
	var results []ObjectResult
	for _, obj := range plan.objects {
		action, err := objectAction(ctx, cs, obj) // re-read: state may have moved since planning
		if err != nil {
			results = append(results, ObjectResult{Key: obj.Key(), Error: err.Error()})
			return results, err
		}
		if action == ObjectActionSkipped {
			results = append(results, ObjectResult{Key: obj.Key(), Action: ObjectActionSkipped})
			continue
		}
		progress(fmt.Sprintf("Applying %s (%s)", obj.Key(), action))
		if err := writeObject(ctx, cs, obj, action); err != nil {
			results = append(results, ObjectResult{Key: obj.Key(), Error: err.Error()})
			return results, fmt.Errorf("apply %s: %w", obj.Key(), err)
		}
		results = append(results, ObjectResult{Key: obj.Key(), Action: action})
	}
	return results, nil
}

func writeObject(ctx context.Context, cs kubernetes.Interface, obj platformrelease.PlatformObject, action string) error {
	create := metav1.CreateOptions{FieldManager: objectFieldManager}
	update := metav1.UpdateOptions{FieldManager: objectFieldManager}
	switch obj.Kind {
	case "ServiceAccount":
		want := &corev1.ServiceAccount{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return err
		}
		api := cs.CoreV1().ServiceAccounts(obj.Namespace)
		if action == ObjectActionCreate {
			_, err := api.Create(ctx, want, create)
			return err
		}
		live, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		live.AutomountServiceAccountToken = want.AutomountServiceAccountToken
		_, err = api.Update(ctx, live, update)
		return err
	case "Role":
		want := &rbacv1.Role{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return err
		}
		api := cs.RbacV1().Roles(obj.Namespace)
		if action == ObjectActionCreate {
			_, err := api.Create(ctx, want, create)
			return err
		}
		live, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		live.Rules = want.Rules
		_, err = api.Update(ctx, live, update)
		return err
	case "ClusterRole":
		want := &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return err
		}
		api := cs.RbacV1().ClusterRoles()
		if action == ObjectActionCreate {
			_, err := api.Create(ctx, want, create)
			return err
		}
		live, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		live.Rules = want.Rules
		_, err = api.Update(ctx, live, update)
		return err
	case "RoleBinding":
		want := &rbacv1.RoleBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return err
		}
		api := cs.RbacV1().RoleBindings(obj.Namespace)
		if action == ObjectActionCreate {
			_, err := api.Create(ctx, want, create)
			return err
		}
		live, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(live.RoleRef, want.RoleRef) { // roleRef is immutable
			if err := api.Delete(ctx, obj.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
			want.ResourceVersion = ""
			_, err := api.Create(ctx, want, create)
			return err
		}
		live.Subjects = want.Subjects
		_, err = api.Update(ctx, live, update)
		return err
	case "ClusterRoleBinding":
		want := &rbacv1.ClusterRoleBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, want); err != nil {
			return err
		}
		api := cs.RbacV1().ClusterRoleBindings()
		if action == ObjectActionCreate {
			_, err := api.Create(ctx, want, create)
			return err
		}
		live, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(live.RoleRef, want.RoleRef) { // roleRef is immutable
			if err := api.Delete(ctx, obj.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
			want.ResourceVersion = ""
			_, err := api.Create(ctx, want, create)
			return err
		}
		live.Subjects = want.Subjects
		_, err = api.Update(ctx, live, update)
		return err
	}
	return fmt.Errorf("unsupported kind %q", obj.Kind)
}
