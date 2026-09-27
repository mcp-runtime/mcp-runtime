package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota
)

type smokePingArgs struct {
	Note string `json:"note,omitempty" jsonschema:"optional no-op note"`
}

type echoArgs struct {
	Message string `json:"message" jsonschema:"message to echo"`
}

type addArgs struct {
	A float64 `json:"a" jsonschema:"first number"`
	B float64 `json:"b" jsonschema:"second number"`
}

type messageArgs struct {
	Message string `json:"message" jsonschema:"message to transform"`
}

type slugifyArgs struct {
	Message string `json:"message" jsonschema:"message to slugify"`
}

type taskArgs struct {
	Title    string `json:"title" jsonschema:"task title"`
	Priority string `json:"priority,omitempty" jsonschema:"optional priority: low, medium, or high"`
	Owner    string `json:"owner,omitempty" jsonschema:"optional task owner"`
}

type releaseNoteArgs struct {
	Title  string `json:"title" jsonschema:"release note title"`
	Change string `json:"change" jsonschema:"what changed"`
	Impact string `json:"impact,omitempty" jsonschema:"optional user or operator impact"`
}

type server struct{}

type whoamiResponse struct {
	Authenticated bool     `json:"authenticated"`
	Subject       string   `json:"subject,omitempty"`
	AgentID       string   `json:"agentId,omitempty"`
	SessionID     string   `json:"sessionId,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
}

func (s *server) whoamiTool(ctx context.Context, request *mcp.CallToolRequest, _ *smokePingArgs) (*mcp.CallToolResult, any, error) {
	claims, ok := mcpauth.TokenClaimsFromContext(ctx)
	if request != nil && request.Extra != nil && request.Extra.TokenInfo != nil {
		info := request.Extra.TokenInfo
		claims = mcpauth.TokenClaims{Subject: info.UserID, Raw: info.Extra, Scopes: make(map[string]bool)}
		for _, scope := range info.Scopes {
			claims.Scopes[scope] = true
		}
		ok = true
	}
	if !ok {
		data, _ := json.Marshal(whoamiResponse{Authenticated: false})
		return textResult(string(data)), nil, nil
	}
	response := whoamiResponse{Authenticated: true, Subject: claims.Subject, Scopes: make([]string, 0, len(claims.Scopes))}
	if value, ok := claims.Raw["azp"].(string); ok {
		response.AgentID = value
	}
	if value, ok := claims.Raw["sid"].(string); ok {
		response.SessionID = value
	}
	for scope := range claims.Scopes {
		response.Scopes = append(response.Scopes, scope)
	}
	sort.Strings(response.Scopes)
	data, err := json.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	return textResult(string(data)), nil, nil
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

func main() {
	port := envOr("PORT", "8088")
	mcpPath := normalizeMCPPath(envOr("MCP_PATH", "/mcp"))
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv("MCP_AUTH_ISSUER")), "/")
	resource := strings.TrimSpace(os.Getenv("MCP_AUTH_RESOURCE"))
	oauthConfigured := issuer != "" || resource != ""
	mcpServer := newMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	if oauthConfigured {
		if issuer == "" || resource == "" {
			log.Fatal("OAuth mode requires MCP_AUTH_ISSUER and MCP_AUTH_RESOURCE")
		}
		metadataURL := protectedResourceMetadataURL(resource)
		if metadataURL == "" {
			log.Fatal("MCP_AUTH_RESOURCE must be an absolute URL")
		}
		jwksURL, err := resolveJWKS(issuer)
		if err != nil {
			log.Fatal(err)
		}
		verifier := &mcpauth.JWTVerifier{
			JWKSURL:        jwksURL,
			Issuer:         issuer,
			Audience:       resource,
			RequiredScopes: map[string]bool{"tools:read": true},
		}
		// Token verification and the OAuth challenge are handled by this
		// server's mcp-auth SDK middleware, without Runtime gateway auth.
		mux.Handle(mcpPath, mcpauth.RequireToken(verifier, mcpauth.ResourceMetadata{URL: metadataURL}, bindVerifiedIdentity(handler)))
		metadataHandler := newProtectedResourceMetadataHandler(verifier, resource, issuer)
		mux.Handle("/.well-known/oauth-protected-resource"+mcpPath, metadataHandler)
		mux.Handle("/.well-known/oauth-protected-resource", metadataHandler)
	} else {
		mux.Handle(mcpPath, handler)
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("oauth-example-go-2025-11-25 listening on :%s", port)
	log.Fatal(httpServer.ListenAndServe())
}

func resolveJWKS(issuer string) (string, error) {
	metadata, err := mcpauth.DiscoverAuthorizationServer(&http.Client{Timeout: 10 * time.Second}, issuer)
	if err != nil {
		return "", err
	}
	return metadata.JWKSURI, nil
}

// The published SDK version exposes the metadata type but not its handler.
// Derive scopes from the same verifier that enforces them on MCP requests.
func newProtectedResourceMetadataHandler(verifier *mcpauth.JWTVerifier, resource, issuer string) http.Handler {
	scopes := make([]string, 0, len(verifier.RequiredScopes))
	for scope := range verifier.RequiredScopes {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	metadata := struct {
		mcpauth.ProtectedResourceMetadata
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}{
		ProtectedResourceMetadata: mcpauth.ProtectedResourceMetadata{
			Resource: resource, AuthorizationServers: []string{issuer}, ScopesSupported: scopes,
		},
		BearerMethodsSupported: []string{"header"},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	})
}

// The mcp-auth middleware validates the JWT. Forward its verified identity to
// the MCP SDK so stateful HTTP sessions are bound to the authenticated subject
// and tools receive the current request's claims.
func bindVerifiedIdentity(handler http.Handler) http.Handler {
	return auth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		claims, ok := mcpauth.TokenClaimsFromContext(ctx)
		expires, hasExpiry := claims.Raw["exp"].(float64)
		if !ok || claims.Subject == "" || !hasExpiry {
			return nil, auth.ErrInvalidToken
		}
		scopes := make([]string, 0, len(claims.Scopes))
		for scope := range claims.Scopes {
			scopes = append(scopes, scope)
		}
		return &auth.TokenInfo{UserID: claims.Subject, Scopes: scopes, Expiration: time.Unix(int64(expires), 0), Extra: claims.Raw}, nil
	}, nil)(handler)
}

func protectedResourceMetadataURL(resource string) string {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	resourcePath := strings.TrimRight(parsed.Path, "/")
	return (&url.URL{
		Scheme: parsed.Scheme,
		Host:   parsed.Host,
		Path:   "/.well-known/oauth-protected-resource" + resourcePath,
	}).String()
}

func newMCPServer() *mcp.Server {
	srv := &server{}
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "oauth-example-go-2025-11-25",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: "Workspace assistant MCP server for task planning, release notes, text cleanup, prompts, and reference resources.",
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "aaa-ping",
		Description: "Check that the workspace assistant is reachable",
	}, srv.smokePingTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "whoami",
		Description: "Return the authenticated subject, agent, session, and scopes, or anonymous status.",
	}, srv.whoamiTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message for adapter and transport debugging",
	}, srv.echoTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "add",
		Description: "Add two numeric values",
	}, srv.addTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "upper",
		Description: "Convert text to uppercase for normalization checks",
	}, srv.upperTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "lower",
		Description: "Convert text to lowercase for normalization checks",
	}, srv.lowerTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "slugify",
		Description: "Convert a title or label into a URL-safe slug",
	}, srv.slugifyTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a deterministic task card summary",
	}, srv.createTaskTool)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "draft_release_note",
		Description: "Draft a compact release note from a change summary and impact",
	}, srv.draftReleaseNoteTool)

	mcpServer.AddResource(&mcp.Resource{
		Name:        "readme",
		Description: "Workspace assistant overview and supported workflows",
		MIMEType:    "text/plain",
		URI:         "embedded:readme",
	}, srv.readResource)

	mcpServer.AddResource(&mcp.Resource{
		Name:        "task-guide",
		Description: "Task card conventions for workspace handoffs",
		MIMEType:    "text/plain",
		URI:         "embedded:task-guide",
	}, srv.readResource)

	mcpServer.AddResource(&mcp.Resource{
		Name:        "workspace-playbook",
		Description: "Short playbook for task, release note, and handoff flows",
		MIMEType:    "text/plain",
		URI:         "embedded:workspace-playbook",
	}, srv.readResource)

	mcpServer.AddPrompt(&mcp.Prompt{
		Name:        "hello",
		Description: "Return a simple workspace assistant greeting",
	}, srv.getHelloPrompt)

	mcpServer.AddPrompt(&mcp.Prompt{
		Name:        "summarize",
		Description: "Ask for a brief summary of a provided note",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "text",
				Description: "Text to summarize",
				Required:    true,
			},
		},
	}, srv.getSummarizePrompt)

	mcpServer.AddPrompt(&mcp.Prompt{
		Name:        "task_brief",
		Description: "Draft a concise task brief from a goal",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "goal",
				Description: "Goal to turn into a task brief",
				Required:    true,
			},
		},
	}, srv.getTaskBriefPrompt)

	mcpServer.AddPrompt(&mcp.Prompt{
		Name:        "handoff_note",
		Description: "Draft a concise handoff note for another teammate",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "project",
				Description: "Project or workstream name",
				Required:    true,
			},
			{
				Name:        "status",
				Description: "Current status or latest progress",
				Required:    false,
			},
			{
				Name:        "next_step",
				Description: "Recommended next step",
				Required:    false,
			},
		},
	}, srv.getHandoffNotePrompt)

	return mcpServer
}

func (s *server) smokePingTool(_ context.Context, _ *mcp.CallToolRequest, _ *smokePingArgs) (*mcp.CallToolResult, any, error) {
	return textResult("pong"), nil, nil
}

func (s *server) echoTool(_ context.Context, _ *mcp.CallToolRequest, args *echoArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &echoArgs{}
	}
	return textResult(args.Message), nil, nil
}

func (s *server) addTool(_ context.Context, _ *mcp.CallToolRequest, args *addArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &addArgs{}
	}
	return textResult(fmt.Sprintf("%g", args.A+args.B)), nil, nil
}

func (s *server) upperTool(_ context.Context, _ *mcp.CallToolRequest, args *messageArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &messageArgs{}
	}
	return textResult(strings.ToUpper(args.Message)), nil, nil
}

func (s *server) lowerTool(_ context.Context, _ *mcp.CallToolRequest, args *messageArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &messageArgs{}
	}
	return textResult(strings.ToLower(args.Message)), nil, nil
}

func (s *server) slugifyTool(_ context.Context, _ *mcp.CallToolRequest, args *slugifyArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &slugifyArgs{}
	}
	slug := strings.ToLower(strings.TrimSpace(args.Message))
	slug = nonSlugChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	return textResult(slug), nil, nil
}

func (s *server) createTaskTool(_ context.Context, _ *mcp.CallToolRequest, args *taskArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &taskArgs{}
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = "Untitled task"
	}
	priority := normalizePriority(args.Priority)
	owner := strings.TrimSpace(args.Owner)
	if owner == "" {
		owner = "unassigned"
	}
	return textResult(fmt.Sprintf("task: %s\npriority: %s\nowner: %s\nstatus: open", title, priority, owner)), nil, nil
}

func (s *server) draftReleaseNoteTool(_ context.Context, _ *mcp.CallToolRequest, args *releaseNoteArgs) (*mcp.CallToolResult, any, error) {
	if args == nil {
		args = &releaseNoteArgs{}
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = "Untitled change"
	}
	change := strings.TrimSpace(args.Change)
	if change == "" {
		change = "No change summary provided."
	}
	impact := strings.TrimSpace(args.Impact)
	if impact == "" {
		impact = "No user impact provided."
	}
	return textResult(fmt.Sprintf("release: %s\nchange: %s\nimpact: %s\nstatus: draft", title, change, impact)), nil, nil
}

func (s *server) readResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if req == nil || req.Params == nil || strings.TrimSpace(req.Params.URI) == "" {
		return nil, fmt.Errorf("invalid request")
	}
	text, ok := resourcePayloads()[req.Params.URI]
	if !ok {
		return nil, fmt.Errorf("resource not found: %s", req.Params.URI)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text:     text,
			},
		},
	}, nil
}

func (s *server) getHelloPrompt(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{
			{
				Role:    "assistant",
				Content: &mcp.TextContent{Text: "Hello from the Workspace assistant MCP server."},
			},
		},
	}, nil
}

func (s *server) getSummarizePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	text := ""
	if req != nil && req.Params != nil {
		text = req.Params.Arguments["text"]
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "No text provided."
	}
	return &mcp.GetPromptResult{
		Description: "Short summary prompt",
		Messages: []*mcp.PromptMessage{
			{
				Role:    "assistant",
				Content: &mcp.TextContent{Text: fmt.Sprintf("Summarize this briefly: %s", text)},
			},
		},
	}, nil
}

func (s *server) getTaskBriefPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	goal := ""
	if req != nil && req.Params != nil {
		goal = req.Params.Arguments["goal"]
	}
	goal = strings.TrimSpace(goal)
	if goal == "" {
		goal = "No goal provided."
	}
	return &mcp.GetPromptResult{
		Description: "Task brief prompt",
		Messages: []*mcp.PromptMessage{
			{
				Role:    "assistant",
				Content: &mcp.TextContent{Text: fmt.Sprintf("Turn this goal into a concise task brief with acceptance criteria: %s", goal)},
			},
		},
	}, nil
}

func (s *server) getHandoffNotePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	project := promptArg(req, "project", "the current project")
	status := promptArg(req, "status", "No status provided.")
	nextStep := promptArg(req, "next_step", "No next step provided.")
	return &mcp.GetPromptResult{
		Description: "Handoff note prompt",
		Messages: []*mcp.PromptMessage{
			{
				Role: "assistant",
				Content: &mcp.TextContent{Text: fmt.Sprintf(
					"Draft a concise handoff note for %s. Current status: %s Next step: %s Include blockers, owner, and verification evidence.",
					project,
					status,
					nextStep,
				)},
			},
		},
	}, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}

func promptArg(req *mcp.GetPromptRequest, name, fallback string) string {
	if req == nil || req.Params == nil {
		return fallback
	}
	value := strings.TrimSpace(req.Params.Arguments[name])
	if value == "" {
		return fallback
	}
	return value
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func normalizeMCPPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func normalizePriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(priority))
	default:
		return "medium"
	}
}

func resourcePayloads() map[string]string {
	return map[string]string{
		"embedded:readme":             "Workspace assistant MCP exposes task cards, release notes, text cleanup, summaries, and handoff prompts for local runtime smoke tests.",
		"embedded:task-guide":         "Task card format: title, priority (low|medium|high), owner, and status. Use create_task for deterministic adapter assertions.",
		"embedded:workspace-playbook": "Playbook: use slugify for route labels, create_task for work tracking, draft_release_note for release summaries, and handoff_note before switching owners.",
	}
}
