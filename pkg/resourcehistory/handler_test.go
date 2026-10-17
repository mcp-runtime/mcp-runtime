package resourcehistory

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestScrapeRequiresExactProjectedIdentityAndAudience(t *testing.T) {
	for _, test := range []struct {
		name, username string
		audiences      []string
		authenticated  bool
		err            error
		want           int
	}{
		{"allowed", "system:serviceaccount:mcp-observability:prometheus", []string{ScrapeAudience}, true, nil, 200},
		{"foreign namespace", "system:serviceaccount:tenant:prometheus", []string{ScrapeAudience}, true, nil, 401},
		{"foreign serviceaccount", "system:serviceaccount:mcp-observability:default", []string{ScrapeAudience}, true, nil, 401},
		{"human token", "alice", []string{ScrapeAudience}, true, nil, 401},
		{"API audience", "system:serviceaccount:mcp-observability:prometheus", []string{"https://kubernetes.default.svc"}, true, nil, 401},
		{"revoked", "system:serviceaccount:mcp-observability:prometheus", []string{ScrapeAudience}, false, nil, 401},
		{"review unavailable", "", nil, false, errors.New("unavailable"), 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
				if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != ScrapeAudience || review.Spec.Token != "projected" {
					t.Fatal("unbounded token review")
				}
				return true, &authv1.TokenReview{Status: authv1.TokenReviewStatus{Authenticated: test.authenticated, Audiences: test.audiences, User: authv1.UserInfo{Username: test.username}}}, test.err
			})
			collected := false
			h := &Handler{Client: client, PrometheusNamespace: "mcp-observability", IsolatedTelemetry: true, collect: func(context.Context) *prometheus.Registry { collected = true; return prometheus.NewRegistry() }}
			r := httptest.NewRequest("GET", "/internal/observability/resources/metrics", nil)
			r.Header.Set("Authorization", "Bearer projected")
			r.Header.Set("X-WEBAUTH-ROLE", "Admin")
			r.Header.Set("X-API-Key", "admin")
			r.Header.Set("X-MCP-Namespace", "mcp-observability")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want || collected != (test.want == 200) {
				t.Fatalf("status=%d collected=%v", w.Code, collected)
			}
			if test.want == 200 {
				h.IsolatedTelemetry = false
				collected = false
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || collected || !strings.Contains(w.Body.String(), "mcp_resource_telemetry_isolation_ready 0") {
					t.Fatal("unenforced telemetry isolation collected privileged resource data")
				}
			}
		})
	}
}

func TestKubeletTrustRechecksUIDAddressAndTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	if _, err := PublicCertificates(append(append([]byte{}, cert...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("never-public")})...)); err == nil {
		t.Fatal("mixed private key bundle accepted")
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "current"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}}}
	target := Target{Name: node.Name, UID: string(node.UID), Address: "127.0.0.1", ServerName: "127.0.0.1", CertificatePEM: string(cert)}
	encoded, _ := EncodeTargets([]Target{target})
	targets, err := DecodeTargets(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !targets["node"].matches(node) {
		t.Fatal("current node rejected")
	}
	client := target.httpClient()
	defer client.CloseIdleConnections()
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, change := range []func(*Target){func(v *Target) { v.UID = "old" }, func(v *Target) { v.Address = "127.0.0.2" }} {
		forged := target
		change(&forged)
		if forged.matches(node) {
			t.Fatal("stale or redirected target accepted")
		}
	}
	wrong := target
	wrong.ServerName = "foreign.invalid"
	wrongClient := wrong.httpClient()
	defer wrongClient.CloseIdleConnections()
	if response, err := wrongClient.Get(upstream.URL); err == nil {
		response.Body.Close()
		t.Fatal("wrong TLS hostname accepted")
	}
	if _, err := DecodeTargets("[" + strings.Trim(encoded, "[]") + "," + strings.Trim(encoded, "[]") + "]"); err == nil {
		t.Fatal("duplicate target accepted")
	}
}

func registryText(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()
	w := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return w.Body.String()
}

func TestMeasuredZeroMissingFieldsAndPVCReservationsRemainDistinct(t *testing.T) {
	var data summary
	if err := json.Unmarshal([]byte(`{"node":{"nodeName":"node","cpu":{"usageNanoCores":0},"fs":{"usedBytes":123}},"pods":[{"podRef":{"name":"pod","namespace":"team","uid":"pod-new"},"containers":[{"name":"app","memory":{"workingSetBytes":42},"rootfs":{"usedBytes":7}}],"volume":[{"pvcRef":{"name":"data","namespace":"team"},"usedBytes":0}]}]}`), &data); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	exportSummary(registry, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "new"}}, data)
	client := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team", UID: "pvc-new"}, Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}}, Status: corev1.PersistentVolumeClaimStatus{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}})
	h := Handler{Client: client}
	if !h.exportMetadata(context.Background(), registry) {
		t.Fatal("metadata export failed")
	}
	text := registryText(t, registry)
	for _, want := range []string{`mcp_resource_node_cpu_cores{node="node",node_uid="new"} 0`, `mcp_resource_volume_used_bytes{namespace="team",node="node",persistentvolumeclaim="data"} 0`, `mcp_resource_pvc_requested_bytes`, `mcp_resource_pvc_capacity_bytes`, `mcp_resource_container_working_set_bytes`, `mcp_resource_container_rootfs_used_bytes`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "mcp_resource_node_working_set_bytes") || strings.Contains(text, "mcp_resource_volume_capacity_bytes") {
		t.Fatal("missing measurements synthesized from reservations")
	}
}

func TestMissingTrustNeverContactsKubelet(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: types.UID("new")}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}}}
	h := Handler{Client: fake.NewSimpleClientset(node), Namespace: "mcp-platform", readSummary: func(context.Context, Target) (summary, error) {
		t.Fatal("missing trust contacted kubelet")
		return summary{}, nil
	}}
	text := registryText(t, h.collectResources(context.Background()))
	if !strings.Contains(text, "mcp_resource_collection_success 0") || strings.Contains(text, "mcp_resource_node_cpu_cores") {
		t.Fatal(text)
	}
}

func TestEffectivePodRequests(t *testing.T) {
	resources := func(cpu string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}
	}
	always := corev1.ContainerRestartPolicyAlways
	pod := corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: resources("100m")}}, InitContainers: []corev1.Container{{RestartPolicy: &always, Resources: resources("200m")}, {Resources: resources("500m")}}, Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")}}}
	if got := PodRequest(pod, corev1.ResourceCPU); got != .75 {
		t.Fatalf("request=%v", got)
	}
	pod.Spec.Resources = &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}
	if got := PodRequest(pod, corev1.ResourceCPU); got != 2.05 {
		t.Fatalf("pod-level request=%v", got)
	}
}
