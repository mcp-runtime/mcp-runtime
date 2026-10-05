package platformstack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Manager provides operations for platform stack components.
// Each component carries its own namespace from the inventory.
type Manager struct {
	clientset kubernetes.Interface
}

// NewManager creates a new platform component manager.
func NewManager(clientset kubernetes.Interface) *Manager {
	return &Manager{clientset: clientset}
}

// GetComponentStatus returns the status of a single component.
func (m *Manager) GetComponentStatus(ctx context.Context, key string) (ComponentStatus, error) {
	component, err := FindComponent(key)
	if err != nil {
		return ComponentStatus{}, err
	}

	return m.getWorkloadStatus(ctx, *component)
}

// GetAllComponentStatuses returns the status of all components.
func (m *Manager) GetAllComponentStatuses(ctx context.Context) ([]ComponentStatus, error) {
	statuses := make([]ComponentStatus, 0, len(Components))

	for _, component := range Components {
		status, err := m.getWorkloadStatus(ctx, component)
		if err != nil {
			status = ComponentStatus{
				Key:       component.Key,
				Display:   component.Display,
				Namespace: component.Namespace,
				Kind:      component.Kind,
				Resource:  component.Resource,
				Status:    "Error",
				Message:   err.Error(),
			}
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

// GetCoreComponentStatuses returns statuses for core runtime components only.
func (m *Manager) GetCoreComponentStatuses(ctx context.Context) ([]ComponentStatus, error) {
	statuses := make([]ComponentStatus, 0)

	for _, component := range Components {
		if !IsCoreComponent(component.Key) {
			continue
		}
		status, err := m.getWorkloadStatus(ctx, component)
		if err != nil {
			status = ComponentStatus{
				Key:       component.Key,
				Display:   component.Display,
				Namespace: component.Namespace,
				Kind:      component.Kind,
				Resource:  component.Resource,
				Status:    "Error",
				Message:   err.Error(),
			}
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

func (m *Manager) getWorkloadStatus(ctx context.Context, component Component) (ComponentStatus, error) {
	switch component.Kind {
	case "deployment":
		return m.getDeploymentStatus(ctx, component)
	case "statefulset":
		return m.getStatefulSetStatus(ctx, component)
	case "daemonset":
		return m.getDaemonSetStatus(ctx, component)
	default:
		return ComponentStatus{}, fmt.Errorf("unsupported kind: %s", component.Kind)
	}
}

func (m *Manager) getDeploymentStatus(ctx context.Context, component Component) (ComponentStatus, error) {
	deployment, err := m.clientset.AppsV1().Deployments(component.Namespace).Get(ctx, component.Resource, metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{}, fmt.Errorf("failed to get deployment %s: %w", component.Resource, err)
	}

	readyReplicas := deployment.Status.ReadyReplicas
	desiredReplicas := desiredReplicaCount(deployment.Spec.Replicas)

	message := ""
	if len(deployment.Status.Conditions) > 0 {
		for _, cond := range deployment.Status.Conditions {
			if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse {
				message = cond.Message
				break
			}
		}
	}

	return StatusFromWorkload(component, readyReplicas, desiredReplicas, message), nil
}

func (m *Manager) getStatefulSetStatus(ctx context.Context, component Component) (ComponentStatus, error) {
	sts, err := m.clientset.AppsV1().StatefulSets(component.Namespace).Get(ctx, component.Resource, metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{}, fmt.Errorf("failed to get statefulset %s: %w", component.Resource, err)
	}

	readyReplicas := sts.Status.ReadyReplicas
	desiredReplicas := desiredReplicaCount(sts.Spec.Replicas)

	message := ""
	if sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		message = "Update in progress"
	}

	return StatusFromWorkload(component, readyReplicas, desiredReplicas, message), nil
}

func (m *Manager) getDaemonSetStatus(ctx context.Context, component Component) (ComponentStatus, error) {
	ds, err := m.clientset.AppsV1().DaemonSets(component.Namespace).Get(ctx, component.Resource, metav1.GetOptions{})
	if err != nil {
		return ComponentStatus{}, fmt.Errorf("failed to get daemonset %s: %w", component.Resource, err)
	}

	readyReplicas := ds.Status.NumberReady
	desiredReplicas := ds.Status.DesiredNumberScheduled

	message := ""
	if ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
		message = "Update in progress"
	}

	return StatusFromWorkload(component, readyReplicas, desiredReplicas, message), nil
}

func desiredReplicaCount(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}

// RestartComponent performs a rolling restart of a component by patching its pod template.
func (m *Manager) RestartComponent(ctx context.Context, key string) error {
	component, err := FindComponent(key)
	if err != nil {
		return err
	}

	restartAnnotation := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"kubectl.kubernetes.io/restartedAt": metav1.Now().Format("2006-01-02T15:04:05Z"),
					},
				},
			},
		},
	}

	patchBytes, err := json.Marshal(restartAnnotation)
	if err != nil {
		return fmt.Errorf("failed to marshal restart patch: %w", err)
	}

	switch component.Kind {
	case "deployment":
		_, err = m.clientset.AppsV1().Deployments(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "statefulset":
		_, err = m.clientset.AppsV1().StatefulSets(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "daemonset":
		_, err = m.clientset.AppsV1().DaemonSets(component.Namespace).Patch(
			ctx, component.Resource, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	default:
		return fmt.Errorf("unsupported kind for restart: %s", component.Kind)
	}

	if err != nil {
		return fmt.Errorf("failed to restart %s: %w", component.Key, err)
	}

	return nil
}

// RestartAllComponents restarts all core components.
func (m *Manager) RestartAllComponents(ctx context.Context) []error {
	var errs []error
	for _, component := range Components {
		if !IsCoreComponent(component.Key) && !IsAnalyticsComponent(component.Key) {
			continue
		}
		if err := m.RestartComponent(ctx, component.Key); err != nil {
			errs = append(errs, fmt.Errorf("failed to restart %s: %w", component.Key, err))
		}
	}
	return errs
}

// GetComponentLogs returns logs for a component (for API streaming).
func (m *Manager) GetComponentLogs(ctx context.Context, key string, tailLines int64) ([]string, error) {
	component, err := FindComponent(key)
	if err != nil {
		return nil, err
	}

	pods, err := m.clientset.CoreV1().Pods(component.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", component.Label),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for %s: %w", component.Key, err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for %s", component.Key)
	}

	// Get logs from the first pod
	podName := pods.Items[0].Name
	logOptions := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}

	req := m.clientset.CoreV1().Pods(component.Namespace).GetLogs(podName, logOptions)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get logs for %s: %w", component.Key, err)
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read logs: %w", err)
	}

	logs := string(data)
	if strings.TrimSpace(logs) == "" {
		return []string{}, nil
	}
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	return lines, nil
}
