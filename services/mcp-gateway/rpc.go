package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	policypkg "mcp-runtime/pkg/policy"
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolParams struct {
	Name string `json:"name"`
}

func inspectRPCRequest(r *http.Request) rpcInspection {
	if r.Method != http.MethodPost {
		return rpcInspection{}
	}
	contentType := strings.ToLower(r.Header.Get("content-type"))
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		// Clearly not a JSON-RPC client (e.g. text/plain probe). Not an audit-worthy
		// MCP attempt.
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed"}
	}
	// From here on the content-type is JSON (or absent), so treat failures as
	// genuine MCP client attempts that should produce audit events.
	if r.Body == nil || r.ContentLength == 0 || r.ContentLength > maxRPCBodyBytes {
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed", IsRPCAttempt: true}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBodyBytes+1))
	if err != nil {
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed", IsRPCAttempt: true}
	}
	if len(body) == 0 || len(body) > maxRPCBodyBytes {
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed", IsRPCAttempt: true}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed", IsRPCAttempt: true}
	}
	if strings.TrimSpace(req.Method) == "" {
		return rpcInspection{Indeterminate: true, FailureReason: "rpc_inspection_failed", IsRPCAttempt: true}
	}

	var toolName string
	if len(req.Params) > 0 {
		var params toolParams
		if err := json.Unmarshal(req.Params, &params); err == nil {
			toolName = params.Name
		}
	}

	return rpcInspection{
		Method:    req.Method,
		ToolName:  toolName,
		ToolCall:  policypkg.IsToolCallMethod(req.Method),
		ParamsRaw: req.Params,
		IDRaw:     req.ID,
	}
}

// maxInt64 returns the maximum of two int64 values.
