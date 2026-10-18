package platform

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
	"strings"
)

// checkStuckOperatorDeployment warns when the operator Deployment from a
// previous setup run is stuck in ImagePullBackOff or CrashLoopBackOff. Setup
// will overwrite the image, but a stuck Pod may delay the new rollout.
func checkStuckOperatorDeployment(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	deploy, err := k8sclient.GetDeployment(ctx, clients, core.NamespaceMCPRuntime, core.OperatorDeploymentName)
	if err != nil || deploy == nil {
		return nil
	}
	pods, err := clients.Clientset.CoreV1().Pods(core.NamespaceMCPRuntime).List(ctx, metav1.ListOptions{LabelSelector: "control-plane=controller-manager"})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	var stuckPods []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			waiting := cs.State.Waiting
			if waiting == nil {
				continue
			}
			if waiting.Reason == "ImagePullBackOff" || waiting.Reason == "CrashLoopBackOff" || waiting.Reason == "ErrImagePull" {
				stuckPods = append(stuckPods, fmt.Sprintf("%s (%s: %s)", pod.Name, cs.Name, waiting.Reason))
			}
		}
	}
	if len(stuckPods) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Operator pod(s) from a previous setup run are stuck: %s.\n"+
				"         Setup will update the image, but cleaning up the stuck pod(s) first speeds up the rollout.",
			strings.Join(stuckPods, "; "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete pod -n %s -l control-plane=controller-manager", core.NamespaceMCPRuntime),
		},
	}}
}

// checkTerminatingNamespaces detects namespaces stuck in Terminating phase.
// kubectl apply into a Terminating namespace hangs or fails with cryptic errors.
func checkTerminatingNamespaces(ctx context.Context, clients *k8sclient.Clients, includeAnalytics bool) []preflightIssue {
	namespaces := []string{core.NamespaceMCPRuntime, core.NamespaceRegistry}
	if includeAnalytics {
		namespaces = append(namespaces,
			core.ComponentNamespace("platform-api"),
			core.ComponentNamespace("analytics-api"),
			core.ComponentNamespace("promtail"),
		)
	}
	var stuck []string
	for _, ns := range namespaces {
		if terminating, err := k8sclient.IsNamespaceTerminating(ctx, clients, ns); err == nil && terminating {
			stuck = append(stuck, ns)
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	var fixes []string
	for _, ns := range stuck {
		fixes = append(fixes, fmt.Sprintf("kubectl patch ns %s -p '{\"metadata\":{\"finalizers\":null}}' --type=merge", ns))
	}
	return []preflightIssue{{
		fatal:   true,
		message: fmt.Sprintf("Namespace(s) %v are stuck in Terminating — kubectl apply will hang until they are freed.", stuck),
		cleanup: fixes,
	}}
}

// checkTerminatingCRD detects when the mcpservers CRD is stuck deleting.
// Applying a new CRD version over a Terminating one silently fails.
func checkTerminatingCRD(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	const crdName = "mcpservers.mcpruntime.org"
	if terminating, err := k8sclient.IsCRDTerminating(ctx, clients, crdName); err == nil && terminating {
		return []preflightIssue{{
			fatal:   true,
			message: fmt.Sprintf("CRD %q is stuck in Terminating — remove its finalizers before re-running setup.", crdName),
			cleanup: []string{
				fmt.Sprintf("kubectl patch crd %s -p '{\"metadata\":{\"finalizers\":null}}' --type=merge", crdName),
			},
		}}
	}
	return nil
}

// checkClusterIdentity refuses to run against a cluster whose ID (the
// kube-system namespace UID) differs from --expect-cluster-id. An empty
// expectation disables the check.
func checkClusterIdentity(ctx context.Context, clients *k8sclient.Clients, want string) []preflightIssue {
	want = strings.TrimSpace(want)
	if want == "" {
		return nil
	}
	ns, err := clients.Clientset.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return []preflightIssue{{fatal: true, message: fmt.Sprintf("cannot read the kube-system namespace to verify --expect-cluster-id: %v", err)}}
	}
	if got := string(ns.UID); got != want {
		return []preflightIssue{{fatal: true, message: fmt.Sprintf("the target cluster ID is %s but --expect-cluster-id is %s; wrong or renamed context, nothing was changed", got, want)}}
	}
	return nil
}

// checkExistingMCPAuthRetained stops a setup run that would drop or swap an
// existing bundled mcp-auth server: its operator configuration and image are
// updated separately (`update --only mcp-auth`), never as a side effect.
func checkExistingMCPAuthRetained(ctx context.Context, clients *k8sclient.Clients, deploying bool, image string) []preflightIssue {
	component, ok := platforminventory.Lookup("mcp-auth")
	if !ok {
		return nil
	}
	deploy, err := k8sclient.GetDeployment(ctx, clients, component.Namespace, component.Resource)
	if err != nil || deploy == nil || len(deploy.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	live := deploy.Spec.Template.Spec.Containers[0].Image
	if !deploying {
		return []preflightIssue{{fatal: true,
			message: fmt.Sprintf("an MCP Auth server (%s) is installed but this setup run does not select it; rerun with --with-mcp-auth-server and its current profile so its operator configuration is kept", live)}}
	}
	if strings.TrimSpace(image) != live {
		return []preflightIssue{{fatal: true,
			message: fmt.Sprintf("setup must retain the installed MCP Auth image %s (got --mcp-auth-server-image %s); update it separately with `mcp-runtime update --only mcp-auth`", live, image)}}
	}
	return nil
}
