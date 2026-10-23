package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConnectorPinsCredentialDestination(t *testing.T) {
	c := connector{gateway: "https://mcp.example/demo/mcp"}
	ticket := strings.Repeat("a", 43)
	for _, target := range []string{
		"https://evil.example/demo/mcp/connect?ticket=" + ticket,
		"http://mcp.example/demo/mcp/connect?ticket=" + ticket,
		"https://mcp.example/other/mcp/connect?ticket=" + ticket,
		"https://user:pass@mcp.example/demo/mcp/connect?ticket=" + ticket,
		"https://mcp.example/demo/mcp/connect?ticket=" + ticket + "&extra=x",
		"https://mcp.example/demo/mcp/connect?ticket=" + ticket + "#fragment",
	} {
		if _, err := c.validateTarget(target); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
	if _, err := c.validateTarget("https://mcp.example/demo/mcp/connect?ticket=" + ticket); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorStdio(t *testing.T) {
	if os.Getenv("ONEPASSWORD_DEMO_STDIO_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("connector", flag.ContinueOnError)
		os.Args = []string{"connector", "--stdio", "--gateway", "https://demo.example/mcp", "--username-ref", "op://Test/Demo/username", "--password-ref", "op://Test/Demo/password"}
		if err := run(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.Command(os.Args[0], "-test.run=^TestConnectorStdio$")
	command.Env = append(os.Environ(), "ONEPASSWORD_DEMO_STDIO_HELPER=1")
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "connect_from_1password" {
		t.Fatal("missing local connection tool")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "connect_from_1password", Arguments: map[string]any{"connect_url": "https://wrong.example/mcp/connect?ticket=" + strings.Repeat("a", 43)}})
	if err != nil || !result.IsError {
		t.Fatal("local MCP tool did not reject an unapproved destination")
	}
}
