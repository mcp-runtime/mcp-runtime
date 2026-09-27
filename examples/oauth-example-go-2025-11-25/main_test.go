package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuthHTTPSessionBindsSubjectAndUsesCurrentClaims(t *testing.T) {
	protocolHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return newMCPServer()
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	boundHandler := bindVerifiedIdentity(protocolHandler)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model the trusted context produced by mcp-auth JWT verification.
		claims := mcpauth.TokenClaims{
			Subject: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			Scopes:  map[string]bool{"tools:read": true},
			Raw:     map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "azp": r.Header.Get("X-Test-Agent")},
		}
		boundHandler.ServeHTTP(w, r.WithContext(mcpauth.WithTokenClaims(r.Context(), claims)))
	}))
	defer endpoint.Close()
	post := func(subject, agent, session, payload string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, endpoint.URL, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+subject)
		req.Header.Set("X-Test-Agent", agent)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := endpoint.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), string(body)
	}
	status, session, body := post("alice", "agent-a", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if status != http.StatusOK || session == "" {
		t.Fatalf("initialize: status=%d session=%q body=%s", status, session, body)
	}
	post("alice", "agent-a", session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	status, _, body = post("alice", "agent-b", session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`)
	if status != http.StatusOK || !strings.Contains(body, "agent-b") || strings.Contains(body, "agent-a") {
		t.Fatalf("whoami must use current request claims: status=%d body=%s", status, body)
	}
	status, _, body = post("bob", "agent-b", session, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	if status != http.StatusForbidden {
		t.Fatalf("different subject reused session: status=%d body=%s", status, body)
	}
}

func TestNewMCPServerExposesSmokeSurface(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := newMCPServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, want := range []string{"aaa-ping", "echo", "add", "upper", "lower", "slugify", "create_task", "draft_release_note"} {
		if !hasTool(tools.Tools, want) {
			t.Fatalf("tools/list missing %s: %#v", want, tools.Tools)
		}
	}

	prompts, err := clientSession.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	for _, want := range []string{"hello", "summarize", "task_brief", "handoff_note"} {
		if !hasPrompt(prompts.Prompts, want) {
			t.Fatalf("prompts/list missing %s: %#v", want, prompts.Prompts)
		}
	}

	resources, err := clientSession.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	for _, want := range []string{"embedded:readme", "embedded:task-guide", "embedded:workspace-playbook"} {
		if !hasResource(resources.Resources, want) {
			t.Fatalf("resources/list missing %s: %#v", want, resources.Resources)
		}
	}
}

func TestSmokeSurfaceHandlers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := newMCPServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	callRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "aaa-ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call tool aaa-ping: %v", err)
	}
	if got := firstText(callRes.Content); got != "pong" {
		t.Fatalf("aaa-ping returned %q, want pong", got)
	}

	callRes, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "upper",
		Arguments: map[string]any{"message": "governance"},
	})
	if err != nil {
		t.Fatalf("call tool upper: %v", err)
	}
	if got := firstText(callRes.Content); got != "GOVERNANCE" {
		t.Fatalf("upper returned %q, want GOVERNANCE", got)
	}

	callRes, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{"title": "Test adapter flow", "priority": "high", "owner": "ide"},
	})
	if err != nil {
		t.Fatalf("call tool create_task: %v", err)
	}
	if got := firstText(callRes.Content); got != "task: Test adapter flow\npriority: high\nowner: ide\nstatus: open" {
		t.Fatalf("create_task returned %q", got)
	}

	callRes, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "draft_release_note",
		Arguments: map[string]any{
			"title":  "Adapter session flow",
			"change": "Added issued session reuse",
			"impact": "Agents can reconnect without manual YAML edits",
		},
	})
	if err != nil {
		t.Fatalf("call tool draft_release_note: %v", err)
	}
	if got := firstText(callRes.Content); got != "release: Adapter session flow\nchange: Added issued session reuse\nimpact: Agents can reconnect without manual YAML edits\nstatus: draft" {
		t.Fatalf("draft_release_note returned %q", got)
	}

	readRes, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: "embedded:readme"})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(readRes.Contents) != 1 || readRes.Contents[0].Text == "" {
		t.Fatalf("unexpected resource contents: %#v", readRes.Contents)
	}

	promptRes, err := clientSession.GetPrompt(ctx, &mcp.GetPromptParams{Name: "hello"})
	if err != nil {
		t.Fatalf("get prompt hello: %v", err)
	}
	if len(promptRes.Messages) != 1 {
		t.Fatalf("hello prompt messages = %d, want 1", len(promptRes.Messages))
	}
	if got := firstText([]mcp.Content{promptRes.Messages[0].Content}); got != "Hello from the Workspace assistant MCP server." {
		t.Fatalf("hello prompt returned %q", got)
	}

	promptRes, err = clientSession.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "task_brief",
		Arguments: map[string]string{"goal": "verify the IDE adapter path"},
	})
	if err != nil {
		t.Fatalf("get prompt task_brief: %v", err)
	}
	if len(promptRes.Messages) != 1 {
		t.Fatalf("task_brief prompt messages = %d, want 1", len(promptRes.Messages))
	}
	if got := firstText([]mcp.Content{promptRes.Messages[0].Content}); got != "Turn this goal into a concise task brief with acceptance criteria: verify the IDE adapter path" {
		t.Fatalf("task_brief prompt returned %q", got)
	}

	promptRes, err = clientSession.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "handoff_note",
		Arguments: map[string]string{
			"project":   "runtime adapters",
			"status":    "session issuance is wired",
			"next_step": "run adapter smoke tests",
		},
	})
	if err != nil {
		t.Fatalf("get prompt handoff_note: %v", err)
	}
	if len(promptRes.Messages) != 1 {
		t.Fatalf("handoff_note prompt messages = %d, want 1", len(promptRes.Messages))
	}
	if got := firstText([]mcp.Content{promptRes.Messages[0].Content}); got != "Draft a concise handoff note for runtime adapters. Current status: session issuance is wired Next step: run adapter smoke tests Include blockers, owner, and verification evidence." {
		t.Fatalf("handoff_note prompt returned %q", got)
	}
}

func hasTool(tools []*mcp.Tool, want string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name == want {
			return true
		}
	}
	return false
}

func hasPrompt(prompts []*mcp.Prompt, want string) bool {
	for _, prompt := range prompts {
		if prompt != nil && prompt.Name == want {
			return true
		}
	}
	return false
}

func hasResource(resources []*mcp.Resource, want string) bool {
	for _, resource := range resources {
		if resource != nil && resource.URI == want {
			return true
		}
	}
	return false
}

func firstText(content []mcp.Content) string {
	if len(content) == 0 {
		return ""
	}
	text, _ := content[0].(*mcp.TextContent)
	if text == nil {
		return ""
	}
	return text.Text
}
