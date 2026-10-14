package usage

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/pkg/platformmode"
	"mcp-runtime/pkg/publishscope"
)

// RequestUsage groups actual gateway events; no tool arguments or results are returned.
type RequestUsage struct {
	Server        string    `json:"server"`
	Namespace     string    `json:"namespace"`
	TeamID        string    `json:"team_id,omitempty"`
	RPCMethod     string    `json:"rpc_method"`
	ToolName      string    `json:"tool_name,omitempty"`
	Decision      string    `json:"decision"`
	PolicyVersion string    `json:"policy_version,omitempty"`
	Events        uint64    `json:"events"`
	HTTPFailures  uint64    `json:"http_failures"`
	P95LatencyMS  float64   `json:"p95_latency_ms"`
	LastSeen      time.Time `json:"last_seen"`
	TraceID       string    `json:"trace_id,omitempty"`
}

type UsageFacets struct {
	Servers        []string `json:"servers"`
	Methods        []string `json:"methods"`
	Tools          []string `json:"tools"`
	PolicyVersions []string `json:"policy_versions"`
}

func validateUsageFilters(r *http.Request) error {
	q := r.URL.Query()
	for _, key := range []string{"server", "namespace", "team_id", "tool_name", "rpc_method", "policy_version"} {
		if len(q.Get(key)) > 256 {
			return fmt.Errorf("%s must be at most 256 characters", key)
		}
	}
	if v := q.Get("window_hours"); v != "" {
		hours, err := strconv.Atoi(v)
		if err != nil || hours < 1 || hours > maxWindowDays*24 {
			return fmt.Errorf("window_hours must be between 1 and %d", maxWindowDays*24)
		}
	}
	switch q.Get("scope") {
	case "", "public", "org", "tenant":
	default:
		return fmt.Errorf("scope must be public, org, or tenant")
	}
	switch q.Get("http_status") {
	case "", "2xx", "4xx", "5xx":
	default:
		return fmt.Errorf("http_status must be 2xx, 4xx, or 5xx")
	}
	return nil
}

func catalogScopeNamespaces(scope publishscope.Scope) []string {
	names := platformmode.ModePublic.CatalogNamespacesForScope(scope)
	names = append(names, platformmode.FromEnv().CatalogNamespacesForScope(scope)...)
	if scope == publishscope.Public {
		names = append(names, platformmode.DefaultPublicCatalogNamespace)
	}
	if scope == publishscope.Org {
		names = append(names, platformmode.DefaultOrgCatalogNamespace)
	}
	return dedupeStrings(names)
}

func appendRequestFilters(scope QueryScope, conditions []string, args []any) ([]string, []any) {
	if scope.Admin {
		// The operator uses "<server-name>-gateway" by default, while a
		// server may configure a custom source. Gateway requests carry their
		// RPC method in the payload, so accept either signal.
		conditions = append(conditions, "(endsWith(source, '-gateway') OR JSONExtractString(payload, 'rpc_method') != '')")
	}
	for _, filter := range []struct{ expression, value string }{
		{"JSONExtractString(payload, 'rpc_method')", scope.RPCMethod},
		{"JSONExtractString(payload, 'policy_version')", scope.PolicyVersion},
	} {
		if filter.value != "" {
			conditions = append(conditions, filter.expression+" = ?")
			args = append(args, filter.value)
		}
	}
	if scope.HTTPStatus != "" {
		// The public handler validates the class before this query is built.
		if lower, err := strconv.Atoi(scope.HTTPStatus[:1]); err == nil {
			conditions = append(conditions, "JSONExtractInt(payload, 'status') >= ? AND JSONExtractInt(payload, 'status') < ?")
			args = append(args, lower*100, (lower+1)*100)
		}
	}
	switch scope.Scope {
	case "public", "org":
		names := catalogScopeNamespaces(publishscope.Scope(scope.Scope))
		conditions = append(conditions, "namespace IN "+sqlPlaceholders(len(names)))
		args = appendStringArgs(args, names)
	case "tenant":
		names := dedupeStrings(append(catalogScopeNamespaces(publishscope.Public), catalogScopeNamespaces(publishscope.Org)...))
		conditions = append(conditions, "namespace NOT IN "+sqlPlaceholders(len(names)), "(team_id != '' OR startsWith(namespace, 'mcp-team-'))")
		args = appendStringArgs(args, names)
	}
	return conditions, args
}

func RequestUsageQuery(dbName, where string) string {
	return "SELECT server, namespace, team_id, JSONExtractString(payload, 'rpc_method') AS rpc_method, tool_name, decision, JSONExtractString(payload, 'policy_version') AS policy_version, count() AS events, countIf(JSONExtractInt(payload, 'status') >= 400) AS http_failures, quantile(0.95)(toFloat64(JSONExtractInt(payload, 'latency_ms'))) AS p95_latency_ms, max(timestamp) AS last_seen, argMax(trace_id, timestamp) AS trace_id FROM " + dbName + ".events " + where + " GROUP BY server, namespace, team_id, rpc_method, tool_name, decision, policy_version ORDER BY events DESC, last_seen DESC, namespace, server, rpc_method, tool_name, decision, policy_version LIMIT ?"
}

func (s *Service) queryRequestUsage(ctx context.Context, scope QueryScope) ([]RequestUsage, error) {
	where, args := WhereClause(scope, "server != ''")
	rows, err := s.DB.Query(ctx, RequestUsageQuery(s.DBName, where), append(args, scope.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RequestUsage, 0, scope.Limit)
	for rows.Next() {
		var row RequestUsage
		if err := rows.Scan(&row.Server, &row.Namespace, &row.TeamID, &row.RPCMethod, &row.ToolName, &row.Decision, &row.PolicyVersion, &row.Events, &row.HTTPFailures, &row.P95LatencyMS, &row.LastSeen, &row.TraceID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) queryFacets(ctx context.Context, scope QueryScope) (*UsageFacets, error) {
	// Preserve choices while narrowing a method/tool/server filter. Window and
	// ownership scope still bound the values, and arrays have an explicit cap.
	scope.Server, scope.RPCMethod, scope.ToolName, scope.Decision, scope.PolicyVersion, scope.HTTPStatus = "", "", "", "", "", ""
	where, args := WhereClause(scope)
	query := "SELECT groupUniqArray(200)(server), groupUniqArray(200)(JSONExtractString(payload, 'rpc_method')), groupUniqArray(200)(tool_name), groupUniqArray(200)(JSONExtractString(payload, 'policy_version')) FROM " + s.DBName + ".events " + where
	var facets UsageFacets
	if err := s.DB.QueryRow(ctx, query, args...).Scan(&facets.Servers, &facets.Methods, &facets.Tools, &facets.PolicyVersions); err != nil {
		return nil, err
	}
	for _, values := range []*[]string{&facets.Servers, &facets.Methods, &facets.Tools, &facets.PolicyVersions} {
		clean := (*values)[:0]
		for _, v := range *values {
			if strings.TrimSpace(v) != "" {
				clean = append(clean, v)
			}
		}
		sort.Strings(clean)
		*values = clean
	}
	return &facets, nil
}

func seriesBucketSeconds(scope QueryScope) int {
	duration := scope.Until.Sub(scope.Since)
	if scope.Until.IsZero() {
		duration = time.Duration(scope.WindowDays) * 24 * time.Hour
	}
	switch {
	case duration <= 6*time.Hour:
		return 60
	case duration <= 48*time.Hour:
		return 3600
	default:
		return 86400
	}
}

func fillTimeSeries(points []TimePoint, scope QueryScope) []TimePoint {
	if scope.Until.IsZero() || !scope.Until.After(scope.Since) {
		return points
	}
	step := time.Duration(seriesBucketSeconds(scope)) * time.Second
	byBucket := make(map[int64]TimePoint, len(points))
	for _, p := range points {
		byBucket[p.Bucket.UTC().Truncate(step).Unix()] = p
	}
	out := make([]TimePoint, 0, int(scope.Until.Sub(scope.Since)/step)+1)
	for bucket := scope.Since.UTC().Truncate(step); bucket.Before(scope.Until); bucket = bucket.Add(step) {
		p := byBucket[bucket.Unix()]
		p.Bucket = bucket
		start, end := bucket, bucket.Add(step)
		if start.Before(scope.Since) {
			start = scope.Since
		}
		if end.After(scope.Until) {
			end = scope.Until
		}
		p.RequestsPerSecond = float64(p.Events) / end.Sub(start).Seconds()
		out = append(out, p)
	}
	return out
}
