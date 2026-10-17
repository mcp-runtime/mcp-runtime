package k8sprovision

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/pkg/kubeworkload"
)

// Provisioner owns namespace defaults and the order of namespace-local access
// setup. The caller supplies credential issuance, which may use a remote broker
// or native registry without importing HTTP handlers into this package.
type Provisioner struct {
	Client                   kubernetes.Interface
	EnsureRegistryPullSecret func(context.Context, kubernetes.Interface, string) error
}

func (p Provisioner) EnsureNamespace(ctx context.Context, namespace string, labels map[string]string, ingressFromNamespaces ...string) error {
	if p.Client == nil || strings.TrimSpace(namespace) == "" {
		return nil
	}
	if p.EnsureRegistryPullSecret == nil {
		return fmt.Errorf("registry pull secret provisioner is required")
	}
	if kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return fmt.Errorf("managed MCPServer namespace %q is reserved for platform infrastructure", namespace)
	}
	base := p.Client
	current, err := base.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: labels,
		}}
		if _, err := base.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if err != nil {
		return err
	} else if mergeNamespaceLabels(current, labels) {
		if _, err := base.CoreV1().Namespaces().Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if err := ensureResourceQuota(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureLimitRange(ctx, base, namespace); err != nil {
		return err
	}
	if err := EnsureDefaultDenyNetworkPolicy(ctx, base, namespace, ingressFromNamespaces...); err != nil {
		return err
	}
	if err := kubeworkload.EnsureServiceAccount(ctx, base, namespace); err != nil {
		return err
	}
	if err := EnsurePlatformAPISecretAccess(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureNamespaceOperatorSecretAccess(ctx, base, namespace); err != nil {
		return err
	}
	if err := p.EnsureRegistryPullSecret(ctx, base, namespace); err != nil {
		return fmt.Errorf("provision registry pull secret for namespace %q: %w", namespace, err)
	}
	return nil
}

// ensureNamespaceOperatorSecretAccess grants the operator Secret access only
// inside namespaces managed by the platform. The operator has no cluster-wide
// Secret permissions; this binding lets it reconcile server-local certificates
// and registry pull credentials without reaching unrelated platform Secrets.
func ensureNamespaceOperatorSecretAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	return kubeworkload.EnsureOperatorSecretAccess(ctx, client, namespace)
}

func mergeNamespaceLabels(ns *corev1.Namespace, labels map[string]string) bool {
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	changed := false
	for key, value := range labels {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if ns.Labels[key] == value {
			continue
		}
		ns.Labels[key] = value
		changed = true
	}
	return changed
}

func ensureResourceQuota(ctx context.Context, client kubernetes.Interface, ns string) error {
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-quota", Namespace: ns}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods:                   resource.MustParse("20"),
		corev1.ResourceRequestsCPU:            resource.MustParse("4"),
		corev1.ResourceRequestsMemory:         resource.MustParse("8Gi"),
		corev1.ResourceLimitsCPU:              resource.MustParse("8"),
		corev1.ResourceLimitsMemory:           resource.MustParse("16Gi"),
		corev1.ResourcePersistentVolumeClaims: resource.MustParse("4"),
	}}}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureLimitRange(ctx context.Context, client kubernetes.Interface, ns string) error {
	limit := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-limits", Namespace: ns}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Default: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}}}}
	if _, err := client.CoreV1().LimitRanges(ns).Create(ctx, limit, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
