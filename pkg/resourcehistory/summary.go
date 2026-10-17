package resourcehistory

import (
	"math"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
)

// Pointers preserve the difference between a measured zero and a missing field.
// Unknown Summary API fields (including pod content) are never exported.
type cpuStats struct {
	UsageNanoCores       *float64 `json:"usageNanoCores"`
	UsageCoreNanoSeconds *float64 `json:"usageCoreNanoSeconds"`
}
type memoryStats struct {
	WorkingSetBytes *float64 `json:"workingSetBytes"`
}
type fsStats struct {
	UsedBytes      *float64 `json:"usedBytes"`
	CapacityBytes  *float64 `json:"capacityBytes"`
	AvailableBytes *float64 `json:"availableBytes"`
}
type networkStats struct {
	RxBytes *float64 `json:"rxBytes"`
	TxBytes *float64 `json:"txBytes"`
}
type usageStats struct {
	CPU     *cpuStats     `json:"cpu"`
	Memory  *memoryStats  `json:"memory"`
	Network *networkStats `json:"network"`
}
type summary struct {
	Node struct {
		usageStats
		NodeName string   `json:"nodeName"`
		FS       *fsStats `json:"fs"`
	} `json:"node"`
	Pods []struct {
		PodRef struct{ Name, Namespace, UID string } `json:"podRef"`
		usageStats
		Containers []struct {
			Name string `json:"name"`
			usageStats
			RootFS *fsStats `json:"rootfs"`
			Logs   *fsStats `json:"logs"`
		} `json:"containers"`
		Volumes []struct {
			fsStats
			PVCRef *struct{ Name, Namespace string } `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

func metric(registry *prometheus.Registry, name, help string, value float64, labels map[string]string) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	if strings.HasSuffix(name, "_total") {
		counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "mcp_resource_" + name, Help: help, ConstLabels: labels})
		if registry.Register(counter) == nil {
			counter.Add(value)
		}
		return
	}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "mcp_resource_" + name, Help: help, ConstLabels: labels})
	if registry.Register(gauge) == nil {
		gauge.Set(value)
	}
}
func observed(registry *prometheus.Registry, name, help string, value *float64, divisor float64, labels map[string]string) {
	if value != nil {
		metric(registry, name, help, *value/divisor, labels)
	}
}
func usage(registry *prometheus.Registry, prefix string, stats usageStats, labels map[string]string) {
	if stats.CPU != nil {
		observed(registry, prefix+"cpu_cores", "Kubelet sampled CPU usage in cores; not a reservation.", stats.CPU.UsageNanoCores, 1e9, labels)
		observed(registry, prefix+"cpu_seconds_total", "Kubelet cumulative CPU core seconds.", stats.CPU.UsageCoreNanoSeconds, 1e9, labels)
	}
	if stats.Memory != nil {
		observed(registry, prefix+"working_set_bytes", "Kubelet working memory bytes.", stats.Memory.WorkingSetBytes, 1, labels)
	}
	if stats.Network != nil {
		observed(registry, prefix+"network_receive_bytes_total", "Kubelet cumulative primary-interface received bytes.", stats.Network.RxBytes, 1, labels)
		observed(registry, prefix+"network_transmit_bytes_total", "Kubelet cumulative primary-interface transmitted bytes.", stats.Network.TxBytes, 1, labels)
	}
}
func filesystem(registry *prometheus.Registry, prefix string, stats *fsStats, labels map[string]string) {
	if stats == nil {
		return
	}
	observed(registry, prefix+"used_bytes", "Kubelet measured filesystem consumed bytes; not PVC reservations.", stats.UsedBytes, 1, labels)
	observed(registry, prefix+"capacity_bytes", "Kubelet filesystem capacity bytes; not consumed bytes.", stats.CapacityBytes, 1, labels)
	observed(registry, prefix+"available_bytes", "Kubelet filesystem available bytes.", stats.AvailableBytes, 1, labels)
}
func exportSummary(registry *prometheus.Registry, node corev1.Node, data summary) {
	labels := map[string]string{"node": node.Name, "node_uid": string(node.UID)}
	usage(registry, "node_", data.Node.usageStats, labels)
	filesystem(registry, "node_filesystem_", data.Node.FS, labels)
	volumes := map[string]*fsStats{}
	for _, pod := range data.Pods {
		if pod.PodRef.UID == "" || pod.PodRef.Namespace == "" || pod.PodRef.Name == "" {
			continue
		}
		podLabels := map[string]string{"node": node.Name, "namespace": pod.PodRef.Namespace, "pod": pod.PodRef.Name, "pod_uid": pod.PodRef.UID}
		usage(registry, "pod_", pod.usageStats, podLabels)
		for _, container := range pod.Containers {
			if container.Name == "" {
				continue
			}
			containerLabels := map[string]string{"node": node.Name, "namespace": pod.PodRef.Namespace, "pod": pod.PodRef.Name, "pod_uid": pod.PodRef.UID, "container": container.Name}
			usage(registry, "container_", container.usageStats, containerLabels)
			filesystem(registry, "container_rootfs_", container.RootFS, containerLabels)
			filesystem(registry, "container_logs_", container.Logs, containerLabels)
		}
		for _, volume := range pod.Volumes {
			ref := volume.PVCRef
			if ref == nil || ref.Namespace != pod.PodRef.Namespace || ref.Name == "" {
				continue
			}
			key := ref.Namespace + "/" + ref.Name
			previous := volumes[key]
			if previous == nil {
				previous = &fsStats{}
				volumes[key] = previous
			}
			maximum := func(before, next *float64) *float64 {
				if next != nil && (before == nil || *next > *before) {
					return next
				}
				return before
			}
			previous.UsedBytes = maximum(previous.UsedBytes, volume.UsedBytes)
			previous.CapacityBytes = maximum(previous.CapacityBytes, volume.CapacityBytes)
			previous.AvailableBytes = maximum(previous.AvailableBytes, volume.AvailableBytes)
		}
	}
	for key, stats := range volumes {
		namespace, name, _ := strings.Cut(key, "/")
		filesystem(registry, "volume_", stats, map[string]string{"node": node.Name, "namespace": namespace, "persistentvolumeclaim": name})
	}
}

// PodRequest follows scheduler accounting for restartable init sidecars,
// ordinary init containers, pod-level resources and pod overhead.
func PodRequest(pod corev1.Pod, resource corev1.ResourceName) float64 {
	request := func(c corev1.Container) float64 { q := c.Resources.Requests[resource]; return q.AsApproximateFloat64() }
	var sidecars, initPeak, regular float64
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += request(c)
			initPeak = math.Max(initPeak, sidecars)
		} else {
			initPeak = math.Max(initPeak, sidecars+request(c))
		}
	}
	for _, c := range pod.Spec.Containers {
		regular += request(c)
	}
	value := math.Max(regular+sidecars, initPeak)
	if pod.Spec.Resources != nil {
		if q, present := pod.Spec.Resources.Requests[resource]; present {
			value = q.AsApproximateFloat64()
		}
	}
	if q, present := pod.Spec.Overhead[resource]; present {
		value += q.AsApproximateFloat64()
	}
	return value
}
