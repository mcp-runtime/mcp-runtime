package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/k8sclient"
)

var newKubernetesClients = k8sclient.New

// platformSetupKubeconfig holds the explicit kubeconfig path for the active
// setup run. Initialised by initPlatformKubeconfig so every k8s operation in
// the setup pipeline targets the correct cluster even when multiple kubeconfig
// files exist on the workstation.
var platformSetupKubeconfig string

func initPlatformKubeconfig(path string) {
	platformSetupKubeconfig = path
}

func platformKubernetesClients() (*k8sclient.Clients, error) {
	if platformSetupKubeconfig != "" {
		return k8sclient.NewWithConfig(k8sclient.Config{KubeconfigPath: platformSetupKubeconfig})
	}
	clients, err := newKubernetesClients()
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func applyManifestYAML(manifest string, namespace string, stdout io.Writer) error {
	manifest, err := renderTestModeManifest(manifest)
	if err != nil {
		return err
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	results, err := k8sclient.ApplyManifestYAML(context.Background(), clients, []byte(manifest), namespace)
	if err != nil {
		return err
	}
	writeApplyResults(stdout, results)
	return nil
}

func applyManifestFile(path string, namespace string, stdout io.Writer) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	results, err := k8sclient.ApplyManifestFile(context.Background(), clients, path, namespace)
	if err != nil {
		return err
	}
	writeApplyResults(stdout, results)
	return nil
}

func applyManifestDir(path string, namespace string, stdout io.Writer) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	results, err := k8sclient.ApplyManifestDir(context.Background(), clients, path, namespace)
	if err != nil {
		return err
	}
	writeApplyResults(stdout, results)
	return nil
}

func ensureNamespaceWithLabels(name string, labels map[string]string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.EnsureNamespace(context.Background(), clients, name, labels)
}

func writeApplyResults(stdout io.Writer, results []k8sclient.ApplyResult) {
	if stdout == nil {
		stdout = os.Stdout
	}
	for _, result := range results {
		if strings.TrimSpace(result.Action) == "" {
			continue
		}
		fmt.Fprintln(stdout, result.String())
	}
}

// stampPlatformVersionClientGo labels platform Deployments with the installed
// platform version (metadata only; no rollout).
func stampPlatformVersionClientGo(version string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return platformrelease.StampInstalledVersion(context.Background(), clients.Clientset, version)
}
