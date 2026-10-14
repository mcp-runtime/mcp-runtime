package clickhouse

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"mcp-runtime/pkg/events"
)

// Explicit loopback target only: this test creates and drops its own database
// and never reads the deployment's ClickHouse configuration.
func TestClickHouseActorAuthorityMigrationRoundTrip(t *testing.T) {
	address := os.Getenv("EVENTS_CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set EVENTS_CLICKHOUSE_TEST_ADDR to a disposable loopback ClickHouse server")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		t.Fatal("ClickHouse integration target must be loopback")
	}
	conn, err := ch.Open(&ch.Options{Addr: []string{address}, Auth: ch.Auth{Database: "default"}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("actor_authority_%d", time.Now().UnixNano())
	defer func() {
		if err := conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC"); err != nil {
			t.Error(err)
		}
	}()
	manifest, err := os.ReadFile("../../k8s/04-clickhouse-init.yaml")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`--query "([^"]+)"`).FindAllStringSubmatch(string(manifest), -1)
	if len(matches) < 10 {
		t.Fatal("initialization SQL not found")
	}
	queries := make([]string, 0, len(matches))
	for _, match := range matches {
		queries = append(queries, strings.ReplaceAll(match[1], "${CLICKHOUSE_DB}", database))
	}
	subjectColumn := ", subject_team_id String MATERIALIZED JSONExtractString(payload, 'subject_team_id')"
	resourceColumn := ", resource_team_id String MATERIALIZED if(JSONExtractString(payload, 'resource_team_id') != '', JSONExtractString(payload, 'resource_team_id'), JSONExtractString(payload, 'team_id'))"
	// Reconstruct the immediately preceding schema, then apply the actual Job's
	// additive SQL to already stored records rather than only testing new inserts.
	for _, query := range queries {
		if strings.Contains(query, "CREATE TABLE") {
			query = strings.ReplaceAll(strings.ReplaceAll(query, subjectColumn, ""), resourceColumn, "")
		} else if strings.Contains(query, "subject_team_id") || strings.Contains(query, "resource_team_id") {
			continue
		}
		if err = conn.Exec(ctx, query); err != nil {
			t.Fatalf("legacy schema query %s: %v", query, err)
		}
	}
	client := &Client{Conn: conn, DBName: database}
	var batch []events.Envelope
	for _, payload := range []map[string]string{
		{"team_id": "resource-a", "resource_team_id": "resource-a", "subject_team_id": "actor-b", "server": "server-a", "namespace": "team-a", "marker": "cross"},
		{"team_id": "resource-a", "resource_team_id": "resource-a", "subject_team_id": "resource-a", "server": "server-a", "namespace": "team-a", "marker": "same"},
		{"team_id": "resource-a", "server": "server-a", "namespace": "team-a", "marker": "legacy"},
	} {
		envelope, err := events.NewEnvelope("server-a-gateway", "mcp.request", payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		envelope.SetTraceID("trace-" + payload["marker"])
		batch = append(batch, envelope)
	}
	if err = client.InsertEvents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		if err = conn.Exec(ctx, query); err != nil {
			t.Fatalf("migration query %s: %v", query, err)
		}
	}
	rows, err := client.QueryEventsFiltered(ctx, EventFilters{SubjectTeamID: "actor-b", ResourceTeamID: "resource-a", Limit: 100})
	if err != nil || len(rows) != 1 {
		t.Fatalf("cross-team rows=%+v err=%v", rows, err)
	}
	if rows[0].TeamID != "resource-a" || rows[0].ResourceTeamID != "resource-a" || rows[0].SubjectTeamID != "actor-b" || rows[0].Namespace != "team-a" || rows[0].TraceID != "trace-cross" {
		t.Fatalf("actor/authority/trace changed: %+v", rows[0])
	}
	rows, err = client.QueryEventsFiltered(ctx, EventFilters{ResourceTeamID: "resource-a", Limit: 100})
	if err != nil || len(rows) != 3 {
		t.Fatalf("authority rows=%+v err=%v", rows, err)
	}
	legacyFound := false
	for _, row := range rows {
		if row.TraceID == "trace-legacy" {
			legacyFound = true
			if row.SubjectTeamID != "" || row.ResourceTeamID != "resource-a" || row.TeamID != "resource-a" {
				t.Fatalf("legacy caller was guessed: %+v", row)
			}
		}
	}
	if !legacyFound {
		t.Fatal("legacy record lost")
	}
	rows, err = client.QueryEventsFiltered(ctx, EventFilters{TeamID: "actor-b", Limit: 100})
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy authority filter became caller scope: %+v err=%v", rows, err)
	}
}
