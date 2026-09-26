package platform

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
)

// ingressControllerIdentity is where the ingress controller actually runs.
// The operator uses it for the mtls gateway NetworkPolicy peer and the pinned
// ingress SPIFFE ID; without it the operator assumes the repo-managed
// traefik/traefik Deployment, which on k3s (Traefik in kube-system with Helm
// labels) makes every mtls gateway reject its own ingress.
type ingressControllerIdentity struct {
	Namespace      string
	ServiceAccount string
	PodLabels      map[string]string
}

// ingressControllerIdentityFromDeployment reads the identity from the Traefik
// Deployment: its selector labels are the stable pod identity the Deployment
// itself relies on, and its pod ServiceAccount is what a SPIFFE-style ID names.
func ingressControllerIdentityFromDeployment(deployment *appsv1.Deployment) ingressControllerIdentity {
	if deployment == nil {
		return ingressControllerIdentity{}
	}
	identity := ingressControllerIdentity{Namespace: deployment.Namespace}
	if deployment.Spec.Selector != nil && len(deployment.Spec.Selector.MatchLabels) > 0 {
		identity.PodLabels = deployment.Spec.Selector.MatchLabels
	}
	identity.ServiceAccount = strings.TrimSpace(deployment.Spec.Template.Spec.ServiceAccountName)
	if identity.ServiceAccount == "" {
		identity.ServiceAccount = "default"
	}
	return identity
}

// detectIngressControllerIdentity is a variable so tests can stub cluster access.
var detectIngressControllerIdentity = detectIngressControllerIdentityClientGo

// validateAdapterCertificateIngressIdentity stops setup when the optional
// certificate path is enabled but the ingress identity needed by its gateway
// NetworkPolicy and SPIFFE check cannot be discovered.
func validateAdapterCertificateIngressIdentity() error {
	if !adapterCertificatesEnabled() {
		return nil
	}
	identity := detectIngressControllerIdentity()
	if strings.TrimSpace(identity.Namespace) == "" || strings.TrimSpace(identity.ServiceAccount) == "" || len(identity.PodLabels) == 0 {
		return fmt.Errorf("MCP_ADAPTER_CERTIFICATES=true requires the Traefik Deployment identity; setup could not read its namespace, pod labels, and service account. Check Kubernetes access and PLATFORM_TRAEFIK_NAMESPACE, then retry")
	}
	return nil
}

func adapterCertificatesEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("MCP_ADAPTER_CERTIFICATES")))
	return err == nil && enabled
}

func detectIngressControllerIdentityClientGo() ingressControllerIdentity {
	namespace := strings.TrimSpace(setupAnalyticsConfigEnvValue("PLATFORM_TRAEFIK_NAMESPACE"))
	if namespace == "" {
		namespace = activeTraefikNamespaceForPlatformClientGo()
	}
	if namespace == "" {
		return ingressControllerIdentity{}
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		warnPartialIngressControllerIdentity(namespace, err)
		return ingressControllerIdentity{Namespace: namespace}
	}
	deployment, err := k8sclient.GetDeployment(context.Background(), clients, namespace, "traefik")
	if err != nil {
		warnPartialIngressControllerIdentity(namespace, err)
		return ingressControllerIdentity{Namespace: namespace}
	}
	return ingressControllerIdentityFromDeployment(deployment)
}

// warnPartialIngressControllerIdentity reports that only the namespace is
// known. The operator then assumes the repo Traefik's app=traefik pod label
// and traefik ServiceAccount, which do not match every Traefik install (k3s
// uses Helm labels); on a mismatch the adapter-certificate NetworkPolicy and
// the pinned ingress identity reject Traefik's own traffic.
func warnPartialIngressControllerIdentity(namespace string, err error) {
	core.Warn(fmt.Sprintf("could not read the traefik Deployment in namespace %s (%v); set MCP_INGRESS_CONTROLLER_POD_LABELS and MCP_INGRESS_CONTROLLER_SERVICE_ACCOUNT on the operator if Traefik does not use app=traefik and the traefik ServiceAccount", namespace, err))
}

func ingressControllerOperatorEnv(identity ingressControllerIdentity) []operatorEnvVar {
	var envVars []operatorEnvVar
	if namespace := strings.TrimSpace(identity.Namespace); namespace != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_INGRESS_CONTROLLER_NAMESPACE", Value: namespace})
	}
	if serviceAccount := strings.TrimSpace(identity.ServiceAccount); serviceAccount != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_INGRESS_CONTROLLER_SERVICE_ACCOUNT", Value: serviceAccount})
	}
	if len(identity.PodLabels) > 0 {
		envVars = append(envVars, operatorEnvVar{
			Name:  "MCP_INGRESS_CONTROLLER_POD_LABELS",
			Value: labels.SelectorFromSet(identity.PodLabels).String(),
		})
	}
	return envVars
}
