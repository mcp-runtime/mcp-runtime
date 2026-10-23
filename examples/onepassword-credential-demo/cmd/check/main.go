// Interactive acceptance client. Credentials are supplied during OAuth login,
// not through MCP tools after initialization.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"onepassword-credential-demo/internal/clientauth"
)

func run() error {
	var options clientauth.Options
	flag.StringVar(&options.Resource, "url", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp", "MCP gateway URL")
	flag.StringVar(&options.Issuer, "issuer", "https://auth.mcpruntime.org/mcp-auth", "Expected broker issuer")
	flag.StringVar(&options.Mode, "credential-mode", "browser", "browser or desktop")
	flag.StringVar(&options.Account, "account", "", "1Password account name or ID")
	flag.StringVar(&options.UsernameRef, "username-ref", "", "Dedicated test Login username op:// reference")
	flag.StringVar(&options.PasswordRef, "password-ref", "", "Dedicated test Login password op:// reference")
	flag.BoolVar(&options.ApproveConsent, "approve-consent", false, "Approve this local test client's tools:read consent; required for desktop mode")
	flag.Parse()
	options.ShowURL = func(u string) { fmt.Println("AUTHORIZE_URL:", u) }
	oauth, closeOAuth, err := clientauth.New(options)
	if err != nil {
		return err
	}
	defer closeOAuth()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "credential-gateway-acceptance", Version: "0.2.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: options.Resource, OAuthHandler: oauth}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	fmt.Println("PASS: upstream login completed before MCP OAuth and initialize")
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	if len(tools.Tools) != 2 {
		return fmt.Errorf("expected two upstream tools, got %d", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Name == "connect_upstream" {
			return errors.New("post-login credential tool must not exist")
		}
	}
	for range 2 {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "upstream_status", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			return errors.New("upstream header authentication failed")
		}
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok && (!strings.Contains(text.Text, `"authenticated":true`) || strings.Contains(text.Text, "demo-password")) {
				return errors.New("invalid or credential-bearing upstream response")
			}
		}
	}
	fmt.Println("PASS: two upstream MCP calls used gateway-injected headers; client sent OAuth only")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
