package credential

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-runtime/pkg/authgateway"
)

// The CLI derives service entries and validates them with pkg/authgateway; this
// package parses the same JSON at runtime. The test keeps the two in step: an
// entry the CLI produces must load here and keep the names mcp-auth is wired to.
func TestServicesDerivedByTheCLILoadHere(t *testing.T) {
	derived, err := authgateway.ServiceFor(authgateway.ServerInput{
		Name: "gitlab", Namespace: "mcp-servers", IngressHost: "mcp.example.com", IngressPath: "/gitlab/mcp",
		Headers: []string{"X-GitLab-Username", "X-GitLab-PAT-Token"}, ValidationPath: "/auth/check",
	}, "auth.example.com", "mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := newKey(t)
	files := map[string]string{
		"session.key": strings.Repeat("k", 40),
		"client.pem":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"listen":                   map[string]any{"host": "0.0.0.0", "port": 8090},
		"authorization_server":     map[string]any{"issuer": "https://auth.example.com/mcp-auth"},
		"session_binding_key_file": authgateway.MountPath + "/session.key",
		"services":                 []any{derived},
	})
	// The CLI validates against the in-pod mount path; rewrite it to the temp dir.
	data := strings.ReplaceAll(string(raw), authgateway.MountPath, dir)

	if _, err := authgateway.Parse(raw); err != nil {
		t.Fatalf("CLI parser rejected its own entry: %v", err)
	}
	cfg, err := ParseConfig([]byte(data))
	if err != nil {
		t.Fatalf("gateway rejected an entry the CLI derived: %v", err)
	}
	svc := cfg.Services[0]
	if svc.ID != derived.ID || svc.ResourceURL != derived.ResourceURL || len(svc.FormFields) != len(derived.FormFields) || len(svc.Headers) != len(derived.Headers) {
		t.Fatalf("derived entry changed meaning: %+v", svc)
	}
	// mcp-auth is wired with the same names the gateway answers to.
	wiring, err := authgateway.BuildWiring(mustParse(t, raw), map[string]string{derived.ID: "-----BEGIN PUBLIC KEY-----\nx\n-----END PUBLIC KEY-----"})
	if err != nil {
		t.Fatal(err)
	}
	var conn string
	for name := range wiring.Connectors {
		conn = name
	}
	if !strings.Contains(string(mustJSON(t, wiring)), LoginClientID(derived.ID)) || !strings.Contains(string(mustJSON(t, wiring)), SubjectPrefix(derived.ID)) || conn == "" {
		t.Fatalf("wiring does not use the gateway's login client id and subject prefix: %s", mustJSON(t, wiring))
	}
}

func mustParse(t *testing.T, raw []byte) *authgateway.Config {
	t.Helper()
	cfg, err := authgateway.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
