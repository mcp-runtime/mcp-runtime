package kubeworkload

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	ServerUIDLabel       = "mcpruntime.org/server-uid"
	PlatformManagedLabel = "platform.mcpruntime.org/managed"
)

// ServerIngressPolicy describes the actual public listener, never the raw
// upstream behind a gateway. Named ports keep retained/candidate pod revisions
// correct during a serving-port change. The namespace's generic workload policy
// must exclude MCPServer pods: NetworkPolicy allows are additive.
func ServerIngressPolicy(namespace, name, uid string, gateway, mtls bool, ingressNamespace string, ingressLabels map[string]string) networkingv1.NetworkPolicySpec {
	tcp := corev1.ProtocolTCP
	port := func(name string) []networkingv1.NetworkPolicyPort {
		value := intstr.FromString(name)
		return []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &value}}
	}
	peer := func(namespace string, labels map[string]string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: maps.Clone(labels)},
		}
	}
	var clients []networkingv1.NetworkPolicyPeer
	if ingressNamespace != "" && len(ingressLabels) != 0 {
		clients = append(clients, peer(ingressNamespace, ingressLabels))
	}
	if !mtls {
		// The read-only live inventory probe uses the ordinary public listener.
		// It receives no bypass of OAuth, grants, or verified ingress assertions.
		clients = append(clients, peer("mcp-platform", map[string]string{"app": "mcp-runtime-api"}))
	}
	listener := "gateway"
	if !gateway {
		listener = "http"
		clients = append(clients, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}},
			PodSelector:       &metav1.LabelSelector{},
		})
	}
	spec := networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
			"app": name, "app.kubernetes.io/managed-by": "mcp-runtime", ServerUIDLabel: uid,
		}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
	}
	if len(clients) != 0 {
		spec.Ingress = append(spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: clients, Ports: port(listener)})
	}
	if gateway {
		spec.Ingress = append(spec.Ingress, networkingv1.NetworkPolicyIngressRule{
			From:  []networkingv1.NetworkPolicyPeer{peer("mcp-observability", map[string]string{"app": "prometheus"})},
			Ports: port("metrics"),
		})
	}
	if uid == "" {
		// A real API object has a UID. An incomplete caller must never grant
		// ingress to an unlabelled legacy pod by falling back to its name.
		spec.Ingress = nil
	}
	return spec
}
