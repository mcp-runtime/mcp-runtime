package authgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const pemKey = "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"

func wiringConfig(t *testing.T) *Config {
	t.Helper()
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildWiringDescribesTheGatewayToMcpAuth(t *testing.T) {
	w, err := BuildWiring(wiringConfig(t), map[string]string{"demo": pemKey})
	if err != nil {
		t.Fatal(err)
	}
	var external []struct {
		ID, Resource   string
		RequiredScopes []string `json:"required_scopes"`
		ResourceClient struct {
			ClientID      string `json:"client_id"`
			PublicKeyFile string `json:"public_key_file"`
		} `json:"resource_client"`
	}
	if err := json.Unmarshal([]byte(w.Files["external-credentials.json"]), &external); err != nil || len(external) != 1 {
		t.Fatalf("external credentials: %v %s", err, w.Files["external-credentials.json"])
	}
	if e := external[0]; e.ID != "demo" || e.Resource != "https://auth.example.com/demo/mcp" || e.ResourceClient.ClientID != "demo-gateway" ||
		e.ResourceClient.PublicKeyFile != WiringMountPath+"/demo.public.pem" || len(e.RequiredScopes) != 1 {
		t.Fatalf("external credential: %+v", e)
	}
	if !strings.Contains(w.Files["demo.public.pem"], "BEGIN PUBLIC KEY") || strings.Contains(w.Files["demo.public.pem"], "PRIVATE") {
		t.Fatal("the public key file is wrong")
	}

	var methods struct {
		Methods []struct {
			ID, Type, Connector, SubjectPrefix string
			Resources                          []string
			SubjectPrefixRaw                   string `json:"subject_prefix"`
		}
	}
	if err := json.Unmarshal([]byte(w.Files["resource-logins.json"]), &methods); err != nil || len(methods.Methods) != 1 {
		t.Fatalf("resource logins: %v", err)
	}
	if m := methods.Methods[0]; m.ID != "form-demo" || m.Type != "oidc" || m.Connector != ConnectorName("demo") || m.SubjectPrefixRaw != "form-demo:" || len(m.Resources) != 1 || m.Resources[0] != "https://auth.example.com/demo/mcp" {
		t.Fatalf("login method: %+v", m)
	}

	var connector map[string]any
	if err := json.Unmarshal(w.Connectors[ConnectorName("demo")], &connector); err != nil {
		t.Fatal(err)
	}
	base := "https://auth.example.com/demo/mcp/login"
	for key, want := range map[string]string{
		"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "userinfo_endpoint": base + "/userinfo",
		"client_id": "login-demo", "token_endpoint_auth_method": "none",
	} {
		if connector[key] != want {
			t.Errorf("connector %s = %v, want %s", key, connector[key], want)
		}
	}
	if callbacks := connector["allowed_upstream_callback_uris"].([]any); len(callbacks) != 1 || callbacks[0] != "https://auth.example.com/mcp-auth/identity/callback" {
		t.Fatalf("callbacks: %v", callbacks)
	}
	if _, ok := connector["client_secret"]; ok {
		t.Fatal("a public client must not carry a secret")
	}
}

func TestBuildWiringRequiresEveryPublicKey(t *testing.T) {
	if _, err := BuildWiring(wiringConfig(t), map[string]string{}); err == nil {
		t.Fatal("a service with no public key was wired")
	}
	if _, err := BuildWiring(wiringConfig(t), map[string]string{"demo": "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"}); err == nil {
		t.Fatal("a private key was accepted as the public key")
	}
}

func TestMergeConnectorsKeepsForeignEntriesAndDropsStaleOwnedOnes(t *testing.T) {
	existing := `{"keycloak":{"issuer":"https://kc/realms/x","client_secret_env":"KC_SECRET"},"` + ConnectorName("gone") + `":{"issuer":"old"}}`
	owned := map[string]json.RawMessage{ConnectorName("demo"): json.RawMessage(`{"issuer":"new"}`)}
	out, err := MergeConnectors(existing, owned)
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &merged); err != nil {
		t.Fatal(err)
	}
	var kept bytes.Buffer
	if err := json.Compact(&kept, merged["keycloak"]); err != nil || kept.String() != `{"issuer":"https://kc/realms/x","client_secret_env":"KC_SECRET"}` {
		t.Fatalf("the installation's own connector changed: %s", merged["keycloak"])
	}
	if _, ok := merged[ConnectorName("gone")]; ok {
		t.Fatal("a stale owned entry survived")
	}
	if _, ok := merged[ConnectorName("demo")]; !ok {
		t.Fatal("the new entry is missing")
	}
	again, _ := MergeConnectors(out, owned)
	if again != out {
		t.Fatal("merging twice changed the result")
	}
	if _, err := MergeConnectors("not json", owned); err == nil {
		t.Fatal("an invalid existing file was accepted")
	}
}
