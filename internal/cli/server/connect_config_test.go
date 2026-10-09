package server

import (
	"testing"

	"mcp-runtime/internal/cli/platformapi"
)

func TestBuildConnectConfigKeepsHeaderAuthNames(t *testing.T) {
	config, err := BuildConnectConfig(platformapi.ServerListItem{
		Name:     "example",
		Endpoint: "https://mcp.example.com/example/mcp",
		AccessJSON: map[string]any{
			"mcpServers": map[string]any{
				"example": map[string]any{
					"type": "http",
					"url":  "https://mcp.example.com/example/mcp",
					"headers": map[string]any{
						"X-Example-Credential": "",
						"Private-Token":        "",
					},
				},
			},
		},
	}, "cursor")
	if err != nil {
		t.Fatalf("BuildConnectConfig: %v", err)
	}
	servers := config["mcpServers"].(map[string]any)
	entry := servers["example"].(map[string]any)
	headers := entry["headers"].(map[string]any)
	if headers["X-Example-Credential"] != "" || headers["Private-Token"] != "" {
		t.Fatalf("headers = %#v", headers)
	}

	vscode, err := BuildConnectConfig(platformapi.ServerListItem{
		Name: "example",
		AccessJSON: map[string]any{
			"mcpServers": map[string]any{
				"example": map[string]any{
					"type": "http",
					"url":  "https://mcp.example.com/example/mcp",
					"headers": map[string]any{
						"Authorization": "",
					},
				},
			},
		},
	}, "vscode")
	if err != nil {
		t.Fatalf("vscode config: %v", err)
	}
	wrapped := vscode["servers"].(map[string]any)
	if _, ok := wrapped["example"].(map[string]any)["headers"]; !ok {
		t.Fatalf("vscode entry = %#v", wrapped["example"])
	}
}

func TestBuildConnectConfigOmitsHeadersWithoutHeaderAuth(t *testing.T) {
	config, err := BuildConnectConfig(platformapi.ServerListItem{
		Name:     "example",
		Endpoint: "https://mcp.example.com/example/mcp",
	}, "claude")
	if err != nil {
		t.Fatalf("BuildConnectConfig: %v", err)
	}
	servers := config["mcpServers"].(map[string]any)
	entry := servers["example"].(map[string]any)
	if _, ok := entry["headers"]; ok {
		t.Fatalf("headers = %#v, want none", entry["headers"])
	}
}
