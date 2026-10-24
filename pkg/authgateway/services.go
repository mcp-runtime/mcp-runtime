package authgateway

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"mcp-runtime/pkg/credentialheaders"
)

// DefaultScope is the OAuth scope a derived gateway service requires. It is
// protocol plumbing: the person never chooses it.
const DefaultScope = "tools:read"

// ClientKeyFile is where derived services read their resource-client key. The
// administrator supplies it with --secret-file client.pem=<file>.
const ClientKeyFile = MountPath + "/client.pem"

var serviceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,26}$`)

// ServerInput is the part of a header-mode MCPServer the gateway needs.
type ServerInput struct {
	Name           string
	Namespace      string
	IngressHost    string
	IngressPath    string
	Headers        []string
	ValidationPath string
	SecretHeaders  []string
}

// FormField is one sign-in input of a derived service.
type FormField struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret,omitempty"`
}

// HeaderRule renders one collected value into an upstream header.
type HeaderRule struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Field string `json:"field"`
}

// ResourceClient is the gateway's identity at mcp-auth for one service.
type ResourceClient struct {
	ClientID       string `json:"client_id"`
	KeyID          string `json:"key_id"`
	PrivateKeyFile string `json:"private_key_file"`
}

// platformMCPHost is the only host the gateway will call. It comes from the
// operator (--mcp-host), never from the server spec on its own.
func platformMCPHost(publicHost string) (string, error) {
	host := strings.TrimSpace(publicHost)
	if host == "" || strings.ContainsAny(host, "/:?#@ ") {
		return "", errors.New("platform MCP host is required (--mcp-host) and must be a hostname")
	}
	return host, nil
}

// DerivedService is a complete gateway service entry derived from a pushed MCPServer.
type DerivedService struct {
	ID             string         `json:"id"`
	ServerName     string         `json:"server_name,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	ResourceURL    string         `json:"resource_url"`
	UpstreamURL    string         `json:"upstream_url"`
	ValidationURL  string         `json:"validation_url"`
	RequiredScopes []string       `json:"required_scopes"`
	ResourceClient ResourceClient `json:"resource_client"`
	Source         string         `json:"source"`
	FormFields     []FormField    `json:"form_fields"`
	Headers        []HeaderRule   `json:"headers"`
}

// ServiceFor derives the gateway service for one server. The resource lives on
// authHost at the server's own MCP path. The upstream host is only publicHost,
// the platform MCP host from --mcp-host. A server ingressHost may repeat that
// host and is rejected when it names anywhere else, so a publisher cannot point
// the sign-in page at an address they choose.
func ServiceFor(in ServerInput, authHost, publicHost string) (DerivedService, error) {
	where := fmt.Sprintf("server %s/%s", in.Namespace, in.Name)
	id := strings.ToLower(in.Name)
	if !serviceIDPattern.MatchString(id) {
		return DerivedService{}, fmt.Errorf("%s: name cannot be a gateway service id (lowercase letters, digits and '-', at most 27)", where)
	}
	host, err := platformMCPHost(publicHost)
	if err != nil {
		return DerivedService{}, fmt.Errorf("%s: %w", where, err)
	}
	if declared := strings.TrimSpace(in.IngressHost); declared != "" {
		if strings.ContainsAny(declared, "/:?#@ ") || !strings.EqualFold(declared, host) {
			return DerivedService{}, fmt.Errorf("%s: ingressHost %q is not the platform MCP host %q", where, declared, host)
		}
	}
	if !strings.HasPrefix(in.IngressPath, "/") || strings.HasSuffix(in.IngressPath, "/") || strings.Contains(in.IngressPath, "//") {
		return DerivedService{}, fmt.Errorf("%s: ingressPath must be an absolute path without a trailing slash", where)
	}
	if err := credentialheaders.ValidateGateway(in.ValidationPath, in.SecretHeaders, in.Headers); err != nil {
		return DerivedService{}, fmt.Errorf("%s: %w", where, err)
	}
	if strings.TrimSpace(authHost) == "" {
		return DerivedService{}, errors.New("auth host is required")
	}
	upstream := "https://" + host + in.IngressPath
	service := DerivedService{
		ID: id, ServerName: in.Name, Namespace: in.Namespace,
		ResourceURL: "https://" + authHost + in.IngressPath, UpstreamURL: upstream, ValidationURL: upstream + in.ValidationPath,
		RequiredScopes: []string{DefaultScope},
		ResourceClient: ResourceClient{ClientID: "gateway-" + id, KeyID: "k1", PrivateKeyFile: ClientKeyFile},
		Source:         SourceForm,
	}
	for i, header := range in.Headers {
		field := credentialheaders.GatewayFieldName(i)
		service.FormFields = append(service.FormFields, FormField{Name: field, Label: header, Secret: credentialheaders.IsSecretHeader(header, in.SecretHeaders)})
		service.Headers = append(service.Headers, HeaderRule{Name: header, Type: "direct", Field: field})
	}
	return service, nil
}
