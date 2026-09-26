package platform

import "testing"

func TestValidateAdapterCertificateIngressIdentity(t *testing.T) {
	previous := detectIngressControllerIdentity
	t.Cleanup(func() { detectIngressControllerIdentity = previous })

	t.Run("disabled feature does not require ingress discovery", func(t *testing.T) {
		t.Setenv("MCP_ADAPTER_CERTIFICATES", "false")
		detectIngressControllerIdentity = func() ingressControllerIdentity { return ingressControllerIdentity{} }
		if err := validateAdapterCertificateIngressIdentity(); err != nil {
			t.Fatalf("disabled feature error = %v, want nil", err)
		}
	})

	t.Run("enabled feature rejects partial ingress identity", func(t *testing.T) {
		t.Setenv("MCP_ADAPTER_CERTIFICATES", "true")
		detectIngressControllerIdentity = func() ingressControllerIdentity {
			return ingressControllerIdentity{Namespace: "kube-system"}
		}
		if err := validateAdapterCertificateIngressIdentity(); err == nil {
			t.Fatal("expected setup to fail when Traefik identity discovery is incomplete")
		}
	})

	t.Run("enabled feature accepts complete ingress identity", func(t *testing.T) {
		t.Setenv("MCP_ADAPTER_CERTIFICATES", "1")
		detectIngressControllerIdentity = func() ingressControllerIdentity {
			return ingressControllerIdentity{
				Namespace:      "kube-system",
				ServiceAccount: "traefik",
				PodLabels:      map[string]string{"app.kubernetes.io/name": "traefik"},
			}
		}
		if err := validateAdapterCertificateIngressIdentity(); err != nil {
			t.Fatalf("complete ingress identity error = %v, want nil", err)
		}
	})
}
