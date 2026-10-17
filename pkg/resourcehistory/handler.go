package resourcehistory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Handler authenticates each scrape before doing any collection. It accepts
// only a bound token for the install's Prometheus identity and dedicated
// audience. Platform user/admin keys and caller identity headers do not apply.
type Handler struct {
	Client              kubernetes.Interface
	Config              *rest.Config
	Namespace           string
	PrometheusNamespace string
	// Set only after supported setup verifies enforcing CNI and shared
	// telemetry ingress isolation. Scrape auth alone cannot protect data
	// subsequently stored in shared Prometheus.
	IsolatedTelemetry bool
	mu                sync.Mutex
	collect           func(context.Context) *prometheus.Registry // test seam; never caller-supplied
	readSummary       func(context.Context, Target) (summary, error)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	credential, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || credential == "" || len(credential) > 16<<10 || h.Client == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	reviewCtx, reviewCancel := context.WithTimeout(ctx, 2*time.Second)
	review, err := h.Client.AuthenticationV1().TokenReviews().Create(reviewCtx, &authv1.TokenReview{Spec: authv1.TokenReviewSpec{Token: credential, Audiences: []string{ScrapeAudience}}}, metav1.CreateOptions{})
	reviewCancel()
	if err != nil || !review.Status.Authenticated || review.Status.Error != "" || !slices.Contains(review.Status.Audiences, ScrapeAudience) || review.Status.User.Username != "system:serviceaccount:"+h.PrometheusNamespace+":prometheus" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// A concurrent scrape is rejected, not queued behind node I/O indefinitely.
	if !h.IsolatedTelemetry {
		registry := prometheus.NewRegistry()
		metric(registry, "collection_success", "One only when all requested sources were sampled successfully.", 0, nil)
		metric(registry, "telemetry_isolation_ready", "One only after supported setup enables enforced shared telemetry isolation.", 0, nil)
		promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
		return
	}
	if !h.mu.TryLock() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer h.mu.Unlock()
	collect := h.collect
	if collect == nil {
		collect = h.collectResources
	}
	registry := collect(ctx)
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (h *Handler) collectResources(ctx context.Context) *prometheus.Registry {
	started := time.Now()
	registry := prometheus.NewRegistry()
	defer func() {
		metric(registry, "collection_duration_seconds", "Wall time spent collecting this bounded resource sample.", time.Since(started).Seconds(), nil)
	}()
	cm, err := h.Client.CoreV1().ConfigMaps(h.Namespace).Get(ctx, TrustConfigMap, metav1.GetOptions{})
	if err != nil {
		metric(registry, "collection_success", "One only when all requested sources were sampled successfully.", 0, nil)
		return registry
	}
	targets, err := DecodeTargets(cm.Data["targets.json"])
	if err != nil {
		metric(registry, "collection_success", "One only when all requested sources were sampled successfully.", 0, nil)
		return registry
	}
	nodes, err := h.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 65})
	if err != nil || len(nodes.Items) > 64 || nodes.Continue != "" {
		metric(registry, "collection_success", "One only when all requested sources were sampled successfully.", 0, nil)
		return registry
	}
	success := len(nodes.Items) > 0
	// A worker pool bounds node requests and total work. Each node request has
	// a three-second TLS/request deadline inside the overall scrape deadline.
	type result struct {
		node corev1.Node
		data summary
		err  error
	}
	results := make(chan result, len(nodes.Items))
	jobs := make(chan corev1.Node)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(nodes.Items)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for node := range jobs {
				target, present := targets[node.Name]
				if !present || !target.matches(node) {
					results <- result{node: node, err: fmt.Errorf("trust unavailable")}
					continue
				}
				read := h.readSummary
				if read == nil {
					read = h.fetchSummary
				}
				data, err := read(ctx, target)
				if err == nil && data.Node.NodeName != node.Name {
					err = fmt.Errorf("summary identity differs")
				}
				results <- result{node: node, data: data, err: err}
			}
		}()
	}
	go func() {
		for _, node := range nodes.Items {
			jobs <- node
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	for result := range results {
		value := 1.0
		if result.err != nil {
			value = 0
			success = false
		} else {
			exportSummary(registry, result.node, result.data)
		}
		metric(registry, "node_collection_success", "One only when the current node was authenticated and sampled.", value, map[string]string{"node": result.node.Name, "node_uid": string(result.node.UID)})
	}
	if !h.exportMetadata(ctx, registry) {
		success = false
	}
	value := 0.0
	if success {
		value = 1
	}
	metric(registry, "collection_success", "One only when all requested sources were sampled successfully.", value, nil)
	return registry
}

func (h *Handler) fetchSummary(ctx context.Context, target Target) (summary, error) {
	var data summary
	if h.Config == nil {
		return data, fmt.Errorf("kubernetes credential unavailable")
	}
	token := h.Config.BearerToken
	if h.Config.BearerTokenFile != "" {
		raw, err := os.ReadFile(h.Config.BearerTokenFile)
		if err != nil {
			return data, fmt.Errorf("kubernetes credential unavailable")
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return data, fmt.Errorf("kubernetes credential unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(target.Address, "10250")+"/stats/summary", nil)
	if err != nil {
		return data, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := target.httpClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return data, fmt.Errorf("kubelet request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return data, fmt.Errorf("kubelet returned status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 || json.Unmarshal(raw, &data) != nil {
		return data, fmt.Errorf("invalid kubelet summary")
	}
	if len(data.Pods) > 5000 {
		return summary{}, fmt.Errorf("too many kubelet pods")
	}
	return data, nil
}

func (h *Handler) exportMetadata(ctx context.Context, registry *prometheus.Registry) bool {
	pods, err := h.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 5000})
	if err != nil || pods.Continue != "" || len(pods.Items) > 5000 {
		return false
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		labels := map[string]string{"namespace": pod.Namespace, "pod": pod.Name, "pod_uid": string(pod.UID), "node": pod.Spec.NodeName}
		metric(registry, "pod_cpu_requests_cores", "Desired PodSpec CPU budget, including init sidecars and overhead; not usage or resize allocation.", PodRequest(pod, corev1.ResourceCPU), labels)
		metric(registry, "pod_memory_requests_bytes", "Desired PodSpec memory budget, including init sidecars and overhead; not usage or resize allocation.", PodRequest(pod, corev1.ResourceMemory), labels)
		for _, status := range pod.Status.ContainerStatuses {
			metric(registry, "container_restarts_total", "Kubernetes container restart count.", float64(status.RestartCount), map[string]string{"namespace": pod.Namespace, "pod": pod.Name, "pod_uid": string(pod.UID), "container": status.Name})
		}
	}
	claims, err := h.Client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{Limit: 5000})
	if err != nil || claims.Continue != "" || len(claims.Items) > 5000 {
		return false
	}
	for _, claim := range claims.Items {
		labels := map[string]string{"namespace": claim.Namespace, "persistentvolumeclaim": claim.Name, "pvc_uid": string(claim.UID)}
		if q, present := claim.Spec.Resources.Requests[corev1.ResourceStorage]; present {
			metric(registry, "pvc_requested_bytes", "PVC requested storage reservation; not consumed bytes.", q.AsApproximateFloat64(), labels)
		}
		if q, present := claim.Status.Capacity[corev1.ResourceStorage]; present {
			metric(registry, "pvc_capacity_bytes", "PVC provisioned capacity; not consumed bytes.", q.AsApproximateFloat64(), labels)
		}
	}
	return true
}
