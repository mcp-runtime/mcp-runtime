// Runtime server, tool, and policy calls against the platform API.

package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/controlplane"
)

// ServerListItem is one row from the platform API runtime servers list.
type ServerListItem struct {
	Name               string                           `json:"name"`
	Namespace          string                           `json:"namespace"`
	UID                string                           `json:"uid,omitempty"`
	TeamID             string                           `json:"team_id,omitempty"`
	Image              string                           `json:"image,omitempty"`
	ImageTag           string                           `json:"imageTag,omitempty"`
	Description        string                           `json:"description,omitempty"`
	Ready              string                           `json:"ready"`
	Status             string                           `json:"status"`
	Message            string                           `json:"message,omitempty"`
	Conditions         []metav1.Condition               `json:"conditions,omitempty"`
	Generation         int64                            `json:"generation,omitempty"`
	Labels             map[string]string                `json:"labels"`
	Age                string                           `json:"age"`
	Endpoint           string                           `json:"endpoint,omitempty"`
	AuthMode           string                           `json:"authMode,omitempty"`
	AuthHeaders        []string                         `json:"authHeaders,omitempty"`
	CredentialPresence string                           `json:"credentialPresence,omitempty"`
	GatewayEnabled     bool                             `json:"gatewayEnabled"`
	ServicePort        int32                            `json:"servicePort,omitempty"`
	Tools              []ToolConfig                     `json:"tools,omitempty"`
	Prompts            []mcpv1alpha1.InventoryItem      `json:"prompts,omitempty"`
	Resources          []mcpv1alpha1.InventoryItem      `json:"resources,omitempty"`
	Tasks              []mcpv1alpha1.InventoryItem      `json:"tasks,omitempty"`
	LiveInventory      *LiveInventory                   `json:"liveInventory,omitempty"`
	LiveInventoryError string                           `json:"liveInventoryError,omitempty"`
	AccessJSON         map[string]any                   `json:"access_json,omitempty"`
	RuntimeStatus      controlplane.ServerRuntimeStatus `json:"runtimeStatus"`
}

type LivePromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// LiveInventory is the latest tools/list, prompts/list and resources/list
// result observed by the platform runtime API. It is cached and may be absent
// until the first probe completes.
type LiveInventory struct {
	FetchedAt       string         `json:"fetchedAt"`
	ProtocolVersion string         `json:"protocolVersion,omitempty"`
	Tools           []LiveTool     `json:"tools"`
	Prompts         []LivePrompt   `json:"prompts"`
	Resources       []LiveResource `json:"resources"`
}

type LiveTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type LivePrompt struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Arguments   []LivePromptArgument `json:"arguments,omitempty"`
}

type LiveResource struct {
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type serverListResponse struct {
	Servers []ServerListItem `json:"servers"`
}

type runtimeServerItemResponse struct {
	Server ServerListItem `json:"server"`
}

type ToolConfig struct {
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	RequiredTrust string            `json:"requiredTrust,omitempty"`
	SideEffect    string            `json:"sideEffect,omitempty"`
	RiskLevel     string            `json:"riskLevel,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type RuntimeToolRow struct {
	ToolName      string            `json:"tool_name"`
	Description   string            `json:"description,omitempty"`
	ServerName    string            `json:"server_name"`
	Namespace     string            `json:"namespace"`
	TeamID        string            `json:"team_id,omitempty"`
	EndpointURL   string            `json:"endpoint_url,omitempty"`
	Declared      bool              `json:"declared"`
	Live          bool              `json:"live"`
	DriftStatus   string            `json:"drift_status"`
	RequiredTrust string            `json:"required_trust,omitempty"`
	SideEffect    string            `json:"side_effect,omitempty"`
	RiskLevel     string            `json:"risk_level,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	ConnectConfig map[string]any    `json:"connect_config,omitempty"`
}

type runtimeToolsResponse struct {
	Tools []RuntimeToolRow `json:"tools"`
}

type runtimeServerApplyRequest struct {
	Name      string                    `json:"name"`
	Namespace string                    `json:"namespace,omitempty"`
	Scope     string                    `json:"scope,omitempty"`
	Update    bool                      `json:"update,omitempty"`
	Labels    map[string]string         `json:"labels,omitempty"`
	Spec      mcpv1alpha1.MCPServerSpec `json:"spec"`
}

type runtimeServerApplyResponse struct {
	Server ServerListItem `json:"server"`
}

func (c *PlatformClient) ListRuntimeServers(ctx context.Context, namespace string) ([]ServerListItem, error) {
	v := url.Values{}
	if strings.TrimSpace(namespace) != "" {
		v.Set("namespace", namespace)
	}
	resp, err := c.do(ctx, http.MethodGet, "/runtime/servers", v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	var out serverListResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// GetRuntimeServer returns one server with its runtime inventory and scoped
// last-session observation when the caller may inspect server activity.
func (c *PlatformClient) GetRuntimeServer(ctx context.Context, namespace, name string) (ServerListItem, error) {
	p := fmt.Sprintf("/runtime/servers/%s/%s", url.PathEscape(strings.TrimSpace(namespace)), url.PathEscape(strings.TrimSpace(name)))
	resp, err := c.do(ctx, http.MethodGet, p, "", nil)
	if err != nil {
		return ServerListItem{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return ServerListItem{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ServerListItem{}, httpAPIError(resp.StatusCode, b)
	}
	var out runtimeServerItemResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return ServerListItem{}, err
	}
	return out.Server, nil
}

func (c *PlatformClient) ListRuntimeTools(ctx context.Context, filters map[string]string) ([]RuntimeToolRow, error) {
	v := url.Values{}
	for key, value := range filters {
		if strings.TrimSpace(value) != "" {
			v.Set(key, strings.TrimSpace(value))
		}
	}
	resp, err := c.do(ctx, http.MethodGet, "/runtime/tools", v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	var out runtimeToolsResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (c *PlatformClient) ApplyRuntimeServer(ctx context.Context, name, namespace string, spec mcpv1alpha1.MCPServerSpec) (ServerListItem, error) {
	return c.ApplyRuntimeServerWithScope(ctx, name, namespace, "", spec)
}

func (c *PlatformClient) ApplyRuntimeServerWithScope(ctx context.Context, name, namespace, scope string, spec mcpv1alpha1.MCPServerSpec) (ServerListItem, error) {
	return c.ApplyRuntimeServerWithScopeUpdate(ctx, name, namespace, scope, spec, false)
}

func (c *PlatformClient) ApplyRuntimeServerWithScopeUpdate(ctx context.Context, name, namespace, scope string, spec mcpv1alpha1.MCPServerSpec, update bool) (ServerListItem, error) {
	body := runtimeServerApplyRequest{
		Name:      strings.TrimSpace(name),
		Namespace: strings.TrimSpace(namespace),
		Scope:     strings.TrimSpace(scope),
		Update:    update,
		Spec:      spec,
	}
	js, err := json.Marshal(body)
	if err != nil {
		return ServerListItem{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/runtime/servers", "", bytes.NewReader(js))
	if err != nil {
		return ServerListItem{}, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return ServerListItem{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ServerListItem{}, httpAPIError(resp.StatusCode, b)
	}
	var out runtimeServerApplyResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return ServerListItem{}, err
	}
	return out.Server, nil
}

func (c *PlatformClient) DeleteRuntimeServer(ctx context.Context, namespace, name string) error {
	p := fmt.Sprintf("/runtime/servers/%s/%s", url.PathEscape(strings.TrimSpace(namespace)), url.PathEscape(strings.TrimSpace(name)))
	resp, err := c.do(ctx, http.MethodDelete, p, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}

func (c *PlatformClient) GetRuntimePolicy(ctx context.Context, namespace, server string) ([]byte, error) {
	v := url.Values{}
	v.Set("namespace", namespace)
	v.Set("server", server)
	resp, err := c.do(ctx, http.MethodGet, "/runtime/policy", v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, b)
	}
	return b, nil
}
