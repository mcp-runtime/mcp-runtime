package operator

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/operatorutil"
)

func (r *MCPServerReconciler) checkPolicyConfigMapReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	if !gatewayEnabled(mcpServer) {
		return false, nil
	}
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: gatewayPolicyConfigMapName(mcpServer.Name), Namespace: mcpServer.Namespace}, configMap); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	_, ok := configMap.Data[gatewayPolicyFileName]
	return ok, nil
}

func (r *MCPServerReconciler) checkCanaryDeploymentReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	if !canaryEnabled(mcpServer) {
		return true, nil
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: canaryDeploymentName(mcpServer.Name), Namespace: mcpServer.Namespace}, deployment); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return controlplane.DeploymentReady(*deployment, 0), nil
}
func (r *MCPServerReconciler) checkDeploymentReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, deployment); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return controlplane.DeploymentReady(*deployment, 1), nil
}

func (r *MCPServerReconciler) checkServiceReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	service := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, service); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return service.Spec.ClusterIP != "", nil
}

func (r *MCPServerReconciler) checkIngressReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	if r.usesAdapterCertificates(mcpServer) {
		// Optional adapter certificate verification serves traffic through a
		// path-based Traefik IngressRoute (the legacy passthrough IngressRouteTCP
		// is deleted during reconcile), so readiness must track the IngressRoute
		// and the Secrets Traefik needs for the re-encrypted hop.
		ready, err := r.mtlsBackendSecretsReady(ctx, mcpServer)
		if err != nil || !ready {
			return ready, err
		}
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(ingressRouteGVK)
		if err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, route); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	ingress := &networkingv1.Ingress{}
	if err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, ingress); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if len(ingress.Status.LoadBalancer.Ingress) > 0 {
		return true, nil
	}

	mode, _ := NormalizeIngressReadinessMode(r.IngressReadinessMode)
	if mode != IngressReadinessModePermissive {
		return false, nil
	}

	return len(ingress.Spec.Rules) > 0, nil
}

func (r *MCPServerReconciler) updateStatus(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, phase, message string, readiness resourceReadiness) {
	logger := log.FromContext(ctx)

	// Re-fetch the object to get the latest resourceVersion
	latest := &mcpv1alpha1.MCPServer{}
	if err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, latest); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("MCPServer not found, skipping status update (may have been deleted)")
			return
		}
		logger.Error(err, "Failed to fetch MCPServer for status update, using original object")
		latest = mcpServer
	}

	// Update status fields
	latest.Status.Phase = phase
	latest.Status.Message = message
	latest.Status.DeploymentReady = readiness.Deployment
	latest.Status.ServiceReady = readiness.Service
	latest.Status.IngressReady = readiness.Ingress
	latest.Status.GatewayReady = readiness.Gateway
	latest.Status.PolicyReady = readiness.Policy
	latest.Status.CanaryReady = readiness.Canary
	// mcpServer is the defaulted copy, so the URL reflects operator-wide
	// ingress defaults the stored spec may not spell out.
	latest.Status.URL = mcpServer.CanonicalResourceURL(r.publicURLOptions())

	// Update all conditions using the centralized helper
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.DeploymentReady, readiness.Deployment, phase, message, latest.Generation)
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.ServiceReady, readiness.Service, phase, message, latest.Generation)
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.IngressReady, readiness.Ingress, phase, message, latest.Generation)
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.GatewayReady, readiness.Gateway, phase, message, latest.Generation)
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.PolicyReady, readiness.Policy, phase, message, latest.Generation)
	operatorutil.SetCondition(&latest.Status.Conditions, operatorutil.CanaryReady, readiness.Canary, phase, message, latest.Generation)

	// Use Status().Update() which only updates the status subresource
	if err := r.Status().Update(ctx, latest); err != nil {
		if errors.IsConflict(err) {
			logger.V(1).Info("Status update conflict (expected in concurrent reconciles), will retry on next reconcile", "resourceVersion", latest.ResourceVersion)
		} else {
			logger.Error(err, "Failed to update MCPServer status", "resourceVersion", latest.ResourceVersion)
		}
	}
}
