package agentadapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

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
