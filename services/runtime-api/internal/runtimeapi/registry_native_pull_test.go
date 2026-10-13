package runtimeapi

import (
	"context"
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/kubeworkload"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativePullCredentialDoesNotCopyServiceKey(t *testing.T) {
	for _, status := range []int{201, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("x-api-key") != "admin-test-value" {
					t.Error("missing service credential")
				}
				w.WriteHeader(status)
				if status == 201 {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "rp_test", "username": "mcp-pull-mcp-team-acme", "password": "mcpp_test-value", "expires_at": time.Now().Add(90 * 24 * time.Hour)})
				}
			}))
			defer broker.Close()
			t.Setenv("MCP_REGISTRY_NATIVE_AUTH", "true")
			t.Setenv("PLATFORM_API_URL", broker.URL)
			t.Setenv("ADMIN_API_KEYS", "admin-test-value")
			cs := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: kubeworkload.DefaultServiceAccountName, Namespace: "mcp-team-acme"}})
			service := &DeploymentService{}
			err := service.ensureNamespaceRegistryPullSecret(context.Background(), cs, "mcp-team-acme")
			secret, getErr := cs.CoreV1().Secrets("mcp-team-acme").Get(context.Background(), registryPullSecretName, metav1.GetOptions{})
			if status != 201 {
				if err == nil || getErr == nil {
					t.Fatal("failed broker created credential")
				}
				return
			}
			if err != nil || getErr != nil {
				t.Fatalf("provision: %v %v", err, getErr)
			}
			config := string(secret.Data[corev1.DockerConfigJsonKey])
			if strings.Contains(config, "admin-test-value") || !strings.Contains(config, "mcpp_test-value") || !strings.Contains(config, "registry.registry.svc:5000") {
				t.Fatal("invalid node credential")
			}
			if err := service.ensureNamespaceRegistryPullSecret(context.Background(), cs, "mcp-team-acme"); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("unexpired credential reissued")
			}
		})
	}
}

// The broker request must ask for the shared platform repositories so the
// namespace can pull the gateway sidecar and the doctor smoke image.
func TestNativePullCredentialRequestsSharedRepositories(t *testing.T) {
	var scope struct {
		Namespace    string   `json:"namespace"`
		Repositories []string `json:"repositories"`
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&scope)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "rp_test", "username": "mcp-pull-mcp-team-acme", "password": "mcpp_test-value", "expires_at": time.Now().Add(90 * 24 * time.Hour)})
	}))
	defer broker.Close()
	t.Setenv("MCP_REGISTRY_NATIVE_AUTH", "true")
	t.Setenv("PLATFORM_API_URL", broker.URL)
	t.Setenv("ADMIN_API_KEYS", "admin-test-value")
	cs := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: kubeworkload.DefaultServiceAccountName, Namespace: "mcp-team-acme"}})
	if err := (&DeploymentService{}).ensureNamespaceRegistryPullSecret(context.Background(), cs, "mcp-team-acme"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(scope.Repositories, ",")
	if scope.Namespace != "mcp-team-acme" || !strings.Contains(got, "mcp-gateway") || !strings.Contains(got, "mcp-runtime-doctor-smoke") {
		t.Fatalf("scope = %+v, want namespace and both shared repositories", scope)
	}
}
