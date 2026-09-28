package agentadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	maxLogFieldBytes       = 120
	metaProtocolVersionKey = "io.modelcontextprotocol/protocolVersion"
)

type rpcMethodContextKey struct{}

// withRPCMethod stores the JSON-RPC method name on ctx so RuntimeTransport can
// read it for method-keyed retry and OTel attribute labeling.
func withRPCMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, rpcMethodContextKey{}, method)
}

func rpcMethodFromContext(ctx context.Context) string {
	m, _ := ctx.Value(rpcMethodContextKey{}).(string)
	return m
}

func requestProtocolVersion(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var decoded struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return ""
	}
	raw, ok := decoded.Meta[metaProtocolVersionKey]
	if !ok {
		return ""
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}

type rpcRequestMetadata struct {
	ID       json.RawMessage
	HasID    bool
	Method   string
	ToolName string
	// ProtocolVersion is params._meta's protocol version; empty for legacy
	// (initialize-based) requests.
	ProtocolVersion string
}

func parseRPCRequestMetadata(payload []byte) rpcRequestMetadata {
	envelope, hasID, err := parseRPCEnvelope(payload)
	if err != nil {
		return rpcRequestMetadata{}
	}
	meta := rpcRequestMetadata{
		HasID:    hasID,
		Method:   envelope.Method,
		ToolName: toolNameFromRPCParams(envelope.Method, envelope.Params),

		ProtocolVersion: requestProtocolVersion(envelope.Params),
	}
	if len(envelope.ID) > 0 {
		meta.ID = append(json.RawMessage(nil), envelope.ID...)
	}
	return meta
}

func rpcIDOrNull(meta rpcRequestMetadata) json.RawMessage {
	if meta.HasID && len(meta.ID) > 0 {
		return meta.ID
	}
	return json.RawMessage("null")
}

func toolNameFromRPCParams(method string, params json.RawMessage) string {
	if method != "tools/call" || len(params) == 0 {
		return ""
	}
	var toolCall struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &toolCall); err != nil {
		return ""
	}
	return strings.TrimSpace(toolCall.Name)
}

func logRuntimeDenial(logLevel string, logWriter io.Writer, component string, status int, message string, meta rpcRequestMetadata) {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(logLevel), "info") {
		return
	}
	writer := logWriter
	if writer == nil {
		writer = os.Stderr
	}

	fmt.Fprintf(writer, "%s: %d %s", component, status, sanitizeLogField(message))
	if meta.Method != "" {
		fmt.Fprintf(writer, " method=%s", sanitizeLogField(meta.Method))
	}
	if meta.ToolName != "" {
		fmt.Fprintf(writer, " tool=%s", sanitizeLogField(meta.ToolName))
	}
	fmt.Fprintln(writer)
}

func sanitizeLogField(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "_")
	if value == "" {
		return "unknown"
	}
	if len(value) > maxLogFieldBytes {
		return value[:maxLogFieldBytes]
	}
	return value
}

func closeIfPossible(reader io.Reader) {
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
}

// isSessionExpiredBody returns true when the runtime denial body indicates
// that the caller's session has expired or was not found — signals that the
// agent should re-initialize rather than retry the current call.
func isSessionExpiredBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var obj struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	var msg string
	switch v := obj.Error.(type) {
	case string:
		msg = v
	case map[string]any:
		if m, ok := v["message"].(string); ok {
			msg = m
		}
		if c, ok := v["code"].(string); ok {
			msg += " " + c
		}
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "session_expired") || strings.Contains(lower, "session_not_found")
}

// injectRuntimeStatus adds runtime_status to the error.data field of a
// JSON-RPC error body. Returns body unchanged when it is not a JSON-RPC error.
func injectRuntimeStatus(body []byte, status string) []byte {
	if !looksLikeJSONRPCError(body) {
		return body
	}
	var response struct {
		JSONRPC string             `json:"jsonrpc"`
		ID      json.RawMessage    `json:"id,omitempty"`
		Error   *rpcErrorForInject `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Error == nil {
		return body
	}
	if response.Error.Data == nil {
		response.Error.Data = make(map[string]any)
	}
	response.Error.Data["runtime_status"] = status
	out, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return out
}

type rpcErrorForInject struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// protocolVersionFromInitializeResult extracts result.protocolVersion from a
// runtime initialize response body. Returns "" when the field is absent or the
// body is not valid JSON-RPC.
func protocolVersionFromInitializeResult(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return ""
	}
	return strings.TrimSpace(response.Result.ProtocolVersion)
}

// isToolsListChangedNotification reports whether a runtime response body is a
// JSON-RPC notification announcing that the tools list changed. Agents that
// see this should invalidate any cached tools/list response.
func isToolsListChangedNotification(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var msg struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return false
	}
	return msg.JSONRPC == "2.0" && msg.Method == "notifications/tools/list_changed"
}

// rebindResponseID replaces the "id" field of a JSON-RPC response body with the
// supplied raw ID. Used to serve a cached tools/list response to a caller whose
// request ID differs from the one captured at cache-store time. Returns nil
// when rebinding fails (callers fall back to an authoritative upstream call;
// returning the body with its old id would be a JSON-RPC protocol violation).
func rebindResponseID(body []byte, id json.RawMessage) []byte {
	if len(id) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	raw["id"] = id
	out, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return out
}
