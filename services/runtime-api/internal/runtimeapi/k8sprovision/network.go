package k8sprovision

import (
	"context"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/pkg/egresscidrs"
	"mcp-runtime/pkg/platformstack"
)

const (
	platformIngestPort = 8081
	platformOTLPPort   = 4318
	RegistryNamespace  = "registry"
	RegistryPort       = 5000
)

func EnsureDefaultDenyNetworkPolicy(ctx context.Context, client kubernetes.Interface, ns string, ingressFromNamespaces ...string) error {
	policy := DefaultDenyNetworkPolicy(ns, ingressFromNamespaces...)
	if err := applyConfiguredPodEgress(policy); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.NetworkingV1().NetworkPolicies(ns).Get(ctx, policy.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = client.NetworkingV1().NetworkPolicies(ns).Create(ctx, policy, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		updated := policy.DeepCopy()
		updated.ResourceVersion = current.ResourceVersion
		updated.Labels = current.Labels
		updated.Annotations = current.Annotations
		_, err = client.NetworkingV1().NetworkPolicies(ns).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

func DefaultDenyNetworkPolicy(ns string, ingressFromNamespaces ...string) *networkingv1.NetworkPolicy {
	udpProtocol := corev1.ProtocolUDP
	tcpProtocol := corev1.ProtocolTCP
	ingress := make([]networkingv1.NetworkPolicyIngressRule, 0, 2)
	ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{
			{PodSelector: &metav1.LabelSelector{}},
		},
	})
	for _, namespace := range ingressFromNamespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
					},
				},
			},
		})
	}
	for _, namespace := range []string{platformstack.PlatformNamespace, platformstack.ObservabilityNamespace} {
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
					},
				},
			},
		})
	}
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-default-deny", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     ingress,
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": platformstack.PlatformNamespace}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "mcp-platform-api"}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcpProtocol, Port: intstrPtr(8085)}},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"k8s-app": "kube-dns"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProtocol, Port: intstrPtr(53)},
						{Protocol: &tcpProtocol, Port: intstrPtr(53)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": platformstack.ObservabilityNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: intstrPtr(platformIngestPort)},
						{Protocol: &tcpProtocol, Port: intstrPtr(platformOTLPPort)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": RegistryNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: intstrPtr(RegistryPort)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
			},
		},
	}
	// OAuth resource servers discover the public issuer through the platform's
	// ingress controller. Permit HTTPS to that controller, not general Internet
	// access. Include the Service and target ports for CNI implementations that
	// enforce policy before or after Service destination translation.
	for _, ingressNamespace := range ingressFromNamespaces {
		ingressNamespace = strings.TrimSpace(ingressNamespace)
		if ingressNamespace == "" {
			continue
		}
		peers := make([]networkingv1.NetworkPolicyPeer, 0, 2)
		for _, label := range []string{"app.kubernetes.io/name", "app"} {
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ingressNamespace}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{label: "traefik"}},
			})
		}
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To: peers,
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcpProtocol, Port: intstrPtr(443)},
				{Protocol: &tcpProtocol, Port: intstrPtr(8443)},
			},
		})
	}
	return policy
}

// applyConfiguredPodEgress opens TCP 443 to cluster-admin destination CIDRs.
// Empty MCP_POD_EGRESS_CIDRS leaves the default policy unchanged. Private
// destinations must list MCP_POD_EGRESS_EXCEPT_CIDRS so pod and service ranges
// stay on the normal namespace policy.
func applyConfiguredPodEgress(policy *networkingv1.NetworkPolicy) error {
	allows, err := egresscidrs.Parse(os.Getenv("MCP_POD_EGRESS_CIDRS"))
	if err != nil {
		return err
	}
	if len(allows) == 0 {
		return nil
	}
	except, err := egresscidrs.Parse(os.Getenv("MCP_POD_EGRESS_EXCEPT_CIDRS"))
	if err != nil {
		return err
	}
	blocks, err := egresscidrs.Blocks(allows, except)
	if err != nil {
		return err
	}
	tcpProtocol := corev1.ProtocolTCP
	for _, block := range blocks {
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{
				IPBlock: &networkingv1.IPBlock{CIDR: block.CIDR, Except: block.Except},
			}},
			Ports: []networkingv1.NetworkPolicyPort{{
				Protocol: &tcpProtocol,
				Port:     intstrPtr(443),
			}},
		})
	}
	return nil
}

func intstrPtr(port int) *intstr.IntOrString {
	v := intstr.FromInt(port)
	return &v
}
