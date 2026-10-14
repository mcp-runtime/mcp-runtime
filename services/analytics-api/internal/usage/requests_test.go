package usage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminRequestFiltersAreParameterizedAndCombined(t *testing.T) {
	scope := QueryScope{Since: time.Unix(100, 0), Until: time.Unix(200, 0), Admin: true, Namespaces: []string{"mcp-team-a"}, TeamIDs: []string{"team-a"}, RPCMethod: "tools/call' OR 1=1 --", ToolName: "echo", PolicyVersion: "v1", HTTPStatus: "5xx", Scope: "tenant"}
	where, args := WhereClause(scope)
	if !strings.Contains(where, "namespace IN (?) AND JSONExtractString(payload, 'team_id') IN (?)") {
		t.Fatal("admin namespace/team filters must intersect:", where)
	}
	if !strings.Contains(where, "JSONExtractString(payload, 'rpc_method') = ?") || strings.Contains(where, scope.RPCMethod) {
		t.Fatal("unbound method filter:", where)
	}
	if !strings.Contains(where, "endsWith(source, '-gateway') OR JSONExtractString(payload, 'rpc_method') != ''") || !strings.Contains(where, "namespace NOT IN") || !strings.Contains(where, "timestamp < ?") {
		t.Fatal("missing event/window/scope filter:", where)
	}
	if len(args) < 10 {
		t.Fatal("missing bound filter values", args)
	}
}

func TestPublicScopeDoesNotIncludeOrganizationCatalog(t *testing.T) {
	_, args := WhereClause(QueryScope{Scope: "public"})
	found := false
	for _, v := range args {
		if v == "mcp-servers-org" {
			t.Fatal("org catalog became public")
		}
		if v == "mcp-servers-public" {
			found = true
		}
	}
	if !found {
		t.Fatal("public catalog filter missing")
	}
}

func TestUsageRejectsInvalidFiltersBeforeDatabaseRead(t *testing.T) {
	for _, query := range []string{"scope=administrator", "http_status=500%20OR%201=1", "window_hours=0", "window_hours=90000", "window_hours=abc"} {
		response := httptest.NewRecorder()
		(&Service{}).HandleAdminUsage(response, httptest.NewRequest(http.MethodGet, "/analytics/usage?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", query, response.Code)
		}
	}
}

func TestSeriesFillsGapsAndUsesActualPartialBucketDuration(t *testing.T) {
	start := time.Date(2026, 10, 14, 10, 0, 30, 0, time.UTC)
	scope := QueryScope{Since: start, Until: start.Add(2 * time.Minute)}
	points := []TimePoint{{Bucket: start.Truncate(time.Minute), Events: 30}, {Bucket: start.Add(2 * time.Minute).Truncate(time.Minute), Events: 60}}
	series := fillTimeSeries(points, scope)
	if len(series) != 3 || series[0].RequestsPerSecond != 1 || series[1].Events != 0 || series[1].RequestsPerSecond != 0 || series[2].RequestsPerSecond != 2 {
		t.Fatalf("partial buckets/gap rate incorrect: %+v", series)
	}
	for i := 1; i < len(series); i++ {
		if series[i].Bucket.Sub(series[i-1].Bucket) != time.Minute {
			t.Fatal("uneven time axis")
		}
	}
}

func TestRequestGroupsKeepMethodToolAndNamespaceDistinct(t *testing.T) {
	query := RequestUsageQuery("mcp", "WHERE timestamp >= ?")
	for _, part := range []string{"GROUP BY server, namespace, team_id, rpc_method, tool_name, decision, policy_version", "argMax(trace_id, timestamp)", "quantile(0.95)", "LIMIT ?"} {
		if !strings.Contains(query, part) {
			t.Fatalf("missing %q in request grouping", part)
		}
	}
}
