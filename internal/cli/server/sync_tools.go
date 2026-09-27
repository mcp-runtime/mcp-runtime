package server

// discoverToolsFromServer calls tools/list on a running MCP server and returns
// the discovered tool names. It performs a minimal initialize →
// notifications/initialized → tools/list handshake so any MCP-compliant server
// will respond correctly.

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

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
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
	endpoint, err := normalizeMCPURL(serverURL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	post := func(req mcpRequest) (*mcpResponse, string, error) {
		body, _ := json.Marshal(req)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		// MCP 2025-06-18 requires both application/json and text/event-stream.
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, "", fmt.Errorf("request to %s failed: %w", endpoint, err)
		}
		defer resp.Body.Close()
		sessionID := resp.Header.Get("Mcp-Session-Id")
		mcpResp, err := decodeMCPResponse(resp)
		if err != nil {
			return nil, sessionID, fmt.Errorf("decode response: %w", err)
		}
		return mcpResp, sessionID, nil
	}

	postWithSession := func(sessionID string, req mcpRequest) (*mcpResponse, error) {
		body, _ := json.Marshal(req)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			httpReq.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("request to %s failed: %w", endpoint, err)
		}
		defer resp.Body.Close()
		return decodeMCPResponse(resp)
	}

	// Step 1: initialize
	initResp, sessionID, err := post(mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "mcp-runtime-discover", "version": "1.0"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if initResp.Error != nil {
		return nil, fmt.Errorf("initialize error: %s", initResp.Error.Message)
	}

	// Step 2: notifications/initialized (no response body expected)
	_, _ = postWithSession(sessionID, mcpRequest{ //nolint:errcheck
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Step 3: tools/list
	toolsResp, err := postWithSession(sessionID, mcpRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]any{},
	})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
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
