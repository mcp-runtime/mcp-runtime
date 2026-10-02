package platform

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/core"
)

func TestTraefikDoesNotWatchLogCollectorNamespace(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	for _, path := range []string{
		"config/ingress/base/traefik.yaml",
		"config/ingress/overlays/http/deployment-args.patch.yaml",
		"config/ingress/overlays/prod/traefik-no-redirect.yaml",
	} {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, "--providers.kubernetesingress.namespaces=") && strings.Contains(line, core.LogCollectorNamespace) {
				t.Errorf("%s grants Traefik an unused collector watch: %s", path, line)
			}
		}
	}
}

func TestLogCollectorPodSecurityBoundary(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return data
	}

	policies := map[string][]string{}
	for _, path := range []string{"k8s/00-namespace.yaml", "config/ingress/base/traefik.yaml"} {
		decoder := yaml.NewDecoder(bytes.NewReader(read(path)))
		for {
			var document struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name   string            `yaml:"name"`
					Labels map[string]string `yaml:"labels"`
				} `yaml:"metadata"`
			}
			if err := decoder.Decode(&document); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("decode namespaces from %s: %v", path, err)
			}
			if document.Kind == "Namespace" {
				policies[document.Metadata.Name] = append(policies[document.Metadata.Name], document.Metadata.Labels["pod-security.kubernetes.io/enforce"])
			}
		}
	}
	if got := policies[core.ComponentNamespace("platform-api")]; len(got) != 1 || got[0] != "restricted" {
		t.Fatalf("%s namespace declarations = %v, want one restricted owner", core.ComponentNamespace("platform-api"), got)
	}
	if got := policies[core.LogCollectorNamespace]; len(got) != 1 || got[0] != "privileged" {
		t.Fatalf("%s Pod Security declarations = %v, want one privileged hostPath collector namespace", core.LogCollectorNamespace, got)
	}

	var collectorNamespaces []string
	decoder := yaml.NewDecoder(bytes.NewReader(read("k8s/18-promtail.yaml")))
	for {
		var document struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode promtail manifest: %v", err)
		}
		if document.Kind == "ServiceAccount" || document.Kind == "ConfigMap" || document.Kind == "DaemonSet" {
			collectorNamespaces = append(collectorNamespaces, document.Metadata.Namespace)
		}
	}
	for _, namespace := range collectorNamespaces {
		if namespace != core.LogCollectorNamespace {
			t.Fatalf("Promtail namespaced resources span namespaces: %v", collectorNamespaces)
		}
	}
}
