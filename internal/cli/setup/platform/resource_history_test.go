package platform

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/resourcehistory"
)

func TestResourceHistoryPublicCAFileIsBoundedAndCertificateOnly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	for _, tc := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"public", certificate, true},
		{"oversized", bytes.Repeat([]byte("x"), (256<<10)+1), false},
		{"mixed-private", append(append([]byte{}, certificate...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("synthetic-test-only")})...), false},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "public-ca.pem")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readResourceHistoryPublicCA(path)
			if (err == nil) != tc.valid || tc.valid && !bytes.Equal(got, certificate) {
				t.Fatalf("valid=%v, result=%q, error=%v", tc.valid, got, err)
			}
		})
	}
	if _, err := readResourceHistoryPublicCA(t.TempDir()); err == nil {
		t.Fatal("directory accepted as public CA")
	}
	if _, err := readResourceHistoryPublicCA(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing public CA accepted")
	}
}

func TestResourceHistorySetupTrustBindsCurrentNode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "example.com", UID: "new-node"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}}}
	client := fake.NewSimpleClientset(node)
	called := false
	read := func(context.Context, string, corev1.Node) ([]byte, error) { called = true; return certificate, nil }
	if err := provisionResourceHistoryTrustWith(context.Background(), client, nil, "test", read); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Kind public certificate not read")
	}
	cm, err := client.CoreV1().ConfigMaps(core.ComponentNamespace("runtime-api")).Get(context.Background(), resourcehistory.TrustConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := resourcehistory.DecodeTargets(cm.Data["targets.json"])
	if err != nil {
		t.Fatal(err)
	}
	target := targets[node.Name]
	if target.UID != "new-node" || target.Address != "127.0.0.1" || target.ServerName != node.Name || strings.Contains(cm.Data["targets.json"], "PRIVATE KEY") {
		t.Fatalf("unbound or private trust: %+v", target)
	}
	// An explicit serving CA uses the live IP SAN and never reads Docker.
	called = false
	if err := provisionResourceHistoryTrustWith(context.Background(), client, certificate, "", read); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("public cluster CA path read Docker")
	}
}

func TestResourceHistoryDoesNotReadForeignKindNode(t *testing.T) {
	for _, provider := range []string{"", "kind://docker/other/node", "aws:///node"} {
		_, err := readKindKubeletCertificate(context.Background(), "test", corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Spec: corev1.NodeSpec{ProviderID: provider}}, nil)
		if err == nil {
			t.Fatal("foreign node trusted")
		}
	}
}

func TestResourceHistoryEgressUsesOnlyCurrentTrustedNodes(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	targets, err := resourcehistory.EncodeTargets([]resourcehistory.Target{
		{Name: "current", UID: "current-uid", Address: "172.18.0.2", ServerName: "current", CertificatePEM: certificate},
		{Name: "replaced", UID: "old-uid", Address: "172.18.0.3", ServerName: "replaced", CertificatePEM: certificate},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: resourcehistory.TrustConfigMap, Namespace: "mcp-platform"}, Data: map[string]string{"targets.json": targets}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "current", UID: "current-uid"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "replaced", UID: "new-uid"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.3"}}}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-api-resource-history-egress", Namespace: "mcp-platform"}},
	)
	for range 2 {
		if err := ensureResourceHistoryNodeEgressWith(ctx, client); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := client.NetworkingV1().NetworkPolicies("mcp-platform").Get(ctx, "mcp-runtime-api-resource-history-egress", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("expected only current trusted node, got %+v", policy.Spec.Egress)
	}
	rule := policy.Spec.Egress[0]
	if len(rule.To) != 1 || rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "172.18.0.2/32" || len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != 10250 || *rule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("unexpected kubelet permission: %+v", rule)
	}
	// Trust removal must revoke stale permissions, including a previous broad rule.
	if err := client.CoreV1().ConfigMaps("mcp-platform").Delete(ctx, resourcehistory.TrustConfigMap, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureResourceHistoryNodeEgressWith(ctx, client); err != nil {
		t.Fatal(err)
	}
	policy, err = client.NetworkingV1().NetworkPolicies("mcp-platform").Get(ctx, policy.Name, metav1.GetOptions{})
	if err != nil || len(policy.Spec.Egress) != 0 {
		t.Fatalf("stale node permission retained: %+v, %v", policy, err)
	}
}
