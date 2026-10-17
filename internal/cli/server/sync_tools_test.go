package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDiscoverToolsFromServerUsesModernProtocol(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var req mcpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("MCP-Protocol-Version") != modernProtocolVersion {
			t.Errorf("MCP-Protocol-Version = %q", r.Header.Get("MCP-Protocol-Version"))
		}
		if r.Header.Get("Mcp-Method") != req.Method {
			t.Errorf("Mcp-Method = %q, want %q", r.Header.Get("Mcp-Method"), req.Method)
		}
		params, _ := req.Params.(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if meta["io.modelcontextprotocol/protocolVersion"] != modernProtocolVersion {
			t.Errorf("request metadata version = %v", meta["io.modelcontextprotocol/protocolVersion"])
		}
		if req.Method == "server/discover" {
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2025-11-25","2026-07-28"]}}`), nil
		}
		if req.Method != "tools/list" {
			t.Errorf("method = %q", req.Method)
		}
		return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"lookup"}]}}`), nil
	})}

	got, err := discoverToolsFromServer("http://mcp.example/mcp", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "lookup" {
		t.Fatalf("tools = %v, want [lookup]", got)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want modern discovery and tools/list", requests)
	}
}

func TestDiscoverToolsFromServerFallsBackOnUnrecognizedHTTP400(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		var req mcpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch req.Method {
		case "server/discover":
			return jsonResponse(http.StatusBadRequest, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`), nil
		case "initialize":
			if r.Header.Get("MCP-Protocol-Version") != legacyProtocolVersion {
				t.Errorf("initialize header = %q", r.Header.Get("MCP-Protocol-Version"))
			}
			params, _ := req.Params.(map[string]any)
			if params["protocolVersion"] != legacyProtocolVersion {
				t.Errorf("initialize body version = %v", params["protocolVersion"])
			}
			response := jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`)
			response.Header.Set("Mcp-Session-Id", "legacy-session")
			return response, nil
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "legacy-session" || r.Header.Get("MCP-Protocol-Version") != legacyProtocolVersion {
				t.Errorf("initialized headers = session %q, version %q", r.Header.Get("Mcp-Session-Id"), r.Header.Get("MCP-Protocol-Version"))
			}
			return jsonResponse(http.StatusAccepted, ""), nil
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "legacy-session" || r.Header.Get("MCP-Protocol-Version") != legacyProtocolVersion {
				t.Errorf("tools/list headers = session %q, version %q", r.Header.Get("Mcp-Session-Id"), r.Header.Get("MCP-Protocol-Version"))
			}
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"legacy_tool"}]}}`), nil
		default:
			t.Errorf("unexpected method %q", req.Method)
			return jsonResponse(http.StatusInternalServerError, "unexpected request"), nil
		}
	})}

	got, err := discoverToolsFromServer("http://mcp.example/mcp", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "legacy_tool" {
		t.Fatalf("tools = %v, want [legacy_tool]", got)
	}
	if requests != 4 {
		t.Fatalf("requests = %d, want probe plus legacy handshake", requests)
	}
}

func TestDiscoverToolsFromServerDoesNotDowngradeRecognizedModernError(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return jsonResponse(http.StatusBadRequest, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2025-11-25"],"requested":"2026-07-28"}}}`), nil
	})}

	_, err := discoverToolsFromServer("http://mcp.example/mcp", client)
	if err == nil || !strings.Contains(err.Error(), "Unsupported protocol version") {
		t.Fatalf("error = %v, want recognized modern protocol error", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want no legacy fallback", requests)
	}
}
