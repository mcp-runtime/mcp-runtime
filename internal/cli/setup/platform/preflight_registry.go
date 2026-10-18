package platform

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/platforminventory"
	"strings"
)

// checkStaleRegistryIngress warns when the registry Ingress exists with a
// host that does not match the expected registry hostname. This causes external
// TLS validation to fail and the platform API to use the wrong pull URL.
func checkStaleRegistryIngress(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	expectedHost := metadata.ResolveRegistryHost()
	if expectedHost == "" {
		return nil
	}
	ing, err := clients.Clientset.NetworkingV1().Ingresses(core.NamespaceRegistry).Get(
		ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return nil // ingress not created yet — fresh install
	}
	for _, rule := range ing.Spec.Rules {
		if rule.Host == expectedHost {
			return nil
		}
	}
	var current string
	if len(ing.Spec.Rules) > 0 {
		current = ing.Spec.Rules[0].Host
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Registry Ingress %q has host %q but the expected registry host is %q — setup will overwrite it.",
			core.RegistryServiceName, current, expectedHost,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete ingress %s -n %s  # setup will recreate with correct host", core.RegistryServiceName, core.NamespaceRegistry),
		},
	}}
}

// checkStuckRegistryPVC detects when the registry-storage PVC is stuck in
// Pending. A Pending PVC prevents the registry pod from scheduling, so the
// rollout-wait in verifySetup times out after 5+ minutes.
func checkStuckRegistryPVC(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	pvc, err := clients.Clientset.CoreV1().PersistentVolumeClaims(core.NamespaceRegistry).Get(
		ctx, "registry-storage", metav1.GetOptions{})
	if err != nil {
		return nil // PVC doesn't exist yet — fresh install
	}
	if pvc.Status.Phase != "Pending" {
		return nil
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"PersistentVolumeClaim %s/registry-storage is stuck in Pending — storage provisioning may have failed. The registry pod cannot schedule until this is resolved.",
			core.NamespaceRegistry,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl describe pvc registry-storage -n %s  # inspect the error", core.NamespaceRegistry),
			fmt.Sprintf("kubectl delete pvc registry-storage -n %s    # delete to force re-creation", core.NamespaceRegistry),
		},
	}}
}

// checkStuckRegistryDeployment warns when the registry Deployment from a
// previous run has pods stuck in ImagePullBackOff or CrashLoopBackOff.
// Setup will update the image, but stuck pods slow the rollout.
func checkStuckRegistryDeployment(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	deploy, err := k8sclient.GetDeployment(ctx, clients, core.NamespaceRegistry, core.RegistryServiceName)
	if err != nil || deploy == nil {
		return nil
	}
	pods, err := clients.Clientset.CoreV1().Pods(core.NamespaceRegistry).List(
		ctx, metav1.ListOptions{LabelSelector: "app=registry"})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	var stuck []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			w := cs.State.Waiting
			if w == nil {
				continue
			}
			if w.Reason == "ImagePullBackOff" || w.Reason == "CrashLoopBackOff" || w.Reason == "ErrImagePull" {
				stuck = append(stuck, fmt.Sprintf("%s (%s)", pod.Name, w.Reason))
			}
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Registry pod(s) from a previous run are stuck: %s — setup will update the image, but cleaning up first speeds up the rollout.",
			strings.Join(stuck, "; "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete pod -n %s -l app=registry", core.NamespaceRegistry),
		},
	}}
}

// checkStalePullSecrets detects when the mcp-runtime-registry-pull Secret in
// any namespace has a password that no longer matches the current UI_API_KEY.
// This causes ImagePullBackOff on new pods after a setup rerun rotates the key.
func checkStalePullSecrets(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	uiNamespace, uiSecret, ok := platforminventory.CredentialPlacement("UI_API_KEY")
	if !ok {
		return nil
	}
	currentKey, err := k8sclient.SecretStringDataValue(ctx, clients, uiNamespace, uiSecret, "UI_API_KEY")
	if err != nil || strings.TrimSpace(currentKey) == "" {
		return nil // secret not yet created; fresh install — nothing to check
	}

	namespaces := []string{core.ComponentNamespace("platform-api"), core.NamespaceMCPRuntime}
	var stale []string
	for _, ns := range namespaces {
		exists, err := k8sclient.SecretExists(ctx, clients, ns, defaultRegistrySecretName)
		if err != nil || !exists {
			continue
		}
		if nativeRegistryPullSecret(ctx, clients, ns) {
			// Native registry authentication replaced the copied service key
			// with a scoped node credential; it never matches UI_API_KEY.
			continue
		}
		storedAuth, err := k8sclient.SecretStringDataValue(ctx, clients, ns, defaultRegistrySecretName, ".dockerconfigjson")
		if err != nil {
			continue
		}
		// The pull secret password appears as base64(user:password) in the auth field.
		// A quick check: does the raw JSON contain the current key?
		if !strings.Contains(storedAuth, currentKey) {
			stale = append(stale, ns)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	var fixes []string
	for _, ns := range stale {
		fixes = append(fixes,
			fmt.Sprintf("kubectl create secret docker-registry %s -n %s --docker-server=%s --docker-username=platform-service --docker-password=<UI_API_KEY> --dry-run=client -o yaml | kubectl apply -f -",
				defaultRegistrySecretName, ns, core.GetRegistryIngressHost()),
		)
	}
	fixes = append(fixes, "# or re-run setup — it refreshes pull secrets automatically")
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Image pull secret %q in namespace(s) %v has a stale password — pods may get ImagePullBackOff after secret rotation.",
			defaultRegistrySecretName, stale,
		),
		cleanup: fixes,
	}}
}
