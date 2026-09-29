package agentadapter

import (
	"encoding/json"
	"testing"
)

func TestRequestProtocolVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
		want   string
	}{
		{name: "modern", params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`, want: "2026-07-28"},
		{name: "legacy has no meta", params: `{"name":"echo"}`, want: ""},
		{name: "other meta keys only", params: `{"_meta":{"progressToken":1}}`, want: ""},
		{name: "non-string version", params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":5}}`, want: ""},
		{name: "empty params", params: ``, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestProtocolVersion(json.RawMessage(tt.params)); got != tt.want {
				t.Fatalf("requestProtocolVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
