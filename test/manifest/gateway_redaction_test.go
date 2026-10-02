package manifest_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const middlewareAnnotation = "traefik.ingress.kubernetes.io/router.middlewares"

type gatewayDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
}

func TestSentinelGatewayRedactionScope(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", "10-gateway.yaml"))
	if err != nil {
		t.Fatalf("read gateway manifest: %v", err)
	}

	ingresses := map[string]map[string]string{}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc gatewayDoc
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode gateway manifest: %v", err)
		}
		if doc.Kind == "Ingress" && (doc.Metadata.Namespace == "mcp-platform" || doc.Metadata.Namespace == "mcp-observability") {
			ingresses[doc.Metadata.Name] = doc.Metadata.Annotations
		}
	}

	for name, annotations := range ingresses {
		middlewares := annotations[middlewareAnnotation]
		if name != "mcp-platform-gateway-ingest" && strings.Contains(middlewares, "pii-redactor") {
			t.Fatalf("%s must not use pii-redactor; control-plane API and UI fields must stay exact", name)
		}
	}

	ingestAnnotations, ok := ingresses["mcp-platform-gateway-ingest"]
	if !ok {
		t.Fatal("mcp-platform-gateway-ingest ingress not found")
	}
	if middlewares := ingestAnnotations[middlewareAnnotation]; !strings.Contains(middlewares, "pii-redactor@file") {
		t.Fatalf("ingest middleware = %q, want pii-redactor@file", middlewares)
	}
}
