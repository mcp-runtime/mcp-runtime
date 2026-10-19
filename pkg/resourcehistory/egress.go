package resourcehistory

import (
	"context"
	"fmt"
	"net"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const NodeEgressPolicy = "mcp-runtime-api-resource-history-egress"

// NodeEgress reads setup's public serving trust and the current node identities.
// Both setup and update use it; live policy rules never establish trust. Missing
// trust permits no kubelet destinations, while invalid or unreadable trust stops
// reconciliation. Discovery is bounded and never writes to the cluster.
func NodeEgress(ctx context.Context, client kubernetes.Interface, namespace string) ([]networkingv1.NetworkPolicyEgressRule, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, TrustConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read resource history trust: %w", err)
	}
	targets, err := DecodeTargets(cm.Data["targets.json"])
	if err != nil {
		return nil, err
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 65})
	if err != nil {
		return nil, fmt.Errorf("list resource history egress nodes: %w", err)
	}
	if len(nodes.Items) > 64 || nodes.Continue != "" {
		return nil, fmt.Errorf("resource history supports at most 64 nodes")
	}
	var cidrs []string
	for _, node := range nodes.Items {
		target, ok := targets[node.Name]
		if !ok || !target.matches(node) {
			continue
		}
		ip, bits := net.ParseIP(target.Address), 128
		if ip.To4() != nil {
			bits = 32
		}
		cidrs = append(cidrs, fmt.Sprintf("%s/%d", ip.String(), bits))
	}
	// Node list order and equivalent duplicate destinations must not cause drift.
	slices.Sort(cidrs)
	cidrs = slices.Compact(cidrs)
	var rules []networkingv1.NetworkPolicyEgressRule
	for _, cidr := range cidrs {
		protocol, port := corev1.ProtocolTCP, intstr.FromInt(10250)
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port}},
		})
	}
	return rules, nil
}
