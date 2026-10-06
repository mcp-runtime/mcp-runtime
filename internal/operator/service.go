package operator

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func (r *MCPServerReconciler) reconcileService(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)
	ensureGatewaySpec(mcpServer)
	targetPort := servingPort(mcpServer)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpServer.Name,
			Namespace: mcpServer.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, service, func() error {
		// Existing routes remain untouched until a stable candidate can accept
		// traffic on the new port. This is persisted in the Service itself, so
		// restarting the operator does not lose the last serving route.
		if service.ResourceVersion != "" && len(service.Spec.Ports) > 0 && service.Spec.Ports[0].TargetPort != intstr.FromInt32(targetPort) {
			ready, err := r.hasReadyServingPod(ctx, mcpServer)
			if err != nil || !ready {
				return err
			}
		}
		labels := map[string]string{
			LabelApp:       mcpServer.Name,
			LabelManagedBy: LabelManagedByValue,
		}
		service.Labels = labels
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		if gatewayEnabled(mcpServer) {
			service.Annotations["prometheus.io/path"] = "/metrics"
			service.Annotations["prometheus.io/port"] = strconv.Itoa(DefaultGatewayMetricsPort)
			service.Annotations["prometheus.io/scrape"] = "true"
		}

		ports := []corev1.ServicePort{
			{
				Name:       "http",
				Port:       mcpServer.Spec.ServicePort,
				TargetPort: intstr.FromInt32(targetPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		if gatewayEnabled(mcpServer) {
			ports = append(ports, corev1.ServicePort{
				Name:       "metrics",
				Port:       DefaultGatewayMetricsPort,
				TargetPort: intstr.FromInt32(DefaultGatewayMetricsPort),
				Protocol:   corev1.ProtocolTCP,
			})
		}

		selector := map[string]string{LabelApp: mcpServer.Name, LabelManagedBy: LabelManagedByValue}
		// On a new service or a port transition exclude old revisions that
		// lack the desired listener. Preserve legacy selectors on upgrades
		// that do not change ports; their declared listener is compatible.
		if service.ResourceVersion == "" || service.Spec.Selector[servingPortLabel] != "" || (len(service.Spec.Ports) > 0 && service.Spec.Ports[0].TargetPort != intstr.FromInt32(targetPort)) {
			selector[servingPortLabel] = strconv.Itoa(int(targetPort))
		}
		service.Spec.Type = corev1.ServiceTypeClusterIP
		service.Spec.Selector = selector
		service.Spec.Ports = ports

		if err := ctrl.SetControllerReference(mcpServer, service, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return err
	}

	if op != controllerutil.OperationResultNone {
		logger.Info("Service reconciled", "operation", op, "name", service.Name)
	}

	return nil
}

const servingPortLabel = "mcpruntime.org/serving-port"

func servingPort(server *mcpv1alpha1.MCPServer) int32 {
	if gatewayEnabled(server) {
		return server.Spec.Gateway.Port
	}
	return server.Spec.Port
}

// A port-changing rollout cannot drain the old revision before a candidate
// serves. Override even Recreate or an explicit maxUnavailable for this step.
func (r *MCPServerReconciler) servingDeploymentStrategy(ctx context.Context, server *mcpv1alpha1.MCPServer) (appsv1.DeploymentStrategy, error) {
	strategy := deploymentStrategy(server)
	service := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, service); err != nil {
		if errors.IsNotFound(err) {
			return strategy, nil
		}
		return strategy, err
	}
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].TargetPort != intstr.FromInt32(servingPort(server)) {
		zero, one := intstr.FromInt(0), intstr.FromInt(1)
		strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one}}
	}
	return strategy, nil
}

func (r *MCPServerReconciler) hasReadyServingPod(ctx context.Context, server *mcpv1alpha1.MCPServer) (bool, error) {
	track := "stable"
	if desiredStableReplicas(server) == 0 && canaryEnabled(server) {
		track = "canary"
	}
	// Pods are not watched (RBAC grants list/patch only), so a cached List
	// would start a cluster-wide Pod informer that cannot watch and serves a
	// stale relisted view. Read pods uncached, like nudgeGatewayPodsForPolicy.
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(server.Namespace), client.MatchingLabels{LabelApp: server.Name, LabelManagedBy: LabelManagedByValue, servingPortLabel: strconv.Itoa(int(servingPort(server))), "mcpruntime.org/rollout-track": track}); err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		declaresPort := false
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.ContainerPort == servingPort(server) {
					declaresPort = true
				}
			}
		}
		if !declaresPort {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
	}
	return false, nil
}
