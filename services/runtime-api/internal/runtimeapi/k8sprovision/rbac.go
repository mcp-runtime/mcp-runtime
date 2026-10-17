package k8sprovision

import (
	"context"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/pkg/platformstack"
)

const (
	platformNamespaceOwnerRoleName = "platform-namespace-owner"
	PlatformAPISecretAccessName    = "mcp-runtime-api-team-secrets"
	PlatformAPIServiceAccountName  = "mcp-runtime-api"
	OperatorSecretAccessName       = "mcp-runtime-operator-managed-secrets"
)

func EnsurePlatformAPISecretAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: PlatformAPISecretAccessName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     PlatformAPISecretAccessName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      PlatformAPIServiceAccountName,
				Namespace: platformstack.PlatformNamespace,
			},
		},
	}
	return upsertRoleBinding(ctx, client, binding)
}

func EnsureUserWorkloadRBAC(ctx context.Context, client kubernetes.Interface, namespace, userID string) error {
	if client == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(userID) == "" {
		return nil
	}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: platformNamespaceOwnerRoleName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		},
	}
	if err := upsertRole(ctx, client, role); err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: platformNamespaceOwnerRoleName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     platformNamespaceOwnerRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.UserKind, Name: "platform:user:" + strings.TrimSpace(userID)},
		},
	}
	return upsertRoleBinding(ctx, client, binding)
}

func upsertRole(ctx context.Context, client kubernetes.Interface, role *rbacv1.Role) error {
	current, err := client.RbacV1().Roles(role.Namespace).Get(ctx, role.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().Roles(role.Namespace).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	role.ResourceVersion = current.ResourceVersion
	role.Labels = current.Labels
	role.Annotations = current.Annotations
	_, err = client.RbacV1().Roles(role.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	return err
}

func upsertRoleBinding(ctx context.Context, client kubernetes.Interface, binding *rbacv1.RoleBinding) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.RbacV1().RoleBindings(binding.Namespace).Get(ctx, binding.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = client.RbacV1().RoleBindings(binding.Namespace).Create(ctx, binding, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if roleBindingMatches(current, binding) {
			return nil
		}
		updated := binding.DeepCopy()
		updated.ResourceVersion = current.ResourceVersion
		updated.Labels = current.Labels
		updated.Annotations = current.Annotations
		_, err = client.RbacV1().RoleBindings(binding.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

func roleBindingMatches(current, desired *rbacv1.RoleBinding) bool {
	if current == nil || desired == nil {
		return false
	}
	if current.RoleRef != desired.RoleRef || len(current.Subjects) != len(desired.Subjects) {
		return false
	}
	for i := range desired.Subjects {
		if current.Subjects[i] != desired.Subjects[i] {
			return false
		}
	}
	return true
}

func roleRulesMatch(current, desired *rbacv1.Role) bool {
	if current == nil || desired == nil {
		return false
	}
	if len(current.Rules) != len(desired.Rules) {
		return false
	}
	for i := range desired.Rules {
		if !policyRuleMatches(current.Rules[i], desired.Rules[i]) {
			return false
		}
	}
	return true
}

func policyRuleMatches(current, desired rbacv1.PolicyRule) bool {
	return slicesEqual(current.APIGroups, desired.APIGroups) &&
		slicesEqual(current.Resources, desired.Resources) &&
		slicesEqual(current.Verbs, desired.Verbs)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
