package authgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// LoginClientID is the OAuth client id mcp-auth presents to a service's sign-in
// endpoints. The gateway uses the same convention.
func LoginClientID(serviceID string) string { return "login-" + serviceID }

// SubjectPrefix is what mcp-auth puts before the opaque subject a service's
// sign-in returns. The gateway stores values under the prefixed subject.
func SubjectPrefix(serviceID string) string { return "form-" + serviceID + ":" }

// LoginMethodID names a service's resource login in mcp-auth.
func LoginMethodID(serviceID string) string { return "form-" + serviceID }

// ConnectorName names the plain OAuth connector entry for a service. The prefix
// marks entries Runtime owns, so a merge never touches the installation's own
// connectors.
func ConnectorName(serviceID string) string { return "gateway-login-" + serviceID }

// ConnectorPrefix is the prefix of every connector entry Runtime owns.
const ConnectorPrefix = "gateway-login-"

// PublicKeyFile is where mcp-auth finds a service's assertion-client public key.
func PublicKeyFile(serviceID string) string {
	return WiringMountPath + "/" + serviceID + ".public.pem"
}

// ExternalCredentialsFile and ResourceLoginsFile are the wiring files' paths as
// mcp-auth sees them.
const (
	ExternalCredentialsFile = WiringMountPath + "/external-credentials.json"
	ResourceLoginsFile      = WiringMountPath + "/resource-logins.json"
)

// Wiring is everything mcp-auth must know about the gateway's services.
type Wiring struct {
	// Files are the keys of the wiring ConfigMap.
	Files map[string]string
	// Connectors are the connector entries to merge into mcp-auth's connectors.
	Connectors map[string]json.RawMessage
}

// BuildWiring derives the mcp-auth side of the setup. publicKeys maps a service
// id to the PEM public half of its resource-client key.
func BuildWiring(cfg *Config, publicKeys map[string]string) (*Wiring, error) {
	issuer, err := url.Parse(cfg.AuthorizationServer.Issuer)
	if err != nil || issuer.Host == "" {
		return nil, errors.New("gateway issuer is required")
	}
	services := append([]Service(nil), cfg.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })

	callback := strings.TrimRight(cfg.AuthorizationServer.Issuer, "/") + "/identity/callback"
	w := &Wiring{Files: map[string]string{}, Connectors: map[string]json.RawMessage{}}
	external := make([]map[string]any, 0, len(services))
	methods := make([]map[string]any, 0, len(services))
	for _, s := range services {
		pub := strings.TrimSpace(publicKeys[s.ID])
		if !strings.Contains(pub, "BEGIN PUBLIC KEY") {
			return nil, fmt.Errorf("service %q has no resource-client public key", s.ID)
		}
		w.Files[s.ID+".public.pem"] = pub + "\n"
		external = append(external, map[string]any{
			"id": s.ID, "resource": s.ResourceURL, "required_scopes": s.RequiredScopes,
			"resource_client": map[string]any{"client_id": s.ResourceClient.ClientID, "public_key_file": PublicKeyFile(s.ID)},
		})
		login := strings.TrimRight(s.ResourceURL, "/") + "/login"
		connector, _ := json.Marshal(map[string]any{
			"issuer": login, "authorization_endpoint": login + "/authorize", "token_endpoint": login + "/token", "userinfo_endpoint": login + "/userinfo",
			"client_id": LoginClientID(s.ID), "exchange_client_id": "unused-in-login-registry-mode", "token_endpoint_auth_method": "none",
			"scopes": []string{"profile"}, "identity_claims": []string{"sub"}, "allowed_upstream_callback_uris": []string{callback},
		})
		w.Connectors[ConnectorName(s.ID)] = connector
		methods = append(methods, map[string]any{
			"id": LoginMethodID(s.ID), "type": "oidc", "connector": ConnectorName(s.ID),
			"subject_prefix": SubjectPrefix(s.ID), "resources": []string{s.ResourceURL},
		})
	}
	externalJSON, _ := json.MarshalIndent(external, "", "  ")
	methodsJSON, _ := json.MarshalIndent(map[string]any{"methods": methods}, "", "  ")
	w.Files["external-credentials.json"] = string(externalJSON) + "\n"
	w.Files["resource-logins.json"] = string(methodsJSON) + "\n"
	return w, nil
}

// MergeConnectors returns the installation's connectors with the gateway's
// entries applied: owned entries are added or replaced, owned entries for
// services that no longer exist are dropped, and every other entry is kept as
// written.
func MergeConnectors(existing string, owned map[string]json.RawMessage) (string, error) {
	connectors := map[string]json.RawMessage{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &connectors); err != nil {
			return "", errors.New("the installed connectors file is not a JSON object")
		}
	}
	for name := range connectors {
		if strings.HasPrefix(name, ConnectorPrefix) {
			delete(connectors, name)
		}
	}
	for name, entry := range owned {
		if _, clash := connectors[name]; clash {
			return "", fmt.Errorf("connector %q already exists and is not owned by the gateway", name)
		}
		connectors[name] = entry
	}
	out, err := json.MarshalIndent(connectors, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}
