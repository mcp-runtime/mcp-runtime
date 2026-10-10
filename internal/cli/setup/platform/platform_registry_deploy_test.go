package platform

import (
	"strings"
	"testing"
)

func TestValidateRegistryTypeRejectsUnsupportedValues(t *testing.T) {
	if err := validateRegistryType("docker"); err != nil {
		t.Fatalf("validateRegistryType(docker) error = %v", err)
	}
	if err := validateRegistryType("harbor"); err == nil {
		t.Fatal("validateRegistryType(harbor) error = nil, want unsupported registry type")
	}
}

func TestMutateRegistryManifestUsesStructuredRegistryUpdates(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: registry
spec:
  template:
    spec:
      containers:
      - name: registry
        # image: registry:2.8.3 in a comment should not be rewritten
        image: registry:2.8.3
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: registry
  namespace: registry
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
spec:
  rules:
  - host: registry.local
`

	rendered, err := mutateRegistryManifest(manifest, "registry.example.com", "registry.example.com/registry:2.8.3", "")
	if err != nil {
		t.Fatalf("mutateRegistryManifest() error = %v", err)
	}
	if strings.Contains(rendered, "cert-manager.io/cluster-issuer") {
		t.Fatalf("expected cluster issuer annotation to be removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "registry.example.com") {
		t.Fatalf("expected registry host rewrite, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "image: registry.example.com/registry:2.8.3") {
		t.Fatalf("expected registry image override, got:\n%s", rendered)
	}
	if strings.Count(rendered, "registry.example.com/registry:2.8.3") != 1 {
		t.Fatalf("expected only the container image to be rewritten, got:\n%s", rendered)
	}
}
