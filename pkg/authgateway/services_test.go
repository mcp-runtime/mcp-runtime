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
	if len(service.FormFields) != 2 || service.FormFields[0].Name != "header1" || service.FormFields[0].Label != "X-GitLab-Username" || service.FormFields[0].Secret ||
		service.FormFields[1].Name != "header2" || service.FormFields[1].Label != "X-GitLab-PAT-Token" || !service.FormFields[1].Secret {
		t.Fatalf("form fields: %+v", service.FormFields)
	}
	if service.Headers[1].Name != "X-GitLab-PAT-Token" || service.Headers[1].Type != "direct" || service.Headers[1].Field != "header2" {
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
		"gateway-owned header":      func(in *ServerInput) { in.Headers = []string{"X-One", "Sec-Token"} },
		"via header":                func(in *ServerInput) { in.Headers = []string{"Via"} },
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

func TestParseAcceptsDerivedServicesWithoutAnyProvider(t *testing.T) {
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
	if got := cfg.Resources()["https://auth.example.com/gitlab/mcp"]; len(got) != 1 || got[0] != DefaultScope {
		t.Fatalf("resources: %v", cfg.Resources())
	}
}

func TestServiceForAcceptsAnyValidHeaderName(t *testing.T) {
	in := gitlabInput()
	in.Headers = []string{"Authorization", "X_Api_Key", "1st-Token", "X-Tenant.ID", "api~key"}
	service, err := ServiceFor(in, "auth.example.com", "")
	if err != nil {
		t.Fatalf("valid header names were rejected: %v", err)
	}
	if len(service.FormFields) != 5 || len(service.Headers) != 5 {
		t.Fatalf("want one input per header: %+v", service.FormFields)
	}
	for i, header := range in.Headers {
		if service.Headers[i].Name != header || service.FormFields[i].Label != header || service.Headers[i].Field != service.FormFields[i].Name {
			t.Fatalf("header %q lost its name or wiring: %+v %+v", header, service.Headers[i], service.FormFields[i])
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"listen": map[string]any{"host": "0.0.0.0", "port": 8090}, "authorization_server": map[string]any{"issuer": "https://auth.example.com/mcp-auth"},
		"services": []any{service},
	})
	if _, err := Parse(raw); err != nil {
		t.Fatalf("derived configuration is invalid: %v", err)
	}
}
