package platform

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/platforminventory"
)

// This must run only after the complete setup pass has installed the scoped
// ingress, datasource isolation policies, and auth-proxy configuration. Update
// deliberately does not turn the feature on for unmigrated installations.
func enableScopedGrafanaClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	namespace := core.ComponentNamespace("runtime-api")
	if namespace != core.ComponentNamespace("ui") {
		return fmt.Errorf("scoped Grafana requires UI and runtime-api in the platform namespace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Remove legacy direct routes, including upgrades from the old namespace.
	for _, name := range []string{"mcp-platform-observability", "mcp-platform-gateway-observability"} {
		legacyNamespace := core.ComponentNamespace("grafana")
		if legacyNamespace != namespace {
			if err := deleteLegacyGrafanaIngress(ctx, clients.Clientset, legacyNamespace, name); err != nil {
				return err
			}
		}
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		config, err := clients.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, platforminventory.SharedConfigName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read scoped Grafana configuration: %w", err)
		}
		if config.Data == nil {
			config.Data = map[string]string{}
		}
		config.Data["GRAFANA_SCOPED_USER_ACCESS"] = "true"
		if _, err := clients.Clientset.CoreV1().ConfigMaps(namespace).Update(ctx, config, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("enable scoped Grafana: %w", err)
		}
		// envFrom is captured at process start; restart only runtime-api, whose
		// authorization controls link visibility and every tenant request.
		deployment, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, "mcp-runtime-api", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations["mcpruntime.org/scoped-grafana-enabled"] = time.Now().UTC().Format(time.RFC3339Nano)
		_, err = clients.Clientset.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}
	return waitForRolloutStatusWithClientGo("deployment", "mcp-runtime-api", namespace, 3*time.Minute)
}

func deleteLegacyGrafanaIngress(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
	err := client.NetworkingV1().Ingresses(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove legacy direct Grafana ingress %s/%s: %w", namespace, name, err)
	}
	return nil
}
