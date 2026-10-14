package usage

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"mcp-analytics-api/internal/identity"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/platformauth"
)

const (
	sharedCatalogNamespace = mcpdefaults.MCPServersNamespace
	defaultWindowDays      = 30
	maxWindowDays          = 365
	teamIDExpression       = "JSONExtractString(payload, 'team_id')"
)

type Service struct {
	DB       clickhouse.Conn
	DBName   string
	Resolver identity.Resolver
}

type ServerUsage struct {
	Server       string    `json:"server"`
	Namespace    string    `json:"namespace"`
	TeamID       string    `json:"team_id,omitempty"`
	Events       uint64    `json:"events"`
	Allowed      uint64    `json:"allowed"`
	Denied       uint64    `json:"denied"`
	UniqueHumans uint64    `json:"unique_humans"`
	UniqueAgents uint64    `json:"unique_agents"`
	LastSeen     time.Time `json:"last_seen"`
}

type ActorUsage struct {
	HumanID       string    `json:"human_id"`
	AgentID       string    `json:"agent_id"`
	Events        uint64    `json:"events"`
	UniqueServers uint64    `json:"unique_servers"`
	UniqueTools   uint64    `json:"unique_tools"`
	Denied        uint64    `json:"denied"`
	LastSeen      time.Time `json:"last_seen"`
}

type ToolUsage struct {
	Server   string    `json:"server"`
	ToolName string    `json:"tool_name"`
	HumanID  string    `json:"human_id"`
	TeamID   string    `json:"team_id"`
	AgentID  string    `json:"agent_id"`
	Events   uint64    `json:"events"`
	Denied   uint64    `json:"denied"`
	LastSeen time.Time `json:"last_seen"`
}

type DecisionUsage struct {
	Decision string `json:"decision"`
	Events   uint64 `json:"events"`
}

type TimePoint struct {
	Bucket            time.Time `json:"bucket"`
	Events            uint64    `json:"events"`
	Allowed           uint64    `json:"allowed"`
	Denied            uint64    `json:"denied"`
	RequestsPerSecond float64   `json:"requests_per_second"`
}

type RecentActivity struct {
	Timestamp time.Time `json:"timestamp"`
	Server    string    `json:"server,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	TeamID    string    `json:"team_id,omitempty"`
	HumanID   string    `json:"human_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Decision  string    `json:"decision,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	EventType string    `json:"event_type,omitempty"`
}

type Totals struct {
	Events           uint64  `json:"events"`
	Allowed          uint64  `json:"allowed"`
	Denied           uint64  `json:"denied"`
	UniqueServers    uint64  `json:"unique_servers"`
	UniqueHumans     uint64  `json:"unique_humans"`
	UniqueAgents     uint64  `json:"unique_agents"`
	UniqueSessions   uint64  `json:"unique_sessions"`
	HTTPFailures     uint64  `json:"http_failures"`
	ServerErrors     uint64  `json:"server_errors"`
	AverageLatencyMS float64 `json:"average_latency_ms"`
	P95LatencyMS     float64 `json:"p95_latency_ms"`
	BytesIn          uint64  `json:"bytes_in"`
	BytesOut         uint64  `json:"bytes_out"`
}

type UsageResponse struct {
	Totals        Totals           `json:"totals"`
	Servers       []ServerUsage    `json:"servers"`
	Actors        []ActorUsage     `json:"actors"`
	Tools         []ToolUsage      `json:"tools"`
	Decisions     []DecisionUsage  `json:"decisions"`
	Series        []TimePoint      `json:"series,omitempty"`
	Recent        []RecentActivity `json:"recent,omitempty"`
	WindowDays    int              `json:"window_days"`
	Filters       UsageFilters     `json:"filters,omitempty"`
	Requests      []RequestUsage   `json:"requests,omitempty"`
	Facets        *UsageFacets     `json:"facets,omitempty"`
	BucketSeconds int              `json:"bucket_seconds"`
	WindowStart   time.Time        `json:"window_start"`
	WindowEnd     time.Time        `json:"window_end"`
}

type UsageFilters struct {
	Namespaces    []string `json:"namespaces,omitempty"`
	TeamIDs       []string `json:"team_ids,omitempty"`
	Server        string   `json:"server,omitempty"`
	Decision      string   `json:"decision,omitempty"`
	ToolName      string   `json:"tool_name,omitempty"`
	RPCMethod     string   `json:"rpc_method,omitempty"`
	PolicyVersion string   `json:"policy_version,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	HTTPStatus    string   `json:"http_status,omitempty"`
}

type QueryScope struct {
	Since         time.Time
	WindowDays    int
	Limit         int
	Namespaces    []string
	TeamIDs       []string
	Server        string
	Decision      string
	ToolName      string
	Until         time.Time
	RPCMethod     string
	PolicyVersion string
	Scope         string
	HTTPStatus    string
	Admin         bool
}

type PrincipalScope struct {
	Namespaces []string
	TeamIDs    []string
}

func scopeFromRequest(r *http.Request, windowDays, limit int) QueryScope {
	decision := strings.TrimSpace(r.URL.Query().Get("decision"))
	if decision == "" {
		decision = strings.TrimSpace(r.URL.Query().Get("status"))
	}
	if decision == "" {
		decision = strings.TrimSpace(r.URL.Query().Get("outcome"))
	}
	toolName := strings.TrimSpace(r.URL.Query().Get("tool_name"))
	if toolName == "" {
		toolName = strings.TrimSpace(r.URL.Query().Get("tool"))
	}
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -windowDays)
	if hours := queryInt(r, "window_hours", 0); hours > 0 {
		since = now.Add(-time.Duration(clampInt(hours, 1, maxWindowDays*24)) * time.Hour)
	}
	return QueryScope{
		Since:         since,
		Until:         now,
		WindowDays:    windowDays,
		Limit:         limit,
		Server:        strings.TrimSpace(r.URL.Query().Get("server")),
		Decision:      decision,
		ToolName:      toolName,
		RPCMethod:     strings.TrimSpace(r.URL.Query().Get("rpc_method")),
		PolicyVersion: strings.TrimSpace(r.URL.Query().Get("policy_version")),
		HTTPStatus:    strings.TrimSpace(r.URL.Query().Get("http_status")),
	}
}

func applyAdminScopeFilters(r *http.Request, scope *QueryScope) {
	scope.Admin = true
	scope.Scope = strings.TrimSpace(r.URL.Query().Get("scope"))
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace != "" {
		scope.Namespaces = []string{namespace}
	}
	teamID := strings.TrimSpace(r.URL.Query().Get("team_id"))
	if teamID != "" {
		scope.TeamIDs = []string{teamID}
	}
}

func PrincipalOwnedScope(p platformauth.Principal) PrincipalScope {
	var scope PrincipalScope
	addNamespace := func(namespace string) {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" || namespace == sharedCatalogNamespace {
			return
		}
		scope.Namespaces = append(scope.Namespaces, namespace)
	}
	addNamespace(p.Namespace)
	for _, team := range p.Teams {
		addNamespace(team.Namespace)
		if teamID := strings.TrimSpace(team.ID); teamID != "" {
			scope.TeamIDs = append(scope.TeamIDs, teamID)
		}
	}
	for _, namespace := range p.AllowedNamespaces {
		addNamespace(namespace)
	}
	scope.Namespaces = dedupeStrings(scope.Namespaces)
	scope.TeamIDs = dedupeStrings(scope.TeamIDs)
	return scope
}

func principalScopeForNamespace(p platformauth.Principal, namespace string) (PrincipalScope, bool) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || namespace == sharedCatalogNamespace {
		return PrincipalScope{}, false
	}
	if strings.TrimSpace(p.Namespace) == namespace {
		return PrincipalScope{Namespaces: []string{namespace}}, true
	}
	for _, team := range p.Teams {
		if strings.TrimSpace(team.Namespace) == namespace {
			scope := PrincipalScope{Namespaces: []string{namespace}}
			if teamID := strings.TrimSpace(team.ID); teamID != "" {
				scope.TeamIDs = []string{teamID}
			}
			return scope, true
		}
	}
	for _, allowed := range p.AllowedNamespaces {
		if strings.TrimSpace(allowed) == namespace {
			return PrincipalScope{Namespaces: []string{namespace}}, true
		}
	}
	return PrincipalScope{}, false
}

func emptyUsageResponse(scope QueryScope) UsageResponse {
	return UsageResponse{
		Servers:    []ServerUsage{},
		Actors:     []ActorUsage{},
		Tools:      []ToolUsage{},
		Decisions:  []DecisionUsage{},
		Series:     []TimePoint{},
		Recent:     []RecentActivity{},
		WindowDays: scope.WindowDays,
		Filters:    scope.Filters(),
	}
}

func (scope QueryScope) Filters() UsageFilters {
	return UsageFilters{
		Namespaces:    dedupeStrings(scope.Namespaces),
		TeamIDs:       dedupeStrings(scope.TeamIDs),
		Server:        scope.Server,
		Decision:      scope.Decision,
		ToolName:      scope.ToolName,
		RPCMethod:     scope.RPCMethod,
		PolicyVersion: scope.PolicyVersion,
		Scope:         scope.Scope,
		HTTPStatus:    scope.HTTPStatus,
	}
}

func (s *Service) queryUsage(parent context.Context, scope QueryScope) (UsageResponse, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		totals    Totals
		servers   []ServerUsage
		actors    []ActorUsage
		tools     []ToolUsage
		decisions []DecisionUsage
		series    []TimePoint
		recent    []RecentActivity

		wg          sync.WaitGroup
		errOnce     sync.Once
		firstErr    error
		firstErrKey string
		requests    []RequestUsage
		facets      *UsageFacets
	)

	recordErr := func(key string, err error) {
		if err == nil {
			return
		}
		errOnce.Do(func() {
			firstErr = err
			firstErrKey = key
			cancel()
		})
	}

	wg.Add(7)
	if scope.Admin {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var err error
			requests, err = s.queryRequestUsage(ctx, scope)
			recordErr("requests", err)
		}()
		go func() {
			defer wg.Done()
			var err error
			facets, err = s.queryFacets(ctx, scope)
			recordErr("facets", err)
		}()
	}
	go func() {
		defer wg.Done()
		var err error
		totals, err = s.queryTotals(ctx, scope)
		recordErr("totals", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		servers, err = s.queryServers(ctx, scope)
		recordErr("servers", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		actors, err = s.queryActors(ctx, scope)
		recordErr("actors", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		tools, err = s.queryTools(ctx, scope)
		recordErr("tools", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		decisions, err = s.queryDecisions(ctx, scope)
		recordErr("decisions", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		series, err = s.querySeries(ctx, scope)
		recordErr("series", err)
	}()
	go func() {
		defer wg.Done()
		var err error
		recent, err = s.queryRecent(ctx, scope)
		recordErr("recent", err)
	}()
	wg.Wait()

	if firstErr != nil {
		return UsageResponse{}, fmt.Errorf("%s: %w", firstErrKey, firstErr)
	}
	series = fillTimeSeries(series, scope)

	return UsageResponse{
		Totals:        totals,
		Servers:       servers,
		Actors:        actors,
		Tools:         tools,
		Decisions:     decisions,
		Series:        series,
		Recent:        recent,
		WindowDays:    scope.WindowDays,
		Filters:       scope.Filters(),
		Requests:      requests,
		Facets:        facets,
		BucketSeconds: seriesBucketSeconds(scope),
		WindowStart:   scope.Since,
		WindowEnd:     scope.Until,
	}, nil
}

func (s *Service) queryTotals(ctx context.Context, scope QueryScope) (Totals, error) {
	where, args := WhereClause(scope)
	query := "SELECT count(), countIf(decision = 'allow'), countIf(decision = 'deny'), uniqIf(server, server != ''), uniqIf(human_id, human_id != ''), uniqIf(agent_id, agent_id != ''), uniqIf(session_id, session_id != ''), countIf(JSONExtractInt(payload, 'status') >= 400), countIf(JSONExtractInt(payload, 'status') >= 500), if(count() = 0, 0., avg(toFloat64(JSONExtractInt(payload, 'latency_ms')))), if(count() = 0, 0., quantile(0.95)(toFloat64(JSONExtractInt(payload, 'latency_ms')))), sum(toUInt64(greatest(JSONExtractInt(payload, 'bytes_in'), 0))), sum(toUInt64(greatest(JSONExtractInt(payload, 'bytes_out'), 0))) FROM " + s.DBName + ".events " + where
	var totals Totals
	err := s.DB.QueryRow(ctx, query, args...).Scan(
		&totals.Events,
		&totals.Allowed,
		&totals.Denied,
		&totals.UniqueServers,
		&totals.UniqueHumans,
		&totals.UniqueAgents,
		&totals.UniqueSessions,
		&totals.HTTPFailures, &totals.ServerErrors, &totals.AverageLatencyMS, &totals.P95LatencyMS, &totals.BytesIn, &totals.BytesOut,
	)
	return totals, err
}

func (s *Service) queryServers(ctx context.Context, scope QueryScope) ([]ServerUsage, error) {
	where, args := WhereClause(scope, "server != ''")
	query := ServersQuery(s.DBName, where)
	args = append(args, scope.Limit)
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ServerUsage, 0, scope.Limit)
	for rows.Next() {
		var row ServerUsage
		if err := rows.Scan(&row.Server, &row.Namespace, &row.TeamID, &row.Events, &row.Allowed, &row.Denied, &row.UniqueHumans, &row.UniqueAgents, &row.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func ServersQuery(dbName, where string) string {
	return "SELECT server, namespace, " + teamIDExpression + " AS team_id, count() AS events, countIf(decision = 'allow') AS allowed, countIf(decision = 'deny') AS denied, uniqIf(human_id, human_id != '') AS unique_humans, uniqIf(agent_id, agent_id != '') AS unique_agents, max(timestamp) AS last_seen FROM " + dbName + ".events " + where + " GROUP BY server, namespace, team_id ORDER BY events DESC LIMIT ?"
}

func (s *Service) queryActors(ctx context.Context, scope QueryScope) ([]ActorUsage, error) {
	where, args := WhereClause(scope, "(human_id != '' OR agent_id != '')")
	query := "SELECT human_id, agent_id, count() AS events, uniqIf(server, server != '') AS unique_servers, uniqIf(tool_name, tool_name != '') AS unique_tools, countIf(decision = 'deny') AS denied, max(timestamp) AS last_seen FROM " + s.DBName + ".events " + where + " GROUP BY human_id, agent_id ORDER BY events DESC LIMIT ?"
	args = append(args, scope.Limit)
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ActorUsage, 0, scope.Limit)
	for rows.Next() {
		var row ActorUsage
		if err := rows.Scan(&row.HumanID, &row.AgentID, &row.Events, &row.UniqueServers, &row.UniqueTools, &row.Denied, &row.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) queryTools(ctx context.Context, scope QueryScope) ([]ToolUsage, error) {
	where, args := WhereClause(scope, "tool_name != ''")
	// Group by server+tool+caller so each (tool, user, agent) combination is a
	// separate row — gives operators a clear picture of who called what and how many times.
	query := "SELECT server, tool_name, human_id, team_id, agent_id, count() AS events, countIf(decision = 'deny') AS denied, max(timestamp) AS last_seen FROM " + s.DBName + ".events " + where + " GROUP BY server, tool_name, human_id, team_id, agent_id ORDER BY events DESC LIMIT ?"
	args = append(args, scope.Limit)
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ToolUsage, 0, scope.Limit)
	for rows.Next() {
		var row ToolUsage
		if err := rows.Scan(&row.Server, &row.ToolName, &row.HumanID, &row.TeamID, &row.AgentID, &row.Events, &row.Denied, &row.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resolve UUID → human-readable names in a single batch query each.
	// human_id is a user UUID (JWT sub); team_id is the team UUID.
	// IDs that look like emails or slugs (non-UUID strings) are kept as-is.
	if s.Resolver != nil && len(out) > 0 {
		uniqueHumans := collectUniqueIDs(out, func(r ToolUsage) string { return r.HumanID })
		uniqueTeams := collectUniqueIDs(out, func(r ToolUsage) string { return r.TeamID })

		userNames, _ := s.Resolver.ResolveUserIDs(ctx, uniqueHumans)
		teamNames, _ := s.Resolver.ResolveTeamIDs(ctx, uniqueTeams)

		for i := range out {
			if v, ok := userNames[out[i].HumanID]; ok {
				out[i].HumanID = v
			}
			if v, ok := teamNames[out[i].TeamID]; ok {
				out[i].TeamID = v
			}
		}
	}

	return out, nil
}

// collectUniqueIDs returns deduplicated non-empty string values from rows.
func collectUniqueIDs(rows []ToolUsage, fn func(ToolUsage) string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range rows {
		v := fn(r)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (s *Service) queryDecisions(ctx context.Context, scope QueryScope) ([]DecisionUsage, error) {
	where, args := WhereClause(scope)
	query := "SELECT if(decision = '', 'unknown', decision) AS decision_label, count() AS events FROM " + s.DBName + ".events " + where + " GROUP BY decision_label ORDER BY events DESC"
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DecisionUsage, 0)
	for rows.Next() {
		var row DecisionUsage
		if err := rows.Scan(&row.Decision, &row.Events); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) querySeries(ctx context.Context, scope QueryScope) ([]TimePoint, error) {
	where, args := WhereClause(scope)
	query := "SELECT toStartOfInterval(timestamp, INTERVAL " + fmt.Sprint(seriesBucketSeconds(scope)) + " SECOND) AS bucket, count() AS events, countIf(decision = 'allow') AS allowed, countIf(decision = 'deny') AS denied FROM " + s.DBName + ".events " + where + " GROUP BY bucket ORDER BY bucket ASC"
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TimePoint, 0)
	for rows.Next() {
		var row TimePoint
		if err := rows.Scan(&row.Bucket, &row.Events, &row.Allowed, &row.Denied); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) queryRecent(ctx context.Context, scope QueryScope) ([]RecentActivity, error) {
	where, args := WhereClause(scope)
	query := "SELECT timestamp, server, namespace, " + teamIDExpression + " AS team_id, human_id, agent_id, session_id, decision, tool_name, event_type FROM " + s.DBName + ".events " + where + " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, scope.Limit)
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentActivity, 0, scope.Limit)
	for rows.Next() {
		var row RecentActivity
		if err := rows.Scan(&row.Timestamp, &row.Server, &row.Namespace, &row.TeamID, &row.HumanID, &row.AgentID, &row.SessionID, &row.Decision, &row.ToolName, &row.EventType); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func WhereClause(scope QueryScope, extraConditions ...string) (string, []any) {
	conditions := []string{"timestamp >= ?"}
	args := []any{scope.Since}
	if !scope.Until.IsZero() {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, scope.Until)
	}

	scopeFilters, scopeArgs := scopeConditions(scope)
	if len(scopeFilters) > 0 {
		join := " OR "
		if scope.Admin {
			join = " AND "
		}
		conditions = append(conditions, "("+strings.Join(scopeFilters, join)+")")
		args = append(args, scopeArgs...)
	}
	if scope.Server != "" {
		conditions = append(conditions, "server = ?")
		args = append(args, scope.Server)
	}
	if scope.Decision != "" {
		conditions = append(conditions, "decision = ?")
		args = append(args, scope.Decision)
	}
	if scope.ToolName != "" {
		conditions = append(conditions, "tool_name = ?")
		args = append(args, scope.ToolName)
	}
	conditions, args = appendRequestFilters(scope, conditions, args)
	for _, condition := range extraConditions {
		if condition = strings.TrimSpace(condition); condition != "" {
			conditions = append(conditions, condition)
		}
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func scopeConditions(scope QueryScope) ([]string, []any) {
	var conditions []string
	var args []any
	namespaces := dedupeStrings(scope.Namespaces)
	if len(namespaces) > 0 {
		conditions = append(conditions, "namespace IN "+sqlPlaceholders(len(namespaces)))
		args = appendStringArgs(args, namespaces)
	}
	teamIDs := dedupeStrings(scope.TeamIDs)
	if len(teamIDs) > 0 {
		conditions = append(conditions, teamIDExpression+" IN "+sqlPlaceholders(len(teamIDs)))
		args = appendStringArgs(args, teamIDs)
	}
	return conditions, args
}

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return "()"
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func appendStringArgs(args []any, values []string) []any {
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
