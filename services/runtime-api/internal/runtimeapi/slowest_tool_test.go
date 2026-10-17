package runtimeapi

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	chpkg "mcp-runtime/pkg/clickhouse"
)

type observedToolConn struct {
	driver.Conn
	query string
	args  []any
	rows  driver.Rows
}

func (c *observedToolConn) Query(_ context.Context, query string, args ...any) (driver.Rows, error) {
	c.query, c.args = query, args
	return c.rows, nil
}

type observedToolRows struct {
	driver.Rows
	tool string
	done bool
}

func (r *observedToolRows) Next() bool {
	if r.done || r.tool == "" {
		return false
	}
	r.done = true
	return true
}
func (r *observedToolRows) Scan(dest ...any) error {
	if len(dest) != 1 {
		return fmt.Errorf("unexpected scan columns")
	}
	*dest[0].(*string) = r.tool
	return nil
}
func (*observedToolRows) Err() error   { return nil }
func (*observedToolRows) Close() error { return nil }

func TestSlowestToolRequiresScopedActualCallEvidence(t *testing.T) {
	for _, tool := range []string{"", "slow-tool"} {
		conn := &observedToolConn{rows: &observedToolRows{tool: tool}}
		server := &RuntimeServer{db: &chpkg.Client{Conn: conn, DBName: "mcp"}}
		got, err := server.slowestObservedTool(context.Background(), "mcp-servers", "demo", "current-uid")
		if err != nil || got != tool {
			t.Fatalf("tool=%q err=%v", got, err)
		}
		if !reflect.DeepEqual(conn.args, []any{"mcp-servers", "demo", "current-uid"}) {
			t.Fatalf("scope was not parameterized with current UID: %v", conn.args)
		}
		for _, required := range []string{"namespace = ?", "server = ?", "JSONExtractString(payload, 'server_uid') = ?", "INTERVAL 1 HOUR", "timestamp <= now()", "'tools/call'", "event_type = 'mcp.request'", "decision = 'allow'", "HAVING count() >= 5", "quantileExact(0.95)"} {
			if !strings.Contains(conn.query, required) {
				t.Errorf("missing actual tools/call evidence constraint: %s", required)
			}
		}
	}
	// Without analytics rows the summary must omit the name, even if metric
	// histograms happen to contain extrapolated samples.
	if tool, err := (&RuntimeServer{}).slowestObservedTool(context.Background(), "team", "server", "uid"); err != nil || tool != "" {
		t.Fatalf("unavailable ClickHouse fabricated a slowest tool: %q %v", tool, err)
	}
}
