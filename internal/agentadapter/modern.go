package agentadapter

import (
	"encoding/json"
	"strings"
)

// MetaProtocolVersionKey carries the per-request MCP protocol version.
const MetaProtocolVersionKey = "io.modelcontextprotocol/protocolVersion"

// requestProtocolVersion returns params._meta["io.modelcontextprotocol/protocolVersion"],
// or "" when the request does not declare one (legacy requests).
func requestProtocolVersion(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	raw, ok := p.Meta[MetaProtocolVersionKey]
	if !ok {
		return ""
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}
