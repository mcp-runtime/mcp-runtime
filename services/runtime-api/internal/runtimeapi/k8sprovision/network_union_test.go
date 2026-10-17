package k8sprovision

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/pkg/kubeworkload"
)

// Interpret the additive ingress union, including destination named ports and
// combined namespace+pod selectors. Testing only the existence of a narrow
// policy missed the old broad namespace rule that overrode the boundary.
func ingressUnionAllows(t *testing.T, policies []networkingv1.NetworkPolicy, destination, source corev1.Pod, port int32) bool {
	t.Helper()
	matches := func(selector *metav1.LabelSelector, actual map[string]string) bool {
		compiled, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			t.Fatal(err)
		}
		return compiled.Matches(labels.Set(actual))
	}
	selected := false
	for _, policy := range policies {
		if policy.Namespace != destination.Namespace || !matches(&policy.Spec.PodSelector, destination.Labels) {
			continue
		}
		ingress := len(policy.Spec.PolicyTypes) == 0
		for _, kind := range policy.Spec.PolicyTypes {
			ingress = ingress || kind == networkingv1.PolicyTypeIngress
		}
		if !ingress {
			continue
		}
		selected = true
		for _, rule := range policy.Spec.Ingress {
			peerAllowed := len(rule.From) == 0
			for _, peer := range rule.From {
				if peer.IPBlock != nil {
					t.Fatal("this peer fixture does not model IPBlocks")
				}
				namespaceAllowed := source.Namespace == destination.Namespace
				if peer.NamespaceSelector != nil {
					namespaceAllowed = matches(peer.NamespaceSelector, map[string]string{"kubernetes.io/metadata.name": source.Namespace})
				}
				peerAllowed = peerAllowed || namespaceAllowed && (peer.PodSelector == nil || matches(peer.PodSelector, source.Labels))
			}
			if !peerAllowed {
				continue
			}
			if len(rule.Ports) == 0 {
				return true
			}
			for _, allowed := range rule.Ports {
				if allowed.Protocol != nil && *allowed.Protocol != corev1.ProtocolTCP {
					continue
				}
				if allowed.Port == nil || allowed.Port.Type == intstr.Int && allowed.Port.IntVal == port {
					return true
				}
				if allowed.Port.Type == intstr.String {
					for _, container := range destination.Spec.Containers {
						for _, declared := range container.Ports {
							if declared.Name == allowed.Port.StrVal && declared.ContainerPort == port && (declared.Protocol == "" || declared.Protocol == corev1.ProtocolTCP) {
								return true
							}
						}
					}
				}
			}
		}
	}
	return !selected
}

func TestMCPServerPolicyUnion(t *testing.T) {
	for _, mtls := range []bool{false, true} {
		mode := "gateway"
		if mtls {
			mode = "mtls"
		}
		t.Run(mode, func(t *testing.T) {
			target := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Labels: map[string]string{
				"app": "server", "app.kubernetes.io/managed-by": "mcp-runtime", kubeworkload.ServerUIDLabel: "current",
				// Even an unexpected generic marker cannot broaden a stamped pod.
				kubeworkload.PlatformManagedLabel: "true",
			}}, Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "server", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8088}}},
				{Name: "gateway", Ports: []corev1.ContainerPort{{Name: "gateway", ContainerPort: 8091}, {Name: "metrics", ContainerPort: 9103}}},
			}}}
			policies := []networkingv1.NetworkPolicy{*DefaultDenyNetworkPolicy("team", "traefik"), *GenericWorkloadIngressNetworkPolicy("team", "traefik"), {
				ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: kubeworkload.ServerIngressPolicy("team", "server", "current", true, mtls, "traefik", map[string]string{"app": "traefik"}),
			}}
			for _, tc := range []struct {
				name, namespace, app string
				port                 int32
				want                 bool
			}{
				{"same namespace raw", "team", "rogue", 8088, false},
				{"same namespace gateway", "team", "rogue", 8091, false},
				{"same namespace metrics", "team", "rogue", 9103, false},
				{"other tenant raw", "foreign", "rogue", 8088, false},
				{"other tenant gateway", "foreign", "rogue", 8091, false},
				{"foreign Traefik label", "foreign", "traefik", 8091, false},
				{"arbitrary ingress pod", "traefik", "rogue", 8091, false},
				{"exact ingress gateway", "traefik", "traefik", 8091, true},
				{"exact ingress raw", "traefik", "traefik", 8088, false},
				{"exact ingress metrics", "traefik", "traefik", 9103, false},
				{"Prometheus metrics", "mcp-observability", "prometheus", 9103, true},
				{"Prometheus raw", "mcp-observability", "prometheus", 8088, false},
				{"Prometheus gateway", "mcp-observability", "prometheus", 8091, false},
				{"arbitrary observability pod", "mcp-observability", "rogue", 9103, false},
				{"forged Prometheus namespace", "team", "prometheus", 9103, false},
				{"runtime inventory gateway", "mcp-platform", "mcp-runtime-api", 8091, !mtls},
				{"runtime inventory raw", "mcp-platform", "mcp-runtime-api", 8088, false},
				{"arbitrary platform pod", "mcp-platform", "rogue", 8091, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					source := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: tc.namespace, Labels: map[string]string{"app": tc.app}}}
					if got := ingressUnionAllows(t, policies, target, source, tc.port); got != tc.want {
						t.Fatalf("union permits traffic=%v want %v", got, tc.want)
					}
				})
			}
			// A retained candidate resolves its own named listener, not the new
			// revision's numeric port. The private port never becomes public.
			target.Spec.Containers[1].Ports[0].ContainerPort = 8101
			ingress := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "traefik", Labels: map[string]string{"app": "traefik"}}}
			if !ingressUnionAllows(t, policies, target, ingress, 8101) || ingressUnionAllows(t, policies, target, ingress, 8091) {
				t.Fatal("named listener did not follow the destination pod revision")
			}
			for _, old := range []string{"", "predecessor"} {
				legacy := target.DeepCopy()
				delete(legacy.Labels, kubeworkload.PlatformManagedLabel)
				if old == "" {
					delete(legacy.Labels, kubeworkload.ServerUIDLabel)
				} else {
					legacy.Labels[kubeworkload.ServerUIDLabel] = old
				}
				if ingressUnionAllows(t, policies, *legacy, ingress, 8088) || ingressUnionAllows(t, policies, *legacy, ingress, 8101) {
					t.Fatal("missing/predecessor UID pod inherited generic or current-server access")
				}
			}
		})
	}
}

func TestStandaloneAndGenericDeploymentPolicyUnion(t *testing.T) {
	policies := []networkingv1.NetworkPolicy{*DefaultDenyNetworkPolicy("team", "traefik"), *GenericWorkloadIngressNetworkPolicy("team", "traefik"), {
		ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: kubeworkload.ServerIngressPolicy("team", "server", "current", false, false, "traefik", map[string]string{"app": "traefik"}),
	}}
	standalone := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Labels: map[string]string{"app": "server", "app.kubernetes.io/managed-by": "mcp-runtime", kubeworkload.ServerUIDLabel: "current"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8088}}}}}}
	for _, source := range []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Namespace: "traefik", Labels: map[string]string{"app": "traefik"}}}, {ObjectMeta: metav1.ObjectMeta{Namespace: "mcp-platform", Labels: map[string]string{"app": "mcp-runtime-api"}}}} {
		if !ingressUnionAllows(t, policies, standalone, source, 8088) {
			t.Fatal("standalone public listener lost supported ingress")
		}
	}
	foreign := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "foreign"}}
	if ingressUnionAllows(t, policies, standalone, foreign, 8088) {
		t.Fatal("foreign standalone access allowed")
	}
	// The supported generic Deployment API uses unnamed arbitrary ports. Its
	// explicit marker preserves the old contract, while unmarked pods fail closed.
	generic := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Labels: map[string]string{kubeworkload.PlatformManagedLabel: "true"}}}
	sameNamespace := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}}
	if !ingressUnionAllows(t, policies, generic, sameNamespace, 12345) {
		t.Fatal("generic Deployment unnamed port lost ingress")
	}
	delete(generic.Labels, kubeworkload.PlatformManagedLabel)
	if ingressUnionAllows(t, policies, generic, sameNamespace, 12345) {
		t.Fatal("unmarked legacy pod inherited generic workload ingress")
	}
}

func TestEnsureReplacesOldBroadBasePolicy(t *testing.T) {
	old := DefaultDenyNetworkPolicy("team")
	old.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}}
	old.Labels = map[string]string{"operator-owned": "preserve"}
	old.Annotations = map[string]string{"note": "preserve"}
	client := fake.NewSimpleClientset(old)
	if err := EnsureDefaultDenyNetworkPolicy(context.Background(), client, "team", "traefik"); err != nil {
		t.Fatal(err)
	}
	updated, err := client.NetworkingV1().NetworkPolicies("team").Get(context.Background(), old.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Spec.Ingress) != 0 || len(updated.Spec.Egress) == 0 || updated.Labels["operator-owned"] != "preserve" || updated.Annotations["note"] != "preserve" {
		t.Fatalf("base drift not reconciled safely: %#v", updated)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("team").Get(context.Background(), "platform-generic-workload-ingress", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}
