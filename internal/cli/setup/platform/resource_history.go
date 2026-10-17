package platform

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/resourcehistory"
)

// Only public certificates are read. No node key, kubeconfig or workload file
// is copied into the trust ConfigMap. This read-only Docker path is limited to
// the explicitly selected Kind cluster in test mode.
func readKindKubeletCertificate(ctx context.Context, cluster string, node corev1.Node, expectedCA []byte) ([]byte, error) {
	if node.Spec.ProviderID != "kind://docker/"+cluster+"/"+node.Name || !core.ValidK8sName.MatchString(node.Name) {
		return nil, fmt.Errorf("node is not owned by the selected Docker Kind cluster")
	}
	// #nosec G204 -- fixed executable/arguments; node is validated above.
	inspect, err := exec.CommandContext(ctx, "docker", "inspect", node.Name).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect named Kind node: %w", err)
	}
	var containers []struct {
		Config          struct{ Labels map[string]string }
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress         string
				GlobalIPv6Address string
			}
		}
	}
	if json.Unmarshal(inspect, &containers) != nil || len(containers) != 1 || containers[0].Config.Labels["io.x-k8s.kind.cluster"] != cluster {
		return nil, fmt.Errorf("docker node label does not match selected Kind cluster")
	}
	matched := false
	for _, network := range containers[0].NetworkSettings.Networks {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP && (address.Address == network.IPAddress || address.Address == network.GlobalIPv6Address) {
				matched = true
			}
		}
	}
	if !matched {
		return nil, fmt.Errorf("docker and Kubernetes node addresses differ")
	}
	// Same-named clusters can exist in different Docker contexts. Public API
	// CA identity must match the explicitly selected Kubernetes installation.
	// #nosec G204 -- validated node; fixed public CA path, never a key.
	nodeCA, err := exec.CommandContext(ctx, "docker", "exec", node.Name, "cat", "/etc/kubernetes/pki/ca.crt").Output()
	if err != nil {
		return nil, fmt.Errorf("read named Kind public cluster CA")
	}
	nodeCA, err = resourcehistory.PublicCertificates(nodeCA)
	if err != nil {
		return nil, err
	}
	expectedCA, err = resourcehistory.PublicCertificates(expectedCA)
	if err != nil || !bytes.Equal(nodeCA, expectedCA) {
		return nil, fmt.Errorf("docker Kind node cluster CA differs from selected Kubernetes target")
	}
	// #nosec G204 -- validated node; the only readable path is a public cert.
	return exec.CommandContext(ctx, "docker", "exec", node.Name, "cat", "/var/lib/kubelet/pki/kubelet.crt").Output()
}

func resourceHistoryKindCluster() string {
	if os.Getenv("MCP_RUNTIME_TEST_MODE") != "1" {
		return ""
	}
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if platformSetupKubeconfig != "" {
		loading.ExplicitPath = platformSetupKubeconfig
	}
	config, err := loading.Load()
	if err != nil {
		return ""
	}
	cluster, ok := strings.CutPrefix(config.CurrentContext, "kind-")
	if !ok || !core.ValidK8sName.MatchString(cluster) {
		return ""
	}
	return cluster
}

func provisionResourceHistoryTrust() error {
	caPath := strings.TrimSpace(os.Getenv("MCP_KUBELET_METRICS_CA_FILE"))
	cluster := resourceHistoryKindCluster()
	if caPath == "" && cluster == "" {
		core.Warn("Resource history requires trusted kubelet serving certificates. Set MCP_KUBELET_METRICS_CA_FILE in setup's environment, or use the named Docker Kind test-mode path; existing trust is preserved and missing data remains unavailable.")
		return nil
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ca []byte
	if caPath != "" {
		ca, err = os.ReadFile(caPath)
		if err != nil {
			return fmt.Errorf("read kubelet serving CA: %w", err)
		}
		ca, err = resourcehistory.PublicCertificates(ca)
		if err != nil {
			return fmt.Errorf("kubelet serving CA: %w", err)
		}
	}
	var selectedCA []byte
	if len(ca) == 0 && cluster != "" {
		if clients.Config == nil || clients.Config.Insecure {
			return fmt.Errorf("verified Kubernetes CA is required for Kind resource history")
		}
		selectedCA = clients.Config.CAData
		if len(selectedCA) == 0 && clients.Config.CAFile != "" {
			selectedCA, err = os.ReadFile(clients.Config.CAFile)
			if err != nil {
				return fmt.Errorf("read selected Kubernetes public CA")
			}
		}
		if _, err = resourcehistory.PublicCertificates(selectedCA); err != nil {
			return fmt.Errorf("selected Kind Kubernetes CA: %w", err)
		}
	}
	return provisionResourceHistoryTrustWith(ctx, clients.Clientset, ca, cluster, func(ctx context.Context, cluster string, node corev1.Node) ([]byte, error) {
		return readKindKubeletCertificate(ctx, cluster, node, selectedCA)
	})
}

func provisionResourceHistoryTrustWith(ctx context.Context, client kubernetes.Interface, ca []byte, cluster string, readCert func(context.Context, string, corev1.Node) ([]byte, error)) error {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 65})
	if err != nil {
		return fmt.Errorf("list resource history nodes: %w", err)
	}
	if len(nodes.Items) > 64 || nodes.Continue != "" {
		return fmt.Errorf("resource history supports at most 64 nodes")
	}
	var targets []resourcehistory.Target
	for _, node := range nodes.Items {
		address := ""
		for _, candidate := range node.Status.Addresses {
			if candidate.Type == corev1.NodeInternalIP && net.ParseIP(candidate.Address) != nil {
				address = candidate.Address
				break
			}
		}
		if address == "" || node.UID == "" {
			return fmt.Errorf("node %s has no stable UID/InternalIP for resource history", node.Name)
		}
		certificate, serverName := ca, address
		if len(certificate) == 0 {
			if cluster == "" {
				return fmt.Errorf("trusted kubelet serving CA is required")
			}
			certificate, err = readCert(ctx, cluster, node)
			if err != nil {
				return fmt.Errorf("read public serving certificate for %s: %w", node.Name, err)
			}
			certificate, err = resourcehistory.PublicCertificates(certificate)
			if err != nil {
				return err
			}
			block, _ := pem.Decode(certificate)
			if block == nil || block.Type != "CERTIFICATE" {
				return fmt.Errorf("invalid kubelet serving certificate")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return fmt.Errorf("invalid kubelet serving certificate")
			}
			if cert.VerifyHostname(node.Name) != nil || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return fmt.Errorf("kubelet serving certificate identity/validity differs from named Kind node")
			}
			serverName = node.Name
		}
		targets = append(targets, resourcehistory.Target{Name: node.Name, UID: string(node.UID), Address: address, ServerName: serverName, CertificatePEM: string(certificate)})
	}
	encoded, err := resourcehistory.EncodeTargets(targets)
	if err != nil {
		return err
	}
	if _, err = resourcehistory.DecodeTargets(encoded); err != nil {
		return err
	}
	namespace := core.ComponentNamespace("runtime-api")
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, resourcehistory.TrustConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: resourcehistory.TrustConfigMap, Namespace: namespace}, Data: map[string]string{"targets.json": encoded}}, metav1.CreateOptions{})
	} else if err == nil {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["targets.json"] = encoded
		_, err = client.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("provision resource history public trust: %w", err)
	}
	core.Info(fmt.Sprintf("Provisioned verified kubelet resource history trust for %d nodes; absent volume statistics remain unavailable.", len(targets)))
	return nil
}
