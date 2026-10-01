package kubeworkload

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	OperatorSecretAccessName = "mcp-runtime-operator-managed-secrets" // #nosec G101 -- ClusterRole name, not a credential
	// OperatorWorkloadAccessName grants Deployment, ServiceAccount and
	// Certificate mutation, which are indirect routes to a Secret.
	OperatorWorkloadAccessName = "mcp-runtime-operator-managed-workloads"
	OperatorServiceAccountName = "mcp-runtime-operator-controller-manager"
	OperatorNamespace          = "mcp-runtime"
	OperatorTrustBundleName    = "mcp-adapter-client-ca" // #nosec G101 -- object name, not a credential
)

// OperatorSecretNamespaceProtected excludes infrastructure and prospective
// namespace domains from tenant Secret grants, including before migration.
func OperatorSecretNamespaceProtected(namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || strings.HasPrefix(namespace, "kube-") {
		return true
	}
	switch namespace {
	case "default", "mcp-sentinel", "mcp-platform", "mcp-observability", "mcp-log-collector", "registry", OperatorNamespace, "cert-manager", "traefik":
		return true
	}
	return false
}

// EnsureOperatorSecretAccess binds the operator's namespace-local Secret and
// workload roles. It is invoked only after the installer or platform
// has established that the namespace hosts managed MCPServers.
func EnsureOperatorSecretAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	if OperatorSecretNamespaceProtected(namespace) {
		return fmt.Errorf("operator tenant Secret access is prohibited in namespace %q", namespace)
	}
	for _, clusterRole := range []string{OperatorSecretAccessName, OperatorWorkloadAccessName} {
		if err := ensureOperatorRoleBinding(ctx, client, namespace, clusterRole); err != nil {
			return err
		}
	}
	return nil
}

func ensureOperatorRoleBinding(ctx context.Context, client kubernetes.Interface, namespace, clusterRole string) error {
	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterRole, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: OperatorServiceAccountName, Namespace: OperatorNamespace}},
	}
	existing, err := client.RbacV1().RoleBindings(namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().RoleBindings(namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.RoleRef != desired.RoleRef {
		return fmt.Errorf("existing operator binding %q in namespace %q has a conflicting roleRef", clusterRole, namespace)
	}
	existing.Subjects = desired.Subjects
	_, err = client.RbacV1().RoleBindings(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// EnsureOperatorTrustBundleAccess pre-creates the public CA bundle Secret and
// grants only named get/update/patch. Kubernetes cannot restrict create by
// resourceNames, so the operator never gets Secret create in the TLS namespace.
// An existing bundle is preserved; the operator refreshes it after issuance.
func EnsureOperatorTrustBundleAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	if namespace == "" {
		return nil
	}
	if namespace == OperatorNamespace || namespace == "cert-manager" || namespace == "default" || strings.HasPrefix(namespace, "kube-") {
		return fmt.Errorf("operator trust bundle is prohibited in namespace %q", namespace)
	}
	_, err := client.CoreV1().Secrets(namespace).Get(ctx, OperatorTrustBundleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: OperatorTrustBundleName, Namespace: namespace}, Type: corev1.SecretTypeOpaque}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	name := "mcp-runtime-operator-trust-bundle"
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{OperatorTrustBundleName}, Verbs: []string{"get", "patch", "update"}}}}
	current, err := client.RbacV1().Roles(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{})
	} else if err == nil {
		current.Rules = role.Rules
		_, err = client.RbacV1().Roles(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: OperatorServiceAccountName, Namespace: OperatorNamespace}}}
	previous, err := client.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if previous.RoleRef != binding.RoleRef {
		return fmt.Errorf("existing operator trust bundle binding in namespace %q has a conflicting roleRef", namespace)
	}
	previous.Subjects = binding.Subjects
	_, err = client.RbacV1().RoleBindings(namespace).Update(ctx, previous, metav1.UpdateOptions{})
	return err
}
