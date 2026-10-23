// Local stdio MCP adapter. It obtains OAuth through the broker while the
// 1Password desktop CLI supplies the upstream Login during that sign-in.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"onepassword-credential-demo/internal/clientauth"
)

func run() error {
	var options clientauth.Options
	var stdio bool
	flag.StringVar(&options.Resource, "gateway", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp", "Exact MCP gateway URL")
	flag.StringVar(&options.Issuer, "issuer", "https://auth.mcpruntime.org/mcp-auth", "Expected broker issuer")
	flag.StringVar(&options.Mode, "credential-mode", "desktop", "desktop or browser")
	flag.StringVar(&options.Account, "account", "", "1Password account name or ID")
	flag.StringVar(&options.UsernameRef, "username-ref", "", "Dedicated test Login username op:// reference")
	flag.StringVar(&options.PasswordRef, "password-ref", "", "Dedicated test Login password op:// reference")
	flag.BoolVar(&options.ApproveConsent, "approve-consent", false, "Authorize this local adapter's tools:read OAuth consent")
	flag.BoolVar(&stdio, "stdio", false, "Serve the authenticated upstream tools to a local stdio MCP client")
	flag.Parse()
	options.ShowURL = func(u string) {
		fmt.Fprintln(os.Stderr, "Complete upstream sign-in:", u)
		if runtime.GOOS == "darwin" {
			_ = exec.Command("open", u).Start()
		}
	}
	oauth, closeOAuth, err := clientauth.New(options)
	if err != nil {
		return err
	}
	defer closeOAuth()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "onepassword-desktop-adapter", Version: "0.2.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: options.Resource, OAuthHandler: oauth}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	if !stdio {
		fmt.Println("Authenticated upstream Login and received the MCP OAuth token. Use --stdio to serve the upstream tools.")
		return nil
	}
	tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "onepassword-desktop-adapter", Version: "0.2.0"}, nil)
	for _, tool := range tools.Tools {
		name := tool.Name
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
		})
	}
	return server.Run(context.Background(), &mcp.StdioTransport{})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
