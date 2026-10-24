package authgateway

import (
	"encoding/json"
	"strings"
	"testing"
)

const sample = `{"listen":{"host":"0.0.0.0","port":8090},"authorization_server":{"issuer":"https://auth.example.com/mcp-auth"},"services":[{"id":"demo","resource_url":"https://auth.example.com/demo/mcp","upstream_url":"https://up.example.com/mcp","validation_url":"https://up.example.com/check","required_scopes":["tools:read"],"resource_client":{"client_id":"demo-gateway","key_id":"k1","private_key_file":"/etc/mcp-gateway-credential/client.pem"}}]}`

func TestGatewayConfigRejectsUnsafeRoutingAndUnknownRegistration(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(sample, "https://auth.example.com/demo/mcp", "https://attacker.example.com/demo/mcp", 1),
		strings.Replace(sample, "https://auth.example.com/demo/mcp", "https://auth.example.com/mcp-auth/token", 1),
		strings.Replace(sample, "https://up.example.com/check", "https://different.example.com/check", 1),
		strings.Replace(sample, "0.0.0.0", "127.0.0.1", 1),
		strings.Replace(sample, "8090", "8080", 1),
		sample + sample,
		strings.Replace(sample, `"client_id":"demo-gateway"`, `"client_id":""`, 1),
		strings.Replace(sample, "/etc/mcp-gateway-credential/client.pem", "/etc/passwd", 1),
		strings.Replace(sample, `"id":"demo"`, `"id":"`+strings.Repeat("a", 28)+`"`, 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("unsafe gateway configuration accepted")
		}
	}
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Resources())
	if _, err := RegisteredResources(string(b), c.AuthorizationServer.Issuer); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisteredResources(`{"https://attacker.example/mcp":["tools:read"]}`, c.AuthorizationServer.Issuer); err == nil {
		t.Fatal("external registration accepted")
	}
}
