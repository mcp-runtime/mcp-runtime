// Interactive acceptance client: OAuth and both connection paths, no saved tokens.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func run() error {
	var endpoint, paths string
	flag.StringVar(&endpoint, "url", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp", "Demo MCP resource URL")
	flag.StringVar(&paths, "paths", "browser,desktop", "Comma-separated connection paths to verify interactively")
	flag.Parse()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	callback := "http://" + listener.Addr().String() + "/callback"
	results := make(chan *auth.AuthorizationResult, 1)
	callbackServer := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("iss") != "https://auth.mcpruntime.org/mcp-auth" || r.URL.Query().Get("error") != "" {
			http.Error(w, "OAuth authorization failed", 400)
			return
		}
		result := &auth.AuthorizationResult{Code: r.URL.Query().Get("code"), State: r.URL.Query().Get("state")}
		if result.Code == "" || result.State == "" {
			http.Error(w, "Invalid OAuth response", 400)
			return
		}
		select {
		case results <- result:
			fmt.Fprint(w, "OAuth complete. Return to the demo acceptance client.")
		default:
			http.Error(w, "Authorization already received", 409)
		}
	})}
	go func() { _ = callbackServer.Serve(listener) }()
	defer callbackServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	oauth, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL:                     callback,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{callback}, ClientName: "1Password MCP demo acceptance", TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: "tools:read"}},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			fmt.Println("AUTHORIZE_URL:", args.URL)
			select {
			case result := <-results:
				return result, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, Client: &http.Client{Timeout: 15 * time.Second},
	})
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "onepassword-demo-acceptance", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, OAuthHandler: oauth}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	if len(tools.Tools) != 3 {
		return fmt.Errorf("expected 3 tools, got %d", len(tools.Tools))
	}
	fmt.Println("PASS: OAuth sign-in, MCP initialize, tools/list")
	call := func(name string) (*mcp.CallToolResult, error) {
		return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	}
	renew := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "renew" {
				select {
				case renew <- struct{}{}:
				default:
				}
			}
		}
	}()
	for _, path := range strings.Split(paths, ",") {
		if path != "browser" && path != "desktop" {
			return errors.New("paths must contain browser or desktop")
		}
		before, err := call("upstream_status")
		if err != nil || !before.IsError {
			return errors.New("unconnected upstream call was not refused")
		}
		result, err := call("connect_upstream")
		if err != nil || result.IsError {
			return errors.New("could not create session-bound connection link")
		}
		var connection struct {
			URL string `json:"connect_url"`
		}
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				_ = json.Unmarshal([]byte(text.Text), &connection)
			}
		}
		if connection.URL == "" {
			return errors.New("missing connection link")
		}
		fmt.Printf("CONNECT_%s_URL: %s\n", strings.ToUpper(path), connection.URL)
		fmt.Println("Waiting for this session's test credentials. Use the form for browser, or cmd/connect for desktop.")
		deadline := time.Now().Add(4 * time.Minute)
		connected := false
		for ctx.Err() == nil {
			result, err := call("upstream_status")
			if err != nil {
				return err
			}
			if !result.IsError {
				connected = true
				break
			}
			select {
			case <-time.After(2 * time.Second):
			case <-renew:
				deadline = time.Time{}
			case <-ctx.Done():
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				result, err = call("connect_upstream")
				if err != nil || result.IsError {
					return errors.New("could not renew connection link")
				}
				for _, content := range result.Content {
					if text, ok := content.(*mcp.TextContent); ok {
						_ = json.Unmarshal([]byte(text.Text), &connection)
					}
				}
				fmt.Printf("CONNECT_%s_URL: %s\n", strings.ToUpper(path), connection.URL)
				deadline = time.Now().Add(4 * time.Minute)
			}
		}
		if !connected {
			return errors.New("connection not completed within five minutes; rerun to issue a fresh link")
		}
		result, err = call("upstream_status")
		if err != nil || result.IsError {
			return errors.New("second upstream call failed")
		}
		fmt.Printf("PASS: %s connection and two upstream calls with cached credentials\n", path)
		result, err = call("disconnect_upstream")
		if err != nil || result.IsError {
			return errors.New("disconnect failed")
		}
		result, err = call("upstream_status")
		if err != nil || !result.IsError {
			return errors.New("disconnected session could still call upstream")
		}
		fmt.Printf("PASS: %s disconnect immediately denied the next upstream call\n", path)
	}
	fmt.Println("PASS: all requested hosted checks complete; credentials discarded")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
