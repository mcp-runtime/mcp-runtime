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
	"mcp-runtime/pkg/platforminventory"
)

func bundleValues(t *testing.T, manifest string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	decoder := yaml.NewDecoder(bytes.NewBufferString(manifest))
	for {
		var doc struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			StringData map[string]string `yaml:"stringData"`
		}
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[doc.Metadata.Name] = doc.StringData
	}
	return out
}

func TestCredentialBundleCopiesOnlyConsumerKeys(t *testing.T) {
	values := map[string]string{}
	for _, set := range platforminventory.CredentialSets() {
		for _, key := range set.Keys {
			values[key] = "value-" + key
		}
	}
	data, err := yaml.Marshal(map[string]any{"metadata": map[string]string{"namespace": "mcp-platform"}, "stringData": values})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := splitCredentialManifest(string(data))
	if err != nil {
		t.Fatal(err)
	}
	objects := bundleValues(t, rendered)
	for _, set := range platforminventory.CredentialSets() {
		got := objects[set.Name]
		if len(got) != len(set.Keys) {
			t.Fatalf("%s has excess/missing keys", set.Name)
		}
		for _, key := range set.Keys {
			if got[key] != values[key] {
				t.Fatalf("%s/%s not preserved", set.Name, key)
			}
		}
	}
	for _, name := range []string{"mcp-ui-credentials", "mcp-ingest-credentials", "mcp-grafana-credentials", "mcp-analytics-api-credentials"} {
		if _, ok := objects[name]["POSTGRES_DSN"]; ok {
			t.Fatalf("database credentials leaked to %s", name)
		}
	}
	if _, err := splitCredentialManifest("stringData:\n  UNKNOWN: forbidden\n"); err == nil {
		t.Fatal("undeclared key accepted")
	}
}

func TestCredentialBundleMigrationAndOwnerPrecedence(t *testing.T) {
	read := func(ns, name, key string) (string, error) { return "installed-" + key, nil }
	manifest, err := renderCredentialBundleWithSnapshots(read, nil)
	if err != nil {
		t.Fatal(err)
	}
	objects := bundleValues(t, manifest)
	if objects["mcp-grafana-credentials"]["GRAFANA_ADMIN_PASSWORD"] != "installed-GRAFANA_ADMIN_PASSWORD" {
		t.Fatal("legacy credential regenerated")
	}
	owner := map[string]map[string]string{"mcp-grafana-credentials": {"GRAFANA_ADMIN_PASSWORD": "rotated"}}
	manifest, err = renderCredentialBundleWithSnapshots(read, owner)
	if err != nil {
		t.Fatal(err)
	}
	objects = bundleValues(t, manifest)
	if objects["mcp-grafana-credentials"]["GRAFANA_ADMIN_PASSWORD"] != "rotated" {
		t.Fatal("owner rotation not applied to the grafana credential set")
	}
	owner = map[string]map[string]string{"mcp-platform-api-credentials": objects["mcp-platform-api-credentials"]}
	owner["mcp-platform-api-credentials"]["ADMIN_API_KEYS"] = ""
	manifest, err = renderCredentialBundleWithSnapshots(read, owner)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(objectsValue(t, manifest, "mcp-runtime-api-credentials", "ADMIN_API_KEYS"), "installed-ADMIN_API_KEYS") {
		t.Fatal("revoked owner key resurrected from legacy")
	}
}
func objectsValue(t *testing.T, manifest, name, key string) string {
	return bundleValues(t, manifest)[name][key]
}

func TestCredentialBundleReadFailureDoesNotGenerateReplacement(t *testing.T) {
	_, err := renderCredentialBundleWithSnapshots(func(ns, name, key string) (string, error) { return "", errors.New("denied") }, nil)
	if err == nil {
		t.Fatal("read error ignored")
	}
}

func TestWorkloadSecretReferencesMatchConsumerAllowlists(t *testing.T) {
	allowed := map[string]map[string]bool{}
	for _, set := range platforminventory.CredentialSets() {
		allowed[set.Name] = map[string]bool{}
		for _, key := range set.Keys {
			allowed[set.Name][key] = true
		}
	}
	root := "../../../../k8s"
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if ref, ok := node["secretKeyRef"].(map[string]any); ok {
				name, _ := ref["name"].(string)
				key, _ := ref["key"].(string)
				if name == platforminventory.LegacyCredentialSecret {
					t.Errorf("workload still mounts legacy secret for %s", key)
				}
				if keys, ok := allowed[name]; ok {
					count++
					if !keys[key] {
						t.Errorf("undeclared consumer key %s/%s", name, key)
					}
				}
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	consumerFiles := map[string]bool{}
	for _, name := range []string{"06-ingest.yaml", "08-platform-api.yaml", "08-runtime-api.yaml", "08-analytics-api.yaml", "09-ui.yaml", "12-grafana.yaml", "14-mcp-gateway-sidecar.yaml", "20-postgres.yaml", "20-postgres-hostpath.yaml", "21-platform-admin-bootstrap-job.yaml"} {
		consumerFiles[name] = true
	}
	for _, file := range files {
		if !consumerFiles[file.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc map[string]any
			err := decoder.Decode(&doc)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			visit(doc)
		}
	}
	if count < 30 {
		t.Fatalf("only %d consumer references checked", count)
	}
}
