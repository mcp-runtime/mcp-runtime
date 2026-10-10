package platform

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"mcp-runtime/internal/cli/core"
)

// The operator writes the per-server Traefik egress NetworkPolicy into the
// live Traefik namespace, so setup must pass that identity even when adapter
// certificates are disabled (#618).
func TestOperatorEnvPassesIngressIdentityWithoutAdapterCertificates(t *testing.T) {
	orig := core.DefaultCLIConfig
	t.Cleanup(func() { core.DefaultCLIConfig = orig })
	core.DefaultCLIConfig = &core.CLIConfig{}
	t.Setenv("MCP_ADAPTER_CERTIFICATES", "false")
	previous := detectIngressControllerIdentity
	t.Cleanup(func() { detectIngressControllerIdentity = previous })
	detectIngressControllerIdentity = func() ingressControllerIdentity {
		return ingressControllerIdentity{
			Namespace:      "kube-system",
			ServiceAccount: "traefik",
			PodLabels:      map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"},
		}
	}
	got := operatorEnvOverrides("", "")
	requireOperatorEnvVar(t, got, "MCP_INGRESS_CONTROLLER_NAMESPACE", "kube-system")
	requireOperatorEnvVar(t, got, "MCP_INGRESS_CONTROLLER_SERVICE_ACCOUNT", "traefik")
	requireOperatorEnvVar(t, got, "MCP_INGRESS_CONTROLLER_POD_LABELS", "app.kubernetes.io/instance=traefik-kube-system,app.kubernetes.io/name=traefik")
}

// On k3s the only Traefik Deployment is the bundled one in kube-system; setup
// must resolve it, with its Helm selector labels, without PLATFORM_TRAEFIK_NAMESPACE.
func TestDetectIngressControllerIdentityResolvesK3sKubeSystem(t *testing.T) {
	resetPlatformKubeconfig(t)
	platformSetupKubeconfig = ""
	t.Setenv("PLATFORM_TRAEFIK_NAMESPACE", "")
	labels := map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"}
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients([]runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "kube-system"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "traefik"}},
			},
		},
	}, nil))

	identity := detectIngressControllerIdentityClientGo()
	if identity.Namespace != "kube-system" || identity.ServiceAccount != "traefik" || len(identity.PodLabels) != 2 || identity.PodLabels["app.kubernetes.io/instance"] != "traefik-kube-system" {
		t.Fatalf("identity = %+v, want kube-system Traefik with Helm labels", identity)
	}
}

func TestDetectIngressControllerIdentityHonorsPlatformTraefikNamespace(t *testing.T) {
	resetPlatformKubeconfig(t)
	platformSetupKubeconfig = ""
	t.Setenv("PLATFORM_TRAEFIK_NAMESPACE", "kube-system")
	swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients([]runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "kube-system"},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "traefik"}}},
		},
	}, nil))
	if identity := detectIngressControllerIdentityClientGo(); identity.Namespace != "kube-system" {
		t.Fatalf("namespace = %q, want PLATFORM_TRAEFIK_NAMESPACE kube-system", identity.Namespace)
	}
}

func TestPreferredTraefikNamespace(t *testing.T) {
	for _, tc := range []struct {
		namespaces []string
		want       string
	}{
		{nil, ""},
		{[]string{"kube-system"}, "kube-system"},
		{[]string{"ingress", "kube-system"}, "kube-system"},
		{[]string{"kube-system", "traefik"}, "traefik"},
		{[]string{"ingress"}, "ingress"},
	} {
		if got := preferredTraefikNamespace(tc.namespaces); got != tc.want {
			t.Fatalf("preferredTraefikNamespace(%v) = %q, want %q", tc.namespaces, got, tc.want)
		}
	}
}

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
