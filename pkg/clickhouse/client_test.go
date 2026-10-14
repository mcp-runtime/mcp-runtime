package clickhouse

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type stubRowScanner struct {
	values []any
	err    error
}

func (s stubRowScanner) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	for i := range dest {
		switch ptr := dest[i].(type) {
		case *time.Time:
			*ptr = s.values[i].(time.Time)
		case *string:
			*ptr = s.values[i].(string)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func TestValidateDBName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid", input: "analytics_events", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "starts with digit", input: "1events", wantErr: true},
		{name: "contains dash", input: "analytics-events", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateDBName(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestNormalizeEventLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 100},
		{input: -5, want: 100},
		{input: 25, want: 25},
		{input: 2000, want: 1000},
	}

	for _, tc := range tests {
		if got := normalizeEventLimit(tc.input); got != tc.want {
			t.Fatalf("normalizeEventLimit(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeEventOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input int
		want  int
	}{
		{input: -5, want: 0},
		{input: 0, want: 0},
		{input: 25, want: 25},
		{input: maxEventOffset + 1, want: maxEventOffset},
	}

	for _, tc := range tests {
		if got := normalizeEventOffset(tc.input); got != tc.want {
			t.Fatalf("normalizeEventOffset(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestEventFilterQueryCapsOffset(t *testing.T) {
	t.Parallel()

	query := buildEventFilterQuery("mcp", "", 25, maxEventOffset+1)
	if !strings.Contains(query, "OFFSET 100000") {
		t.Fatalf("query = %q, want capped OFFSET", query)
	}
}

func TestEventQueriesUseMaterializedTeamIDColumn(t *testing.T) {
	t.Parallel()

	if !strings.Contains(eventSelectColumns, "namespace, team_id, cluster") {
		t.Fatalf("eventSelectColumns = %q, want direct team_id column selected", eventSelectColumns)
	}
	if !strings.Contains(eventSelectColumns, "trace_id") {
		t.Fatalf("eventSelectColumns = %q, want trace_id selected", eventSelectColumns)
	}

	whereClause, args := buildEventFilterWhereClause(EventFilters{TraceID: "trace-123", TeamID: "team-acme", Limit: 25})
	if !strings.Contains(whereClause, "trace_id = ?") {
		t.Fatalf("whereClause = %q, want trace_id filter", whereClause)
	}
	if !strings.Contains(whereClause, "team_id = ?") {
		t.Fatalf("whereClause = %q, want direct team_id filter", whereClause)
	}
	if len(args) != 2 || args[0] != "trace-123" || args[1] != "team-acme" {
		t.Fatalf("args = %#v, want trace-123 and team-acme", args)
	}

	query := buildEventFilterQuery("mcp", whereClause, 25, 0)
	if !strings.Contains(query, "FROM mcp.events WHERE") {
		t.Fatalf("query = %q, want filtered events query", query)
	}
	if !strings.Contains(query, "OFFSET 0") {
		t.Fatalf("query = %q, want OFFSET clause", query)
	}
}

func TestScanEventRow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	scanner := stubRowScanner{
		values: []any{
			now,
			"trace-123",
			"gateway",
			"tools/call",
			"demo-one",
			"mcp-servers",
			"team-acme",
			"kind",
			"user-123",
			"ops-agent",
			"sess-1",
			"allow",
			"add",
			"team-actor",
			"team-acme",
			`{"value":1}`,
		},
	}

	var row EventRow
	if err := scanEventRow(scanner, &row); err != nil {
		t.Fatalf("scanEventRow returned error: %v", err)
	}
	if row.Timestamp != now {
		t.Fatalf("unexpected timestamp: got %v want %v", row.Timestamp, now)
	}
	if row.TraceID != "trace-123" {
		t.Fatalf("unexpected trace ID: got %q want trace-123", row.TraceID)
	}
	if string(row.Payload) != `{"value":1}` {
		t.Fatalf("unexpected payload: %s", row.Payload)
	}
	if row.TeamID != "team-acme" {
		t.Fatalf("unexpected team ID: got %q want team-acme", row.TeamID)
	}
	if row.SubjectTeamID != "team-actor" || row.ResourceTeamID != "team-acme" {
		t.Fatalf("actor/authority fields lost: %+v", row)
	}
}

func TestScanEventRowWrapsInvalidJSONPayload(t *testing.T) {
	t.Parallel()

	scanner := stubRowScanner{
		values: []any{
			time.Unix(1_700_000_000, 0).UTC(),
			"",
			"gateway",
			"tools/call",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"plain-text",
		},
	}

	var row EventRow
	if err := scanEventRow(scanner, &row); err != nil {
		t.Fatalf("scanEventRow returned error: %v", err)
	}
	if string(row.Payload) != `"plain-text"` {
		t.Fatalf("unexpected wrapped payload: %s", row.Payload)
	}
}
