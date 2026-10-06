package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

// Per-server Traefik egress (issue #618).
//
// The bundled Traefik runs under a default-deny NetworkPolicy, so it can only
// reach a backend whose port is explicitly allowed. Rather than a static port
// list for every MCP server namespace, the operator keeps one narrow egress
// policy per MCPServer in the ingress controller namespace. It selects the
// Traefik pods and allows TCP egress only to that server's pods (app=<name>
// in the server namespace) on the port the Service routes to.
//
// The policy lives in a different namespace from its MCPServer, so it cannot
// carry an owner reference. Labels identify the owning server; the reconciler
// deletes the policy when the server is gone or no longer routes through
// Traefik, and a startup sweep removes policies orphaned while the operator
// was not running.
const (
	traefikEgressComponent      = "traefik-egress"
	labelComponent              = "mcpruntime.org/component"
	labelServerName             = "mcpruntime.org/server"
	labelServerNamespace        = "mcpruntime.org/server-namespace"
	traefikEgressPolicyPrefix   = "mcp-egress-"
	traefikEgressNameHashLength = 8
)

func traefikEgressPolicyName(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	suffix := hex.EncodeToString(sum[:])[:traefikEgressNameHashLength]
	base := traefikEgressPolicyPrefix + namespace + "-" + name
	// NetworkPolicy names are DNS subdomains (<=253). The hash keeps names
	// unique when namespace/name pairs share a dashed concatenation.
	if limit := 253 - len(suffix) - 1; len(base) > limit {
		base = strings.TrimRight(base[:limit], "-.")
	}
	return base + "-" + suffix
}

func traefikEgressLabels(namespace, name string) map[string]string {
	return map[string]string{
		LabelManagedBy:       LabelManagedByValue,
		labelComponent:       traefikEgressComponent,
		labelServerName:      name,
		labelServerNamespace: namespace,
	}
}

func routesThroughTraefik(mcpServer *mcpv1alpha1.MCPServer) bool {
	class := strings.TrimSpace(mcpServer.Spec.IngressClass)
	return class == "" || class == DefaultIngressClass
}

// reconcileTraefikEgress keeps the per-server egress policy aligned with the
// ports the server's Service can route to. Run it after reconcileService so a
// promoted port transition drops the retired port in the same pass.
func (r *MCPServerReconciler) reconcileTraefikEgress(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	traefikNamespace := r.ingressControllerNamespace()
	if !routesThroughTraefik(mcpServer) {
		return r.deleteTraefikEgressPolicy(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Name})
	}
	restricted, err := r.traefikEgressRestricted(ctx, traefikNamespace)
	if err != nil {
		return err
	}
	if !restricted {
		// Traefik egress is unrestricted (for example k3s Traefik without the
		// bundled default-deny). A new egress policy would isolate those pods
		// and cut every other backend, so leave egress alone.
		return r.deleteTraefikEgressPolicy(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Name})
	}
	ports, err := r.traefikEgressPorts(ctx, mcpServer)
	if err != nil {
		return err
	}

	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name:      traefikEgressPolicyName(mcpServer.Namespace, mcpServer.Name),
		Namespace: traefikNamespace,
	}}
	tcp := corev1.ProtocolTCP
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		policy.Labels = traefikEgressLabels(mcpServer.Namespace, mcpServer.Name)
		policyPorts := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
		for _, port := range ports {
			target := intstr.FromInt32(port)
			policyPorts = append(policyPorts, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &target})
		}
		policy.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: r.ingressControllerPodLabels()},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": mcpServer.Namespace},
					},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{LabelApp: mcpServer.Name},
					},
				}},
				Ports: policyPorts,
			}},
		}
		return nil
	})
	if apierrors.IsNotFound(err) {
		// The ingress controller namespace disappeared between the check and
		// the write; nothing routes through it.
		return nil
	}
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("Traefik egress NetworkPolicy reconciled", "operation", op, "name", policy.Name, "namespace", policy.Namespace, "ports", ports)
	}
	return nil
}

// traefikEgressPorts returns the desired serving port plus the port the
// Service currently routes to. During a port transition the Service keeps the
// old target until a candidate is ready, so both must stay reachable.
func (r *MCPServerReconciler) traefikEgressPorts(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) ([]int32, error) {
	ports := []int32{servingPort(mcpServer)}
	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Name}, service)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if err == nil && len(service.Spec.Ports) > 0 {
		target := service.Spec.Ports[0].TargetPort
		if target.Type == intstr.Int && target.IntVal > 0 && !slices.Contains(ports, target.IntVal) {
			ports = append(ports, target.IntVal)
		}
	}
	slices.Sort(ports)
	return ports, nil
}

// traefikEgressRestricted reports whether a policy not managed by this file
// already isolates the Traefik pods for egress, such as the bundled
// traefik-default-deny.
func (r *MCPServerReconciler) traefikEgressRestricted(ctx context.Context, traefikNamespace string) (bool, error) {
	var list networkingv1.NetworkPolicyList
	if err := r.List(ctx, &list, client.InNamespace(traefikNamespace)); err != nil {
		return false, err
	}
	podLabels := labels.Set(r.ingressControllerPodLabels())
	for i := range list.Items {
		policy := &list.Items[i]
		if policy.Labels[labelComponent] == traefikEgressComponent {
			continue
		}
		if !policyIsolatesEgress(policy) {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			continue
		}
		if selector.Matches(podLabels) {
			return true, nil
		}
	}
	return false, nil
}

func policyIsolatesEgress(policy *networkingv1.NetworkPolicy) bool {
	if len(policy.Spec.PolicyTypes) == 0 {
		// The API server defaults policyTypes; Egress is implied only when
		// egress rules are present.
		return len(policy.Spec.Egress) > 0
	}
	return slices.Contains(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
}

// deleteTraefikEgressPolicy removes the egress policy for a server, in any
// namespace, by its owning labels. It is called when the server is gone.
func (r *MCPServerReconciler) deleteTraefikEgressPolicy(ctx context.Context, server types.NamespacedName) error {
	var list networkingv1.NetworkPolicyList
	if err := r.List(ctx, &list, client.MatchingLabels{
		labelComponent:       traefikEgressComponent,
		labelServerName:      server.Name,
		labelServerNamespace: server.Namespace,
	}); err != nil {
		return err
	}
	for i := range list.Items {
		if err := r.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// sweepTraefikEgressPolicies deletes policies whose server no longer exists
// or that sit outside the current ingress controller namespace. It covers
// servers deleted while the operator was not running.
func (r *MCPServerReconciler) sweepTraefikEgressPolicies(ctx context.Context) error {
	var list networkingv1.NetworkPolicyList
	if err := r.List(ctx, &list, client.MatchingLabels{labelComponent: traefikEgressComponent}); err != nil {
		return err
	}
	traefikNamespace := r.ingressControllerNamespace()
	for i := range list.Items {
		policy := &list.Items[i]
		orphaned := policy.Namespace != traefikNamespace
		if !orphaned {
			server := &mcpv1alpha1.MCPServer{}
			key := types.NamespacedName{Namespace: policy.Labels[labelServerNamespace], Name: policy.Labels[labelServerName]}
			if key.Name == "" || key.Namespace == "" {
				orphaned = true
			} else if err := r.Get(ctx, key, server); err != nil {
				if !apierrors.IsNotFound(err) {
					return err
				}
				orphaned = true
			} else if !server.DeletionTimestamp.IsZero() {
				orphaned = true
			}
		}
		if !orphaned {
			continue
		}
		if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// requestsForTraefikEgressPolicy maps NetworkPolicy events to MCPServers. A
// managed policy enqueues its server so drift is repaired. A change to any
// other policy in the ingress controller namespace can flip whether Traefik
// egress is restricted, so it enqueues every server.
func (r *MCPServerReconciler) requestsForTraefikEgressPolicy(ctx context.Context, obj client.Object) []ctrl.Request {
	objLabels := obj.GetLabels()
	if objLabels[labelComponent] == traefikEgressComponent {
		name, namespace := objLabels[labelServerName], objLabels[labelServerNamespace]
		if name == "" || namespace == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
	}
	if obj.GetNamespace() != r.ingressControllerNamespace() {
		return nil
	}
	var servers mcpv1alpha1.MCPServerList
	if err := r.List(ctx, &servers); err != nil {
		log.FromContext(ctx).Error(err, "list MCPServers for Traefik egress policy change")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(servers.Items))
	for _, server := range servers.Items {
		requests = append(requests, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: server.Namespace, Name: server.Name}})
	}
	return requests
}

// traefikEgressSweeper runs the orphan sweep once after the cache syncs, on
// the elected leader only.
func (r *MCPServerReconciler) traefikEgressSweeper() manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		if err := r.sweepTraefikEgressPolicies(ctx); err != nil {
			// Not fatal: per-server reconciles still converge live servers.
			log.FromContext(ctx).Error(fmt.Errorf("sweep Traefik egress policies: %w", err), "Traefik egress sweep failed")
		}
		return nil
	})
}
