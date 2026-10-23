package authgateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func gitlabInput() ServerInput {
	return ServerInput{
		Name: "gitlab", Namespace: "mcp-servers", IngressHost: "mcp.example.com", IngressPath: "/gitlab/mcp",
		Headers: []string{"X-GitLab-Username", "X-GitLab-PAT-Token"}, ValidationPath: "/auth/check",
	}
}

func TestServiceForDerivesAFormServiceFromTheServer(t *testing.T) {
	service, err := ServiceFor(gitlabInput(), "auth.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if service.ID != "gitlab" || service.ResourceURL != "https://auth.example.com/gitlab/mcp" ||
		service.UpstreamURL != "https://mcp.example.com/gitlab/mcp" || service.ValidationURL != "https://mcp.example.com/gitlab/mcp/auth/check" {
		t.Fatalf("urls: %+v", service)
	}
	if service.Source != SourceForm || service.ResourceClient.ClientID != "gateway-gitlab" || service.ResourceClient.PrivateKeyFile != ClientKeyFile {
		t.Fatalf("source or client: %+v", service)
	}
	if len(service.FormFields) != 2 || service.FormFields[0].Name != "x-gitlab-username" || service.FormFields[0].Secret ||
		service.FormFields[1].Name != "x-gitlab-pat-token" || !service.FormFields[1].Secret {
		t.Fatalf("form fields: %+v", service.FormFields)
	}
	if service.Headers[1].Name != "X-GitLab-PAT-Token" || service.Headers[1].Type != "direct" || service.Headers[1].Field != "x-gitlab-pat-token" {
		t.Fatalf("header rules: %+v", service.Headers)
	}
}

func TestServiceForHonorsExplicitSecretHeadersAndPublicHostFallback(t *testing.T) {
	in := gitlabInput()
	in.IngressHost = ""
	in.SecretHeaders = []string{"x-gitlab-username"}
	service, err := ServiceFor(in, "auth.example.com", "mcp.fallback.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(service.UpstreamURL, "https://mcp.fallback.example.com/") {
		t.Fatalf("fallback host unused: %s", service.UpstreamURL)
	}
	if !service.FormFields[0].Secret || service.FormFields[1].Secret {
		t.Fatalf("explicit secret headers must replace the name heuristic: %+v", service.FormFields)
	}
}

func TestServiceForRejectsUnsafeInput(t *testing.T) {
	mutations := map[string]func(*ServerInput){
		"uppercase-only invalid id": func(in *ServerInput) { in.Name = "Git_Lab" },
		"no host":                   func(in *ServerInput) { in.IngressHost = "" },
		"host with path":            func(in *ServerInput) { in.IngressHost = "mcp.example.com/evil" },
		"host with credentials":     func(in *ServerInput) { in.IngressHost = "user@evil.example.com" },
		"trailing slash path":       func(in *ServerInput) { in.IngressPath = "/gitlab/mcp/" },
		"relative path":             func(in *ServerInput) { in.IngressPath = "gitlab/mcp" },
		"query in validation":       func(in *ServerInput) { in.ValidationPath = "/check?x=1" },
		"traversal in validation":   func(in *ServerInput) { in.ValidationPath = "/../admin" },
		"header not a field":        func(in *ServerInput) { in.Headers = []string{"X-One", "1-bad"} },
		"unknown secret header":     func(in *ServerInput) { in.SecretHeaders = []string{"x-other"} },
	}
	for name, mutate := range mutations {
		in := gitlabInput()
		mutate(&in)
		if _, err := ServiceFor(in, "auth.example.com", ""); err == nil {
			t.Errorf("accepted: %s", name)
		}
	}
	if _, err := ServiceFor(gitlabInput(), "", ""); err == nil {
		t.Error("accepted an empty auth host")
	}
}

func TestParseAcceptsFormOnlyConfigWithoutProvider(t *testing.T) {
	service, _ := ServiceFor(gitlabInput(), "auth.example.com", "")
	doc := map[string]any{
		"listen": map[string]any{"host": "0.0.0.0", "port": 8090}, "authorization_server": map[string]any{"issuer": "https://auth.example.com/mcp-auth"},
		"services": []any{service},
	}
	raw, _ := json.Marshal(doc)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("form-only config rejected: %v", err)
	}
	if cfg.HasVaultServices() {
		t.Fatal("a form service must not need the vault")
	}
	if got := cfg.Resources()["https://auth.example.com/gitlab/mcp"]; len(got) != 1 || got[0] != DefaultScope {
		t.Fatalf("resources: %v", cfg.Resources())
	}
	vault := map[string]any{"id": "other", "resource_url": "https://auth.example.com/other/mcp", "upstream_url": "https://u.example.com/mcp", "validation_url": "https://u.example.com/check", "required_scopes": []string{"tools:read"}}
	doc["services"] = []any{service, vault}
	raw, _ = json.Marshal(doc)
	if _, err := Parse(raw); err == nil {
		t.Fatal("a vault service without a provider must be rejected")
	}
}
