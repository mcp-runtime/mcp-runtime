package platform

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/resourcehistory"
)

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
