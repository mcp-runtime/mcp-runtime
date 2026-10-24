// Package authgateway owns the deployment contract for the optional bundled
// credential gateway. The gateway collects header values from the person at
// sign-in; this package neither stores nor resolves any credential.
package authgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// ResourceAnnotation records the resources the gateway registered on the
	// mcp-auth Deployment, so the operator keeps them across MCPServer events.
	ResourceAnnotation = "mcpruntime.org/auth-gateway-resources"
	// GatewayName names the gateway's own workload objects in mcp-platform:
	// Deployment, Service, Secret, Ingress and NetworkPolicy.
	GatewayName = "mcp-gateway-credential"
	// MountPath is where the gateway reads its configuration and keys.
	MountPath = "/etc/mcp-gateway-credential"
	// WiringName is the ConfigMap holding what mcp-auth reads about the gateway:
	// the assertion client's public keys and the resource logins.
	WiringName = "mcp-auth-gateway-wiring"
	// WiringMountPath is where mcp-auth mounts that ConfigMap.
	WiringMountPath = "/etc/mcp-auth-gateway-wiring"
	// SourceForm marks a service whose header values are submitted at sign-in.
	SourceForm = "form"
)

type Config struct {
	Listen struct {
		Host string `json:"host"`
		Port int32  `json:"port"`
	} `json:"listen"`
	AuthorizationServer struct {
		Issuer string `json:"issuer"`
	} `json:"authorization_server"`
	SessionBindingKeyFile string    `json:"session_binding_key_file"`
	Services              []Service `json:"services"`
}

// Service is the part of a gateway service entry the deployment contract reads.
type Service struct {
	ID             string         `json:"id"`
	ResourceURL    string         `json:"resource_url"`
	UpstreamURL    string         `json:"upstream_url"`
	ValidationURL  string         `json:"validation_url"`
	RequiredScopes []string       `json:"required_scopes"`
	Source         string         `json:"source"`
	ResourceClient ResourceClient `json:"resource_client"`
}

func Parse(data []byte) (*Config, error) {
	if len(data) > 512<<10 {
		return nil, errors.New("gateway configuration exceeds 512 KiB")
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	if d.Decode(&c) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return nil, errors.New("gateway configuration must contain one JSON object")
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = 8090
	}
	if c.Listen.Host != "0.0.0.0" || c.Listen.Port < 1024 || c.Listen.Port > 65535 || c.Listen.Port == 8080 {
		return nil, errors.New("gateway must listen on 0.0.0.0 with a distinct unprivileged port")
	}
	issuer, err := httpsURL(c.AuthorizationServer.Issuer)
	if err != nil {
		return nil, errors.New("gateway issuer must be an HTTPS URL")
	}
	if len(c.Services) == 0 || len(c.Services) > 16 {
		return nil, errors.New("1-16 services are required")
	}
	seenID, seenURL := map[string]bool{}, map[string]bool{}
	for _, s := range c.Services {
		resource, err := httpsURL(s.ResourceURL)
		if err != nil || resource.Host != issuer.Host || resource.Path == "/" || path.Clean(resource.Path) != resource.Path || strings.HasPrefix(resource.Path, "/.well-known/") || resource.Path == issuer.Path || (strings.Trim(issuer.Path, "/") != "" && strings.HasPrefix(resource.Path, strings.TrimRight(issuer.Path, "/")+"/")) {
			return nil, errors.New("gateway resources must use distinct paths on the existing auth host, outside the OAuth issuer path")
		}
		// The login method mcp-auth gets is "form-<id>", and its id is at most 32 characters.
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,26}$`).MatchString(s.ID) || seenID[s.ID] || seenURL[s.ResourceURL] {
			return nil, errors.New("gateway service IDs (lowercase, at most 27 characters) and resources must be unique")
		}
		if s.ResourceClient.ClientID == "" || s.ResourceClient.KeyID == "" || !strings.HasPrefix(s.ResourceClient.PrivateKeyFile, MountPath+"/") {
			return nil, errors.New("each gateway service needs a resource_client with a key under " + MountPath)
		}
		seenID[s.ID], seenURL[s.ResourceURL] = true, true
		up, err := httpsURL(s.UpstreamURL)
		if err != nil {
			return nil, errors.New("upstream must use HTTPS")
		}
		check, err := httpsURL(s.ValidationURL)
		if err != nil || check.Host != up.Host {
			return nil, errors.New("validation endpoint must use the upstream HTTPS origin")
		}
		if len(s.RequiredScopes) == 0 {
			return nil, errors.New("gateway services require explicit OAuth scopes")
		}
		for _, scope := range s.RequiredScopes {
			if scope == "" || strings.ContainsAny(scope, " \t\r\n\"\\") {
				return nil, errors.New("invalid gateway OAuth scope")
			}
		}
	}
	return &c, nil
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("invalid HTTPS URL")
	}
	return u, nil
}

func (c *Config) Resources() map[string][]string {
	out := map[string][]string{}
	for _, s := range c.Services {
		out[s.ResourceURL] = append([]string(nil), s.RequiredScopes...)
		sort.Strings(out[s.ResourceURL])
	}
	return out
}

// RegisteredResources is administrator-owned Deployment metadata, never tenant
// MCPServer input. Reject malformed metadata rather than silently dropping it.
func RegisteredResources(raw, issuer string) (map[string][]string, error) {
	out := map[string][]string{}
	if raw == "" {
		return out, nil
	}
	if len(raw) > 32768 || json.Unmarshal([]byte(raw), &out) != nil || len(out) > 16 {
		return nil, errors.New("invalid administrator gateway resource registration")
	}
	i, err := httpsURL(issuer)
	if err != nil {
		return nil, errors.New("gateway registration requires an HTTPS issuer")
	}
	for resource, scopes := range out {
		u, err := httpsURL(resource)
		if err != nil || u.Host != i.Host || len(scopes) == 0 {
			return nil, errors.New("invalid administrator gateway resource registration")
		}
		for _, s := range scopes {
			if s == "" || strings.ContainsAny(s, " \t\r\n\"\\") {
				return nil, errors.New("invalid administrator gateway scope")
			}
		}
	}
	return out, nil
}
