package platform

import (
	"context"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/setup/ingressmanifest"
)

func supportedCiliumFixtures() (*corev1.ConfigMap, *appsv1.DaemonSet) {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: "kube-system"}, Data: map[string]string{"enable-policy": "default", "enable-k8s-networkpolicy": "true"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system", Generation: 1}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "cilium-agent", Command: []string{"cilium-agent"}, Args: []string{"--enable-policy=default", "--enable-k8s-networkpolicy=true"}}}}}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}}
}

func TestScopedGrafanaRequiresPositiveSupportedPolicyCapability(t *testing.T) {
	for _, name := range []string{"unknown", "kindnet", "policy-never", "missing-k8s-policy", "agent-not-ready", "agent-old-revision", "argument-override", "argument-zero", "arguments-missing", "ready-cilium"} {
		t.Run(name, func(t *testing.T) {
			config, agents := supportedCiliumFixtures()
			var objects []runtime.Object
			switch name {
			case "unknown":
			case "kindnet":
				objects = append(objects, config, agents, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "kindnet", Namespace: "kube-system"}, Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1}})
			default:
				switch name {
				case "policy-never":
					config.Data["enable-policy"] = "never"
				case "missing-k8s-policy":
					delete(config.Data, "enable-k8s-networkpolicy")
				case "agent-not-ready":
					agents.Status.NumberReady = 0
				case "agent-old-revision":
					agents.Status.UpdatedNumberScheduled = 0
				case "argument-override":
					agents.Spec.Template.Spec.Containers[0].Args = []string{"--enable-policy=never"}
				case "argument-zero":
					agents.Spec.Template.Spec.Containers[0].Args = []string{"--enable-policy=default", "--enable-k8s-networkpolicy=0"}
				case "arguments-missing":
					agents.Spec.Template.Spec.Containers[0].Args = nil
				}
				objects = append(objects, config, agents)
			}
			cs := fake.NewSimpleClientset(objects...)
			supported, reason := ciliumPolicyCapability(context.Background(), cs)
			if supported != (name == "ready-cilium") || reason == "" {
				t.Fatalf("supported=%v reason=%q", supported, reason)
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("capability verification mutated the cluster: %v", action)
				}
			}
		})
	}
}

func TestGrafanaRenderingKeepsUnknownCNISafeAndAdminAccessible(t *testing.T) {
	config, agents := supportedCiliumFixtures()
	for _, supported := range []bool{false, true} {
		var objects []runtime.Object
		if supported {
			policy, err := scopedTelemetryPolicySource()
			if err != nil {
				t.Fatal(err)
			}
			objects = []runtime.Object{config, agents, policy}
		}
		clients := newPlatformKubernetesTestClients(objects, nil)
		clients.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}:            "CiliumNetworkPolicyList",
			{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}: "CiliumClusterwideNetworkPolicyList",
		})
		swapKubernetesClientsForTest(t, clients)
		raw, err := os.ReadFile("../../../../k8s/12-grafana.yaml")
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := renderAnalyticsManifest(string(raw), AnalyticsImageSet{}, "", "org")
		if err != nil {
			t.Fatal(err)
		}
		var deployment appsv1.Deployment
		for _, doc := range strings.Split(rendered, "\n---\n") {
			if strings.Contains(doc, "kind: Deployment") {
				if err := yaml.Unmarshal([]byte(doc), &deployment); err != nil {
					t.Fatal(err)
				}
			}
		}
		found := false
		for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "GF_AUTH_PROXY_ENABLED" {
				found = true
				if (env.Value == "true") != supported {
					t.Fatalf("spoofable auth proxy with unsupported CNI: %q supported=%v", env.Value, supported)
				}
			}
		}
		if !found {
			t.Fatal("auth proxy setting missing")
		}
		ingress := ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", false, "mcp-platform", "mcp-observability")
		ingress, err = renderGrafanaIngressAccess(ingress, supported)
		if err != nil {
			t.Fatal(err)
		}
		if supported && strings.Contains(ingress, "name: grafana") || !supported && (!strings.Contains(ingress, "name: grafana") || !strings.Contains(ingress, "platform-admin-auth@file")) {
			t.Fatalf("incorrect Grafana ingress for supported=%v: %s", supported, ingress)
		}
	}
}

func TestUnknownCNISetupDoesNotEnableTenantGrafana(t *testing.T) {
	clients := newPlatformKubernetesTestClients([]runtime.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: "mcp-platform"}, Data: map[string]string{"GRAFANA_SCOPED_USER_ACCESS": "false"}}}, nil)
	swapKubernetesClientsForTest(t, clients)
	if err := enableScopedGrafanaClientGo(); err != nil {
		t.Fatal(err)
	}
	for _, action := range clients.Clientset.(*fake.Clientset).Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("unknown CNI enabled or mutated Grafana: %v", action)
		}
	}
}

func TestScopedGrafanaRejectsAdditiveTelemetryAllowPolicies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector metav1.LabelSelector
		allowed  bool
	}{
		{"broad-allow", metav1.LabelSelector{}, false},
		{"grafana-allow", metav1.LabelSelector{MatchLabels: map[string]string{"app": "grafana"}}, false},
		{"admission-label-allow", metav1.LabelSelector{MatchLabels: map[string]string{"environment": "prod"}}, false},
		{"unrelated-kafka", metav1.LabelSelector{MatchLabels: map[string]string{"app": "kafka"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: "mcp-observability"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: tc.selector, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}}
			cs := fake.NewSimpleClientset(policy)
			err := rejectAdditionalTelemetryPolicies(context.Background(), cs)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v result=%v", tc.allowed, err)
			}
		})
	}
}

func TestScopedGrafanaRefusesNativeCiliumPolicies(t *testing.T) {
	config, agents := supportedCiliumFixtures()
	clients := newPlatformKubernetesTestClients([]runtime.Object{config, agents}, nil)
	clients.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}:            "CiliumNetworkPolicyList",
		{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}: "CiliumClusterwideNetworkPolicyList",
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"metadata": map[string]any{"name": "unreviewed-allow", "namespace": "mcp-observability"},
	}})
	swapKubernetesClientsForTest(t, clients)
	if supported, reason := scopedGrafanaPolicyCapability(); supported || !strings.Contains(reason, "native Cilium policies") {
		t.Fatalf("unreviewed native policy accepted: %v %s", supported, reason)
	}
}

func TestScopedGrafanaRefusesMissingOrWidenedOwnPolicy(t *testing.T) {
	for _, missing := range []bool{false, true} {
		policy, err := scopedTelemetryPolicySource()
		if err != nil {
			t.Fatal(err)
		}
		var objects []runtime.Object
		if !missing {
			policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
			objects = append(objects, policy)
		}
		swapKubernetesClientsForTest(t, newPlatformKubernetesTestClients(objects, nil))
		if err := verifyScopedTelemetryPolicy(); err == nil {
			t.Fatalf("missing=%v telemetry policy accepted before auth proxy", missing)
		}
	}
}
