// This upstream MCP server knows only Basic authentication. Credential lookup,
// OAuth login and token issuance belong to mcp-auth's optional gateway bundle.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Public test fixtures: use only a dedicated test Login item.
const username, password = "demo-user", "demo-password"

func newHandler(path string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "vault-header-demo", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "upstream_status", Description: "Confirm upstream header authentication."}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "upstream authenticated"}}}, nil, nil
	})
	type echoArgs struct {
		Message string `json:"message"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo an authenticated message."}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Message}}}, nil, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(204)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(username)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="demo-upstream"`)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == path+"/auth/check" && r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
}

func run() error {
	path := os.Getenv("MCP_PATH")
	if path == "" {
		path = "/mcp"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") || strings.HasSuffix(path, "/") {
		return errors.New("MCP_PATH must be an absolute path without trailing slash")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8088"
	}
	server := &http.Server{Addr: net.JoinHostPort(os.Getenv("LISTEN_HOST"), port), Handler: newHandler(path), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	return server.ListenAndServe()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
