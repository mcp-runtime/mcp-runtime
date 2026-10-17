package clickhouse

import (
	"reflect"
	"strings"
	"testing"
)

func TestServerEventFiltersUseImmutableIdentity(t *testing.T) {
	where, args := buildEventFilterWhereClause(EventFilters{Server: "same-name", ServerUID: "replacement-uid", Namespace: "mcp-servers"})
	if !strings.Contains(where, "JSONExtractString(payload, 'server_uid') = ?") || !strings.Contains(where, "server = ?") || !strings.Contains(where, "namespace = ?") {
		t.Fatalf("server instance scope missing from query: %s", where)
	}
	if !reflect.DeepEqual(args, []any{"same-name", "replacement-uid", "mcp-servers"}) {
		t.Fatalf("server identity was not parameterized: %v", args)
	}
}
