package server

// discoverToolsFromServer calls tools/list on a running MCP server and returns
// the discovered tool names. It probes the modern stateless protocol first,
// then uses the legacy initialize handshake only when the endpoint responds
// with an unrecognized HTTP 400 (the Streamable HTTP compatibility signal).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// decodeMCPResponse reads an HTTP response that is either plain JSON or
// SSE (text/event-stream) and returns the first JSON-RPC message found.
// It uses a 1 MiB per-line scanner buffer to safely handle large tool lists.
func decodeMCPResponse(resp *http.Response) (*mcpResponse, error) {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read SSE body: %w", err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimRight(line, "\r")
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var r mcpResponse
			if err := json.Unmarshal([]byte(payload), &r); err == nil {
				return &r, nil
			}
		}
		return nil, fmt.Errorf("no JSON-RPC message found in SSE stream")
	}
	// Plain JSON (or unknown — try JSON first)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r mcpResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%w (body prefix: %.40s)", err, string(b))
	}
	return &r, nil
}

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

const (
	modernProtocolVersion = "2026-07-28"
	legacyProtocolVersion = "2025-11-25"
)

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

type mcpInitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type mcpDiscoverResult struct {
	SupportedVersions []string `json:"supportedVersions"`
}

type mcpToolsListResult struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

// normalizeMCPURL ensures the URL has an explicit path. If the parsed URL has
// no path (or just "/"), "/mcp" is appended — the default endpoint used by the
// go-sdk and the oauth-example-go-2025-11-25 example server.
func normalizeMCPURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q: must include scheme and host (e.g. http://localhost:8088)", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	return u.String(), nil
}

// DiscoverToolsFromServer connects to a running MCP server at serverURL and
// returns the tool names. They are returned as bare names; callers wrap them
// into --tool flags or metadata.ToolConfig values.
//
// If the URL has no explicit path, /mcp is appended automatically (the default
// MCP endpoint path used by the go-sdk).
func DiscoverToolsFromServer(serverURL string) ([]string, error) {
	return discoverToolsFromServer(serverURL, &http.Client{Timeout: 10 * time.Second})
}

func discoverToolsFromServer(serverURL string, client *http.Client) ([]string, error) {
	endpoint, err := normalizeMCPURL(serverURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	post := func(req mcpRequest, protocolVersion string, methodHeader bool, sessionID string) (*mcpResponse, int, string, error) {
		body, _ := json.Marshal(req)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		// MCP 2025-06-18 requires both application/json and text/event-stream.
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		if protocolVersion != "" {
			httpReq.Header.Set("MCP-Protocol-Version", protocolVersion)
		}
		if methodHeader {
			httpReq.Header.Set("Mcp-Method", req.Method)
		}
		if sessionID != "" {
			httpReq.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, 0, "", fmt.Errorf("request to %s failed: %w", endpoint, err)
		}
		defer resp.Body.Close()
		responseSessionID := resp.Header.Get("Mcp-Session-Id")
		mcpResp, err := decodeMCPResponse(resp)
		if err != nil {
			return nil, resp.StatusCode, responseSessionID, fmt.Errorf("decode response: %w", err)
		}
		return mcpResp, resp.StatusCode, responseSessionID, nil
	}

	modernMeta := func() map[string]any {
		return map[string]any{
			"io.modelcontextprotocol/protocolVersion":    modernProtocolVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "mcp-runtime-discover", "version": "1.0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	modernRequest := func(id int, method string) mcpRequest {
		return mcpRequest{JSONRPC: "2.0", ID: id, Method: method, Params: map[string]any{"_meta": modernMeta()}}
	}

	// MCP 2026-07-28 is stateless: every request carries per-request metadata
	// and the matching transport headers. A conforming modern server advertises
	// the versions it supports in server/discover.
	discoverResp, discoverStatus, _, discoverErr := post(modernRequest(1, "server/discover"), modernProtocolVersion, true, "")
	modern := false
	if discoverErr == nil && discoverStatus >= 200 && discoverStatus < 300 && discoverResp.Error == nil {
		var discovered mcpDiscoverResult
		if err := json.Unmarshal(discoverResp.Result, &discovered); err != nil {
			return nil, fmt.Errorf("server/discover result: %w", err)
		}
		for _, version := range discovered.SupportedVersions {
			if version == modernProtocolVersion {
				modern = true
				break
			}
		}
		if !modern {
			return nil, fmt.Errorf("server/discover did not advertise supported protocol version %s", modernProtocolVersion)
		}
	} else if discoverStatus != http.StatusBadRequest || hasRecognizedModernProtocolError(discoverResp) {
		if discoverErr != nil {
			return nil, fmt.Errorf("server/discover: %w", discoverErr)
		}
		if discoverResp != nil && discoverResp.Error != nil {
			return nil, fmt.Errorf("server/discover error: %s", discoverResp.Error.Message)
		}
		return nil, fmt.Errorf("server/discover returned HTTP %d", discoverStatus)
	}

	var toolsResp *mcpResponse
	if modern {
		var status int
		toolsResp, status, _, err = post(modernRequest(2, "tools/list"), modernProtocolVersion, true, "")
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("tools/list returned HTTP %d", status)
		}
	} else {
		toolsResp, err = discoverLegacyTools(post)
		if err != nil {
			return nil, err
		}
	}

	if toolsResp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", toolsResp.Error.Message)
	}
	var result mcpToolsListResult
	if err := json.Unmarshal(toolsResp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}

	seen := make(map[string]struct{}, len(result.Tools))
	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func hasRecognizedModernProtocolError(resp *mcpResponse) bool {
	if resp == nil || resp.Error == nil {
		return false
	}
	return resp.Error.Code == -32020 || resp.Error.Code == -32022
}

type mcpPostFunc func(mcpRequest, string, bool, string) (*mcpResponse, int, string, error)

func discoverLegacyTools(post mcpPostFunc) (*mcpResponse, error) {
	initResp, status, sessionID, err := post(mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": legacyProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "mcp-runtime-discover", "version": "1.0"},
		},
	}, legacyProtocolVersion, false, "")
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("initialize returned HTTP %d", status)
	}
	if initResp.Error != nil {
		return nil, fmt.Errorf("initialize error: %s", initResp.Error.Message)
	}
	var initialized mcpInitializeResult
	if err := json.Unmarshal(initResp.Result, &initialized); err != nil {
		return nil, fmt.Errorf("parse initialize result: %w", err)
	}
	protocolVersion := initialized.ProtocolVersion
	if protocolVersion == "" {
		protocolVersion = legacyProtocolVersion
	}

	// Step 2: notifications/initialized (no response body expected)
	_, _, _, _ = post(mcpRequest{ //nolint:errcheck
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}, protocolVersion, false, sessionID)

	// Step 3: tools/list
	toolsResp, status, _, err := post(mcpRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]any{},
	}, protocolVersion, false, sessionID)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("tools/list returned HTTP %d", status)
	}
	if toolsResp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", toolsResp.Error.Message)
	}
	return toolsResp, nil
}
