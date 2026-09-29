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

const maxLogFieldBytes = 120

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

type rpcRequestEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

type rpcError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func parseRPCEnvelope(payload []byte) (rpcRequestEnvelope, bool, error) {
	var envelope rpcRequestEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return envelope, false, err
	}
	return envelope, len(envelope.ID) > 0, nil
}

func looksLikeJSONRPCError(payload []byte) bool {
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return false
	}
	return response.JSONRPC == "2.0" && len(response.Error) > 0
}

func extractHTTPErrorMessage(status int, payload []byte) string {
	if len(payload) > 0 {
		var object struct {
			Error any `json:"error"`
		}
		if err := json.Unmarshal(payload, &object); err == nil {
			switch value := object.Error.(type) {
			case string:
				if strings.TrimSpace(value) != "" {
					return value
				}
			case map[string]any:
				if message, ok := value["message"].(string); ok && strings.TrimSpace(message) != "" {
					return message
				}
			}
		}
		if text := strings.TrimSpace(string(payload)); text != "" {
			if len(text) > 240 {
				return text[:240]
			}
			return text
		}
	}
	if text := http.StatusText(status); text != "" {
		return text
	}
	return fmt.Sprintf("upstream HTTP %d", status)
}

func jsonRPCHTTPError(id json.RawMessage, status int, message string, payload []byte) []byte {
	response := rpcErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: rpcError{
			Code:    -32000,
			Message: message,
			Data: map[string]any{
				"http_status": status,
			},
		},
	}
	if len(payload) > 0 && len(payload) <= 4096 {
		response.Error.Data["upstream_body"] = string(payload)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"upstream error"}}`)
	}
	return encoded
}

func jsonRPCParseError(detail string) []byte {
	response := rpcErrorResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Error: rpcError{
			Code:    -32700,
			Message: "parse error",
			Data: map[string]any{
				"detail": detail,
			},
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
	}
	return encoded
}
