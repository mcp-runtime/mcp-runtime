package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

const maxDeploymentDiagnosticText = 512

// deploymentFailureDetails reports a current unready server pod and its most
// recent warning event. It reads only pods and events from the MCPServer's
// namespace; the corresponding event permission is bound only in managed
// workload namespaces.
func (r *MCPServerReconciler) deploymentFailureDetails(ctx context.Context, server *mcpv1alpha1.MCPServer) string {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(server.Namespace), client.MatchingLabels{
		LabelApp: server.Name, LabelManagedBy: LabelManagedByValue, "mcpruntime.org/rollout-track": "stable",
	}); err != nil {
		return ""
	}
	var candidates []corev1.Pod
	for i := range pods.Items {
		pod := pods.Items[i]
		if !diagnosticPodReady(&pod) {
			candidates = append(candidates, pod)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreationTimestamp.After(candidates[j].CreationTimestamp.Time)
	})
	pod := candidates[0]
	parts := []string{fmt.Sprintf("Deployment is not ready; pod=%s phase=%s", safeDeploymentDiagnostic(pod.Name), safeDeploymentDiagnostic(string(pod.Status.Phase)))}
	if container, reason, message := podContainerFailure(&pod); reason != "" {
		part := fmt.Sprintf("container=%s reason=%s", safeDeploymentDiagnostic(container), safeDeploymentDiagnostic(reason))
		if message != "" {
			part += fmt.Sprintf(" message=%q", safeDeploymentDiagnostic(message))
		}
		parts = append(parts, part)
	} else if pod.Status.Reason != "" {
		part := "podReason=" + safeDeploymentDiagnostic(pod.Status.Reason)
		if pod.Status.Message != "" {
			part += fmt.Sprintf(" message=%q", safeDeploymentDiagnostic(pod.Status.Message))
		}
		parts = append(parts, part)
	}
	if event := r.latestWarningEventForPod(ctx, server.Namespace, &pod); event != nil {
		message := safeDeploymentDiagnostic(event.Message)
		if message != "" {
			parts = append(parts, fmt.Sprintf("recentEvent=%q", message))
		}
	}
	return strings.Join(parts, "; ")
}

func diagnosticPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podContainerFailure(pod *corev1.Pod) (container, reason, message string) {
	for _, status := range pod.Status.InitContainerStatuses {
		if name, why, detail := containerFailure(status); why != "" {
			return name, why, detail
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if name, why, detail := containerFailure(status); why != "" {
			return name, why, detail
		}
	}
	return "", "", ""
}

func containerFailure(status corev1.ContainerStatus) (container, reason, message string) {
	if status.State.Waiting != nil {
		return status.Name, status.State.Waiting.Reason, status.State.Waiting.Message
	}
	if terminated := status.State.Terminated; terminated != nil {
		reason := terminated.Reason
		if reason == "" {
			reason = fmt.Sprintf("ExitCode%d", terminated.ExitCode)
		}
		return status.Name, reason, terminated.Message
	}
	if status.LastTerminationState.Terminated != nil {
		terminated := status.LastTerminationState.Terminated
		reason := terminated.Reason
		if reason == "" {
			reason = fmt.Sprintf("ExitCode%d", terminated.ExitCode)
		}
		return status.Name, reason, terminated.Message
	}
	return "", "", ""
}

func (r *MCPServerReconciler) latestWarningEventForPod(ctx context.Context, namespace string, pod *corev1.Pod) *corev1.Event {
	// Events are readable only in managed workload namespaces. A cached List
	// would start a cluster-wide informer that cannot sync under that RBAC,
	// blocking the reconcile worker even for other servers.
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	events := &corev1.EventList{}
	if err := reader.List(ctx, events, client.InNamespace(namespace)); err != nil {
		return nil
	}
	var latest *corev1.Event
	for i := range events.Items {
		event := &events.Items[i]
		if event.Type != corev1.EventTypeWarning || !eventMatchesPod(event, pod) {
			continue
		}
		if latest == nil || eventTime(event).After(eventTime(latest)) {
			latest = event
		}
	}
	return latest
}

func eventMatchesPod(event *corev1.Event, pod *corev1.Pod) bool {
	if event.InvolvedObject.Kind != "Pod" {
		return false
	}
	if pod.UID != "" {
		return event.InvolvedObject.UID == pod.UID
	}
	return event.InvolvedObject.Name == pod.Name
}

func eventTime(event *corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time
	}
	return event.EventTime.Time
}

func safeDeploymentDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > maxDeploymentDiagnosticText {
		value = string(runes[:maxDeploymentDiagnosticText]) + "..."
	}
	return value
}
