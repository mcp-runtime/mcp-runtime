package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// Exercise the actual QA preparation block without its subsequent cluster apply.
// This catches script edits that corrupt otherwise valid CLI-generated YAML.
func TestQAPolicyGrantManifest(t *testing.T) {
	runCLI(t, "access", "grant", "init", "--help")
	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, "test", "e2e", "qa-e2e.sh"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), `  "${PROJECT_ROOT}/bin/mcp-runtime" access --use-kube grant init "${SERVER_NAME}-grant"`)
	if start < 0 {
		t.Fatal("QA grant preparation command missing")
	}
	block := string(source[start:])
	end := strings.Index(block, `  (cd "${WORKDIR}" && "${PROJECT_ROOT}/bin/mcp-runtime" access --use-kube grant apply`)
	if end < 0 {
		t.Fatal("QA grant apply boundary missing")
	}
	workdir := t.TempDir()
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	// #nosec G204 -- execute a trusted repo test-script fragment, before any apply.
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+block[:end])
	cmd.Env = append(os.Environ(), "PROJECT_ROOT="+root, "WORKDIR="+workdir,
		"SERVER_NAME=qa-policy", "HUMAN_ID=qa-human", "AGENT_ID=qa-agent",
		"ADAPTER_TEAM_ID=qa-team", "ADAPTER_AGENT_EXPIRES_AT="+expiresAt,
		"KUBECONFIG="+filepath.Join(workdir, "empty-kubeconfig"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("QA grant preparation failed: %v\n%s", err, output)
	}
	body, err := os.ReadFile(filepath.Join(workdir, "access-grant.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseQAGrantManifest(body)
	if err != nil {
		t.Fatalf("QA generated invalid grant YAML: %v\n%s", err, body)
	}
	spec := manifest["spec"].(map[string]any)
	if spec["expiresAt"] != expiresAt {
		t.Fatalf("expiry = %v, want %s", spec["expiresAt"], expiresAt)
	}
	subject := spec["subject"].(map[string]any)
	if subject["humanID"] != "qa-human" || subject["agentID"] != "qa-agent" || subject["teamID"] != "qa-team" {
		t.Fatalf("grant subject changed: %v", subject)
	}
	if len(spec["toolRules"].([]any)) != 3 {
		t.Fatalf("grant tool rules changed: %v", spec["toolRules"])
	}
	// Reproduce the original two-space insertion into yaml.v3's four-space spec.
	// The same parser used for the live fixture must detect this regression.
	malformed := strings.Replace(string(body), "spec:\n", "spec:\n  expiresAt: "+expiresAt+"\n", 1)
	if _, err := parseQAGrantManifest([]byte(malformed)); err == nil {
		t.Fatal("parser accepted the historical mixed-indentation YAML corruption")
	}
}

func parseQAGrantManifest(body []byte) (map[string]any, error) {
	data, err := yaml.YAMLToJSONStrict(body)
	if err != nil {
		return nil, err
	}
	var manifest map[string]any
	err = json.Unmarshal(data, &manifest)
	return manifest, err
}
