package k8sprovision

import (
	"context"
	"errors"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const TraefikWatchRoleName = "traefik-watch"
const traefikNamespaceWatchArgPrefix = "--providers.kubernetesingress.namespaces="
const traefikCRDNamespaceWatchArgPrefix = "--providers.kubernetescrd.namespaces="

// TraefikWatchConfig is resolved by the service bootstrap. Provisioning uses
// explicit settings rather than reading process configuration itself.
type TraefikWatchConfig struct {
	Mode           string
	Namespace      string
	Deployment     string
	ServiceAccount string
}

func EnsureTeamTraefikWatch(ctx context.Context, client kubernetes.Interface, namespace string, cfg TraefikWatchConfig) error {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return nil
	}
	if cfg.Mode == "disabled" {
		return nil
	}
	// k3s Traefik watches ingresses cluster-wide without namespace-scoped args.
	if cfg.Mode == "auto" && strings.TrimSpace(cfg.Namespace) == "kube-system" {
		return nil
	}
	if cfg.Mode == "auto" {
		managed, err := traefikDeploymentHasNamespaceWatchArg(ctx, client, cfg)
		if err != nil {
			return err
		}
		if !managed {
			return nil
		}
	}
	if err := ensureTraefikWatchRBAC(ctx, client, namespace, cfg); err != nil {
		return err
	}
	if err := ensureTraefikDeploymentWatchesNamespace(ctx, client, namespace, cfg); err != nil {
		if cfg.Mode == "auto" && apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func ensureTraefikWatchRBAC(ctx context.Context, client kubernetes.Interface, namespace string, cfg TraefikWatchConfig) error {
	role := desiredTraefikWatchRole(namespace)
	if err := ensureTraefikWatchRole(ctx, client, role); err != nil {
		return err
	}
	binding := desiredTraefikWatchRoleBinding(namespace, cfg)
	return ensureTraefikWatchRoleBinding(ctx, client, binding)
}

func desiredTraefikWatchRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: TraefikWatchRoleName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints", "secrets", "configmaps"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}},
		},
	}
}

func ensureTraefikWatchRole(ctx context.Context, client kubernetes.Interface, role *rbacv1.Role) error {
	current, err := client.RbacV1().Roles(role.Namespace).Get(ctx, role.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().Roles(role.Namespace).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if roleRulesMatch(current, role) {
		return nil
	}
	role.ResourceVersion = current.ResourceVersion
	role.Labels = current.Labels
	role.Annotations = current.Annotations
	_, err = client.RbacV1().Roles(role.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	return err
}

func desiredTraefikWatchRoleBinding(namespace string, cfg TraefikWatchConfig) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: TraefikWatchRoleName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     TraefikWatchRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: cfg.ServiceAccount, Namespace: cfg.Namespace},
		},
	}
}

func ensureTraefikWatchRoleBinding(ctx context.Context, client kubernetes.Interface, binding *rbacv1.RoleBinding) error {
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
	binding.ResourceVersion = current.ResourceVersion
	binding.Labels = current.Labels
	binding.Annotations = current.Annotations
	_, err = client.RbacV1().RoleBindings(binding.Namespace).Update(ctx, binding, metav1.UpdateOptions{})
	return err
}

func traefikDeploymentHasNamespaceWatchArg(ctx context.Context, client kubernetes.Interface, cfg TraefikWatchConfig) (bool, error) {
	deployment, err := client.AppsV1().Deployments(cfg.Namespace).Get(ctx, cfg.Deployment, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _, _, ok := traefikNamespaceWatchArg(deployment)
	return ok, nil
}

func ensureTraefikDeploymentWatchesNamespace(ctx context.Context, client kubernetes.Interface, namespace string, cfg TraefikWatchConfig) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(cfg.Namespace).Get(ctx, cfg.Deployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		containerIndex, argIndex, argValue, ok := traefikNamespaceWatchArg(deployment)
		if !ok {
			if cfg.Mode == "auto" {
				return nil
			}
			return errors.New("traefik deployment does not expose --providers.kubernetesingress.namespaces")
		}
		watched := splitCSV(strings.TrimPrefix(argValue, traefikNamespaceWatchArgPrefix))
		alreadyWatched := false
		for _, watchedNamespace := range watched {
			if watchedNamespace == namespace {
				alreadyWatched = true
				break
			}
		}
		updated := deployment.DeepCopy()
		if !alreadyWatched {
			watched = append(watched, namespace)
			updated.Spec.Template.Spec.Containers[containerIndex].Args[argIndex] = traefikNamespaceWatchArgPrefix + strings.Join(watched, ",")
			appendTraefikNamespaceWatch(updated, traefikCRDNamespaceWatchArgPrefix, namespace)
		}
		// Drop deleted namespaces from both provider watches. Traefik v2.10
		// stalls the whole CRD provider on a forbidden list/watch (no
		// traefik-watch Role in a gone namespace), so IngressRoutes 404 and the
		// Traefik→gateway mTLS hop never runs.
		if changed, pruneErr := pruneMissingTraefikNamespaceWatches(ctx, client, updated); pruneErr != nil {
			return pruneErr
		} else if alreadyWatched && !changed {
			return nil
		}
		_, err = client.AppsV1().Deployments(cfg.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return err
		}
		if err != nil {
			return err
		}
		return nil
	})
}

// RemoveTraefikDeploymentWatchesNamespace drops a team namespace from both
// Traefik provider watch args. Best-effort: missing Traefik deployment or
// watch args are ignored so team delete still succeeds.
func RemoveTraefikDeploymentWatchesNamespace(ctx context.Context, client kubernetes.Interface, namespace string, cfg TraefikWatchConfig) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || client == nil {
		return nil
	}
	if cfg.Mode == "disabled" {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(cfg.Namespace).Get(ctx, cfg.Deployment, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		updated := deployment.DeepCopy()
		changed := false
		for _, prefix := range []string{traefikNamespaceWatchArgPrefix, traefikCRDNamespaceWatchArgPrefix} {
			if removeTraefikNamespaceWatch(updated, prefix, namespace) {
				changed = true
			}
		}
		if !changed {
			return nil
		}
		_, err = client.AppsV1().Deployments(cfg.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return err
		}
		return err
	})
}

func pruneMissingTraefikNamespaceWatches(ctx context.Context, client kubernetes.Interface, deployment *appsv1.Deployment) (bool, error) {
	if deployment == nil || client == nil {
		return false, nil
	}
	changed := false
	for _, prefix := range []string{traefikNamespaceWatchArgPrefix, traefikCRDNamespaceWatchArgPrefix} {
		for ci := range deployment.Spec.Template.Spec.Containers {
			for ai, arg := range deployment.Spec.Template.Spec.Containers[ci].Args {
				if !strings.HasPrefix(arg, prefix) {
					continue
				}
				watched := splitCSV(strings.TrimPrefix(arg, prefix))
				kept := make([]string, 0, len(watched))
				for _, ns := range watched {
					_, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						changed = true
						continue
					}
					if err != nil {
						return false, err
					}
					kept = append(kept, ns)
				}
				if len(kept) != len(watched) {
					deployment.Spec.Template.Spec.Containers[ci].Args[ai] = prefix + strings.Join(kept, ",")
					changed = true
				}
			}
		}
	}
	return changed, nil
}

func appendTraefikNamespaceWatch(deployment *appsv1.Deployment, prefix, namespace string) {
	if deployment == nil {
		return
	}
	for ci := range deployment.Spec.Template.Spec.Containers {
		for ai, arg := range deployment.Spec.Template.Spec.Containers[ci].Args {
			if !strings.HasPrefix(arg, prefix) {
				continue
			}
			watched := splitCSV(strings.TrimPrefix(arg, prefix))
			for _, watchedNamespace := range watched {
				if watchedNamespace == namespace {
					return
				}
			}
			watched = append(watched, namespace)
			deployment.Spec.Template.Spec.Containers[ci].Args[ai] = prefix + strings.Join(watched, ",")
			return
		}
	}
}

func removeTraefikNamespaceWatch(deployment *appsv1.Deployment, prefix, namespace string) bool {
	if deployment == nil || strings.TrimSpace(namespace) == "" {
		return false
	}
	changed := false
	for ci := range deployment.Spec.Template.Spec.Containers {
		for ai, arg := range deployment.Spec.Template.Spec.Containers[ci].Args {
			if !strings.HasPrefix(arg, prefix) {
				continue
			}
			watched := splitCSV(strings.TrimPrefix(arg, prefix))
			kept := make([]string, 0, len(watched))
			for _, watchedNamespace := range watched {
				if watchedNamespace == namespace {
					changed = true
					continue
				}
				kept = append(kept, watchedNamespace)
			}
			if changed {
				deployment.Spec.Template.Spec.Containers[ci].Args[ai] = prefix + strings.Join(kept, ",")
			}
			return changed
		}
	}
	return false
}

func traefikNamespaceWatchArg(deployment *appsv1.Deployment) (int, int, string, bool) {
	if deployment == nil {
		return -1, -1, "", false
	}
	containerIndex := -1
	argIndex := -1
	argValue := ""
	for ci, container := range deployment.Spec.Template.Spec.Containers {
		for ai, arg := range container.Args {
			if strings.HasPrefix(arg, traefikNamespaceWatchArgPrefix) {
				containerIndex = ci
				argIndex = ai
				argValue = arg
				break
			}
		}
		if containerIndex >= 0 {
			break
		}
	}
	if containerIndex < 0 {
		return -1, -1, "", false
	}
	return containerIndex, argIndex, argValue, true
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
