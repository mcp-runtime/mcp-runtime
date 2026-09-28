package runtimeapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcp-runtime/pkg/controlplane"
)

const (
	liveInventoryTTL             = 30 * time.Second
	liveInventoryProbeTimeout    = 5 * time.Second
	liveInventoryProtocolVersion = "2025-06-18"
	liveInventoryMaxBodyBytes    = 2 << 20
	liveInventoryMaxCacheEntries = 512

	mcpProtocolHeader = "Mcp-Protocol-Version"
	mcpSessionHeader  = "Mcp-Session-Id"
)

type liveInventory struct {
	FetchedAt       time.Time               `json:"fetchedAt"`
	ProtocolVersion string                  `json:"protocolVersion,omitempty"`
	Tools           []liveInventoryTool     `json:"tools"`
	Prompts         []liveInventoryPrompt   `json:"prompts"`
	Resources       []liveInventoryResource `json:"resources"`
}

type liveInventoryTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type liveInventoryPrompt struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description,omitempty"`
	Arguments   []liveInventoryPromptArgument `json:"arguments,omitempty"`
}

type liveInventoryPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type liveInventoryResource struct {
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type liveInventoryProber interface {
	probe(ctx context.Context, server controlplane.ServerInfo) (*liveInventory, error)
}

type liveInventoryKey struct {
	namespace  string
	name       string
	uid        string
	generation int64
}

type liveInventoryEntry struct {
	inventory *liveInventory
	reason    string
	storedAt  time.Time
}

type liveInventoryCache struct {
	ttl    time.Duration
	prober liveInventoryProber
	now    func() time.Time

	maxEntries int
	mu         sync.Mutex
	entries    map[liveInventoryKey]liveInventoryEntry
	inFlight   map[liveInventoryKey]struct{}
}

func (s *InventoryService) liveInventory() *liveInventoryCache {
	if s == nil {
		return nil
	}
	s.liveInventoryOnce.Do(func() {
		prober := s.liveInventoryProbe
		if prober == nil {
			prober = &mcpLiveInventoryProber{client: &http.Client{Timeout: liveInventoryProbeTimeout}, access: s.access}
		}
		s.liveInventoryCache = newLiveInventoryCache(liveInventoryTTL, prober)
	})
	return s.liveInventoryCache
}

func newLiveInventoryCache(ttl time.Duration, prober liveInventoryProber) *liveInventoryCache {
	if ttl <= 0 {
		ttl = liveInventoryTTL
	}
	return &liveInventoryCache{
		ttl:        ttl,
		prober:     prober,
		now:        time.Now,
		maxEntries: liveInventoryMaxCacheEntries,
		entries:    map[liveInventoryKey]liveInventoryEntry{},
		inFlight:   map[liveInventoryKey]struct{}{},
	}
}

func (c *liveInventoryCache) getOrStart(ctx context.Context, server controlplane.ServerInfo) (*liveInventory, string) {
	if c == nil || c.prober == nil {
		return nil, "live inventory unavailable"
	}
	key := liveInventoryKey{
		namespace:  strings.TrimSpace(server.Namespace),
		name:       strings.TrimSpace(server.Name),
		uid:        strings.TrimSpace(server.UID),
		generation: server.Generation,
	}
	if key.namespace == "" || key.name == "" {
		return nil, "live inventory endpoint unavailable"
	}

	now := c.currentTime()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Sub(entry.storedAt) < c.ttl {
		c.mu.Unlock()
		return entry.inventory, entry.reason
	}
	if _, ok := c.inFlight[key]; ok {
		c.mu.Unlock()
		return nil, "live inventory pending"
	}
	c.inFlight[key] = struct{}{}
	c.mu.Unlock()

	go c.refresh(ctx, key, server)
	return nil, "live inventory pending"
}

func (c *liveInventoryCache) refresh(parent context.Context, key liveInventoryKey, server controlplane.ServerInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), liveInventoryProbeTimeout)
	defer cancel()
	if deadline, ok := parent.Deadline(); ok {
		if until := time.Until(deadline); until > 0 && until < liveInventoryProbeTimeout {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), until)
			defer cancel()
		}
	}

	inventory, err := c.prober.probe(ctx, server)
	reason := ""
	if err != nil {
		inventory = nil
		reason = shortLiveInventoryReason(err)
	}
	now := c.currentTime()
	c.mu.Lock()
	c.entries[key] = liveInventoryEntry{inventory: inventory, reason: reason, storedAt: now}
	delete(c.inFlight, key)
	c.pruneLocked(now, key)
	c.mu.Unlock()
}

func (c *liveInventoryCache) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *liveInventoryCache) pruneLocked(now time.Time, keep liveInventoryKey) {
	for key, entry := range c.entries {
		if key == keep {
			continue
		}
		if key.namespace == keep.namespace && key.name == keep.name {
			delete(c.entries, key)
			continue
		}
		if now.Sub(entry.storedAt) >= 2*c.ttl {
			delete(c.entries, key)
		}
	}
	maxEntries := c.maxEntries
	if maxEntries <= 0 {
		maxEntries = liveInventoryMaxCacheEntries
	}
	for len(c.entries) > maxEntries {
		var oldestKey liveInventoryKey
		var oldestAt time.Time
		found := false
		for key, entry := range c.entries {
			if key == keep {
				continue
			}
			if !found || entry.storedAt.Before(oldestAt) {
				oldestKey = key
				oldestAt = entry.storedAt
				found = true
			}
		}
		if !found {
			break
		}
		delete(c.entries, oldestKey)
	}
}

func shortLiveInventoryReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "live inventory probe failed"
	}
	msg = strings.Join(strings.Fields(msg), " ")
	runes := []rune(msg)
	if len(runes) > 180 {
		return string(runes[:180])
	}
	return msg
}

type liveInventoryCapabilities struct {
	tools     bool
	prompts   bool
	resources bool
}

type mcpLiveInventoryProber struct {
	client           *http.Client
	baseURLForServer func(controlplane.ServerInfo) string
	now              func() time.Time
	access           *AccessService
}

func (p *mcpLiveInventoryProber) probe(ctx context.Context, server controlplane.ServerInfo) (*liveInventory, error) {
	endpoint, err := p.endpoint(server)
	if err != nil {
		return nil, err
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: liveInventoryProbeTimeout}
	}
	session := ""
	protocol := liveInventoryProtocolVersion
	initResult, initSession, err := p.call(ctx, client, endpoint, protocol, session, 1, "initialize", map[string]any{
		"protocolVersion": liveInventoryProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "mcp-runtime-api",
			"version": "live-inventory",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if negotiated := protocolVersionFromInitializeResult(initResult); negotiated != "" {
		protocol = negotiated
	}
	capabilities := capabilitiesFromInitializeResult(initResult)
	session = initSession

	if _, nextSession, err := p.notify(ctx, client, endpoint, protocol, session, "notifications/initialized", map[string]any{}); err == nil && nextSession != "" {
		session = nextSession
	}

	var toolsRaw json.RawMessage
	var promptsRaw json.RawMessage
	var resourcesRaw json.RawMessage
	var nextSession string
	if capabilities.tools {
		toolsRaw, nextSession, err = p.call(ctx, client, endpoint, protocol, session, 2, "tools/list", nil)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		if nextSession != "" {
			session = nextSession
		}
	}
	if capabilities.prompts {
		promptsRaw, nextSession, err = p.call(ctx, client, endpoint, protocol, session, 3, "prompts/list", nil)
		if err == nil && nextSession != "" {
			session = nextSession
		}
	}
	if capabilities.resources {
		resourcesRaw, _, err = p.call(ctx, client, endpoint, protocol, session, 4, "resources/list", nil)
		if err != nil {
			resourcesRaw = nil
		}
	}

	return &liveInventory{
		FetchedAt:       p.currentTime().UTC(),
		ProtocolVersion: protocol,
		Tools:           decodeLiveTools(toolsRaw),
		Prompts:         decodeLivePrompts(promptsRaw),
		Resources:       decodeLiveResources(resourcesRaw),
	}, nil
}

func (p *mcpLiveInventoryProber) endpoint(server controlplane.ServerInfo) (string, error) {
	if p.baseURLForServer != nil {
		if value := strings.TrimSpace(p.baseURLForServer(server)); value != "" {
			return value, nil
		}
	}
	name := strings.TrimSpace(server.Name)
	namespace := strings.TrimSpace(server.Namespace)
	if name == "" || namespace == "" {
		return "", errors.New("server namespace/name is required")
	}
	if endpoint := liveInventoryPublicEndpoint(server.Endpoint); endpoint != "" {
		return endpoint, nil
	}
	endpointPath := endpointPath(server.Endpoint)
	if endpointPath == "" {
		endpointPath = "/"
	}
	port := server.ServicePort
	if port == 0 {
		port = 80
	}
	return (&url.URL{
		Scheme: "http",
		Host:   name + "." + namespace + ".svc.cluster.local:" + strconv.Itoa(int(port)),
		Path:   endpointPath,
	}).String(), nil
}

func liveInventoryPublicEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return ""
	}
	return parsed.String()
}

func endpointPath(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Path != "" {
		endpoint = parsed.Path
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return endpoint
}

func (p *mcpLiveInventoryProber) currentTime() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *mcpLiveInventoryProber) notify(ctx context.Context, client *http.Client, endpoint, protocol, session string, method string, params any) (json.RawMessage, string, error) {
	return p.rpc(ctx, client, endpoint, protocol, session, nil, method, params)
}

func (p *mcpLiveInventoryProber) call(ctx context.Context, client *http.Client, endpoint, protocol, session string, id int, method string, params any) (json.RawMessage, string, error) {
	rawID, _ := json.Marshal(id)
	return p.rpc(ctx, client, endpoint, protocol, session, rawID, method, params)
}

func (p *mcpLiveInventoryProber) rpc(ctx context.Context, client *http.Client, endpoint, protocol, session string, id json.RawMessage, method string, params any) (json.RawMessage, string, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if len(id) > 0 {
		payload["id"] = json.RawMessage(id)
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	req.Header.Set(mcpProtocolHeader, protocol)
	if session != "" {
		req.Header.Set(mcpSessionHeader, session)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		message := http.StatusText(resp.StatusCode)
		if detail := readSmallErrorBody(resp.Body); detail != "" {
			message = detail
		}
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, message)
	}
	responseBody, err := readMCPResponseBody(resp)
	if err != nil {
		return nil, "", err
	}
	if len(id) == 0 || len(responseBody) == 0 {
		return nil, resp.Header.Get(mcpSessionHeader), nil
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, "", err
	}
	if envelope.Error != nil {
		return nil, "", fmt.Errorf("JSON-RPC %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.JSONRPC != "2.0" {
		return nil, "", errors.New("invalid JSON-RPC response")
	}
	return envelope.Result, resp.Header.Get(mcpSessionHeader), nil
}

func readSmallErrorBody(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, 1024))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func readMCPResponseBody(resp *http.Response) ([]byte, error) {
	if strings.Contains(strings.ToLower(resp.Header.Get("content-type")), "text/event-stream") {
		return firstSSEData(resp.Body)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, liveInventoryMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > liveInventoryMaxBodyBytes {
		return nil, errors.New("upstream response too large")
	}
	return bytes.TrimSpace(payload), nil
}

func firstSSEData(body io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), liveInventoryMaxBodyBytes)
	var event bytes.Buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if event.Len() > 0 {
				return bytes.TrimSpace(event.Bytes()), nil
			}
			continue
		}
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.Write(bytes.TrimSpace(data))
		}
		if event.Len() > liveInventoryMaxBodyBytes {
			return nil, errors.New("upstream response too large")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(event.Bytes()), nil
}

func protocolVersionFromInitializeResult(raw json.RawMessage) string {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.ProtocolVersion)
}

func capabilitiesFromInitializeResult(raw json.RawMessage) liveInventoryCapabilities {
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return liveInventoryCapabilities{}
	}
	return liveInventoryCapabilities{
		tools:     capabilityPresent(result.Capabilities, "tools"),
		prompts:   capabilityPresent(result.Capabilities, "prompts"),
		resources: capabilityPresent(result.Capabilities, "resources"),
	}
}

func capabilityPresent(capabilities map[string]json.RawMessage, name string) bool {
	raw, ok := capabilities[name]
	if !ok {
		return false
	}
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func decodeLiveTools(raw json.RawMessage) []liveInventoryTool {
	var result struct {
		Tools []liveInventoryTool `json:"tools"`
	}
	_ = json.Unmarshal(raw, &result)
	if len(result.Tools) == 0 {
		return []liveInventoryTool{}
	}
	return result.Tools
}

func decodeLivePrompts(raw json.RawMessage) []liveInventoryPrompt {
	var result struct {
		Prompts []liveInventoryPrompt `json:"prompts"`
	}
	_ = json.Unmarshal(raw, &result)
	if len(result.Prompts) == 0 {
		return []liveInventoryPrompt{}
	}
	return result.Prompts
}

func decodeLiveResources(raw json.RawMessage) []liveInventoryResource {
	var result struct {
		Resources []liveInventoryResource `json:"resources"`
	}
	_ = json.Unmarshal(raw, &result)
	if len(result.Resources) == 0 {
		return []liveInventoryResource{}
	}
	return result.Resources
}
