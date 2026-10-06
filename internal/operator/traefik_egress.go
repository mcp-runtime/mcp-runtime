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
	"k8s.io/apimachinery/pkg/api/meta"
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

	// TraefikEgressReadyCondition reports whether Traefik can reach the
	// server through the managed egress policy (or needs none).
	TraefikEgressReadyCondition      = "TraefikEgressReady"
	traefikEgressReasonPolicyApplied = "PolicyApplied"
	traefikEgressReasonUnrestricted  = "EgressUnrestricted"
	traefikEgressReasonNotApplicable = "NotTraefikIngress"
	traefikEgressReasonPodsNotFound  = "TraefikPodsNotFound"
)

// Warning events use the events.k8s.io API (controller-runtime GetEventRecorder).
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch;update

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
//
// It also records the TraefikEgressReady condition. When no Traefik pods match
// the configured identity, routing cannot be verified (typically the operator
// was not told the real Traefik namespace), so the condition turns False and a
// Warning event is emitted instead of silently skipping the policy.
func (r *MCPServerReconciler) reconcileTraefikEgress(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	key := types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Name}
	if !routesThroughTraefik(mcpServer) {
		r.setTraefikEgressCondition(ctx, mcpServer, true, traefikEgressReasonNotApplicable,
			fmt.Sprintf("ingress class %q does not route through Traefik", mcpServer.Spec.IngressClass))
		return r.deleteTraefikEgressPolicy(ctx, key)
	}
	traefikNamespace := r.ingressControllerNamespace()
	podLabels := r.ingressControllerPodLabels()
	podsFound, err := r.traefikPodsPresent(ctx, traefikNamespace)
	if err != nil {
		return err
	}
	if !podsFound {
		r.setTraefikEgressCondition(ctx, mcpServer, false, traefikEgressReasonPodsNotFound, fmt.Sprintf(
			"no Traefik pods match %s in namespace %s, so Traefik egress to this server cannot be managed or verified; "+
				"rerun setup so it passes the live Traefik identity (PLATFORM_TRAEFIK_NAMESPACE), or set MCP_INGRESS_CONTROLLER_NAMESPACE "+
				"and MCP_INGRESS_CONTROLLER_POD_LABELS on the operator",
			labels.SelectorFromSet(podLabels).String(), traefikNamespace))
	}
	restricted, err := r.traefikEgressRestricted(ctx, traefikNamespace)
	if err != nil {
		return err
	}
	if !restricted {
		// Traefik egress is unrestricted (for example k3s Traefik without the
		// bundled default-deny). A new egress policy would isolate those pods
		// and cut every other backend, so leave egress alone.
		if podsFound {
			r.setTraefikEgressCondition(ctx, mcpServer, true, traefikEgressReasonUnrestricted,
				fmt.Sprintf("Traefik egress in namespace %s is not restricted by a NetworkPolicy; no per-server rule is needed", traefikNamespace))
		}
		return r.deleteTraefikEgressPolicy(ctx, key)
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
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
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
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("Traefik egress NetworkPolicy reconciled", "operation", op, "name", policy.Name, "namespace", policy.Namespace, "ports", ports)
	}
	if podsFound {
		r.setTraefikEgressCondition(ctx, mcpServer, true, traefikEgressReasonPolicyApplied,
			fmt.Sprintf("NetworkPolicy %s/%s allows Traefik egress on TCP %v", policy.Namespace, policy.Name, ports))
	}
	return nil
}

// traefikPodsPresent reports whether any pod in the ingress controller
// namespace matches the configured Traefik pod labels. Pods are read
// uncached: the operator may list but not watch pods.
func (r *MCPServerReconciler) traefikPodsPresent(ctx context.Context, traefikNamespace string) (bool, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(traefikNamespace), client.MatchingLabels(r.ingressControllerPodLabels()), client.Limit(1)); err != nil {
		return false, err
	}
	return len(pods.Items) > 0, nil
}

// setTraefikEgressCondition writes the TraefikEgressReady condition when it
// changes and emits a Warning event when it turns False. Failures are logged,
// not returned: the condition is diagnostic and must not block routing.
func (r *MCPServerReconciler) setTraefikEgressCondition(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, ready bool, reason, message string) {
	logger := log.FromContext(ctx)
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	latest := &mcpv1alpha1.MCPServer{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Name}, latest); err != nil {
		logger.V(1).Info("Skipping TraefikEgressReady condition update", "error", err.Error())
		return
	}
	existing := meta.FindStatusCondition(latest.Status.Conditions, TraefikEgressReadyCondition)
	if existing != nil && existing.Status == status && existing.Reason == reason && existing.Message == message && existing.ObservedGeneration == latest.Generation {
		return
	}
	if !ready {
		logger.Info("Traefik egress cannot be managed", "reason", reason, "message", message)
		if r.Recorder != nil && (existing == nil || existing.Status != status || existing.Reason != reason) {
			r.Recorder.Eventf(latest, nil, corev1.EventTypeWarning, reason, "ReconcileTraefikEgress", "%s", message)
		}
	}
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type:               TraefikEgressReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: latest.Generation,
	})
	if err := r.Status().Update(ctx, latest); err != nil {
		logger.V(1).Info("TraefikEgressReady condition update failed; retrying on next reconcile", "error", err.Error())
	}
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
