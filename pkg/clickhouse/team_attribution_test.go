package clickhouse

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"mcp-runtime/pkg/events"
)

func TestAuditActorAndAuthorityFiltersRemainDistinct(t *testing.T) {
	where, args := buildEventFilterWhereClause(EventFilters{TeamID: "resource-a", SubjectTeamID: "actor-b", ResourceTeamID: "resource-a"})
	for _, condition := range []string{"team_id = ?", "subject_team_id = ?", "resource_team_id = ?"} {
		if !strings.Contains(where, condition) {
			t.Fatalf("missing exact filter %q: %s", condition, where)
		}
	}
	if !reflect.DeepEqual(args, []interface{}{"resource-a", "actor-b", "resource-a"}) {
		t.Fatalf("args=%#v", args)
	}
	if strings.Contains(where, " OR ") {
		t.Fatal("actor filter broadened the resource authority filter")
	}
}

type captureEventConn struct {
	driver.Conn
	batch *captureEventBatch
	query string
}

func (c *captureEventConn) PrepareBatch(_ context.Context, query string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	c.query = query
	return c.batch, nil
}

type captureEventBatch struct {
	driver.Batch
	rows [][]any
	sent bool
}

func (b *captureEventBatch) Append(values ...any) error { b.rows = append(b.rows, values); return nil }
func (b *captureEventBatch) Send() error                { b.sent = true; return nil }

func TestEventBatchPreservesActorAuthorityAndTracePayload(t *testing.T) {
	envelope, err := events.NewEnvelope("server-a-gateway", "mcp.request", map[string]any{"team_id": "resource-a", "resource_team_id": "resource-a", "subject_team_id": "actor-b", "namespace": "team-a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	envelope.SetTraceID("trace-cross-team")
	batch := &captureEventBatch{}
	conn := &captureEventConn{batch: batch}
	client := &Client{Conn: conn, DBName: "mcp"}
	if err := client.InsertEvents(context.Background(), []events.Envelope{envelope}); err != nil {
		t.Fatal(err)
	}
	if !batch.sent || len(batch.rows) != 1 || batch.rows[0][1] != "trace-cross-team" {
		t.Fatalf("batch=%+v", batch)
	}
	if strings.Contains(conn.query, "subject_team_id") || strings.Contains(conn.query, "resource_team_id") {
		t.Fatal("materialized team fields were written independently of authoritative event payload")
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(batch.rows[0][4].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["team_id"] != "resource-a" || payload["resource_team_id"] != "resource-a" || payload["subject_team_id"] != "actor-b" {
		t.Fatalf("attribution changed during persistence: %v", payload)
	}
}
