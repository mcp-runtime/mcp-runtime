package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventRow represents a single event from ClickHouse.
type EventRow struct {
	Timestamp time.Time       `json:"timestamp"`
	TraceID   string          `json:"trace_id,omitempty"`
	Source    string          `json:"source"`
	EventType string          `json:"event_type"`
	Server    string          `json:"server,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	TeamID    string          `json:"team_id,omitempty"`
	Cluster   string          `json:"cluster,omitempty"`
	HumanID   string          `json:"human_id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Decision  string          `json:"decision,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// SourceStat represents event counts by source.
type SourceStat struct {
	Source string `json:"source"`
	Count  uint64 `json:"count"`
}

// EventTypeStat represents event counts by event type.
type EventTypeStat struct {
	EventType string `json:"event_type"`
	Count     uint64 `json:"count"`
}

// DashboardSummary provides overview statistics for the dashboard.
type DashboardSummary struct {
	TotalEvents    uint64 `json:"total_events"`
	ActiveServers  int    `json:"active_servers"`
	ActiveGrants   int    `json:"active_grants"`
	ActiveSessions int    `json:"active_sessions"`
	LatestSource   string `json:"latest_source"`
	LastEventType  string `json:"last_event_type"`
	LastEventTime  string `json:"last_event_time,omitempty"`
}

const (
	eventSelectColumns = "timestamp, trace_id, source, event_type, server, namespace, team_id, cluster, human_id, agent_id, session_id, decision, tool_name, payload"
	maxEventOffset     = 100000
)

// RowScanner abstracts row scanning for testability.
type RowScanner interface {
	Scan(dest ...any) error
}

// QueryEvents returns events from ClickHouse with limit and offset.
func (c *Client) QueryEvents(ctx context.Context, limit, offset int) ([]EventRow, error) {
	limit = normalizeEventLimit(limit)
	offset = normalizeEventOffset(offset)

	query := fmt.Sprintf("SELECT %s FROM %s.events ORDER BY timestamp DESC LIMIT %d OFFSET %d",
		eventSelectColumns, c.DBName, limit, offset)
	rows, err := c.Conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	events := make([]EventRow, 0, limit)
	for rows.Next() {
		var row EventRow
		if err := scanEventRow(rows, &row); err != nil {
			return nil, fmt.Errorf("failed to scan event row: %w", err)
		}
		events = append(events, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	return events, nil
}

// QueryStats returns total event count.
func (c *Client) QueryStats(ctx context.Context) (uint64, error) {
	query := fmt.Sprintf("SELECT count() FROM %s.events", c.DBName)
	row := c.Conn.QueryRow(ctx, query)
	var count uint64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to query stats: %w", err)
	}
	return count, nil
}

// QuerySources returns event counts grouped by source.
func (c *Client) QuerySources(ctx context.Context) ([]SourceStat, error) {
	query := fmt.Sprintf("SELECT source, count() as count FROM %s.events GROUP BY source ORDER BY count DESC", c.DBName)
	rows, err := c.Conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query sources: %w", err)
	}
	defer rows.Close()

	var sources []SourceStat
	for rows.Next() {
		var stat SourceStat
		if err := rows.Scan(&stat.Source, &stat.Count); err != nil {
			return nil, fmt.Errorf("failed to scan source: %w", err)
		}
		sources = append(sources, stat)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sources: %w", err)
	}

	return sources, nil
}

// QueryEventTypes returns event counts grouped by event type.
func (c *Client) QueryEventTypes(ctx context.Context) ([]EventTypeStat, error) {
	query := fmt.Sprintf("SELECT event_type, count() as count FROM %s.events GROUP BY event_type ORDER BY count DESC", c.DBName)
	rows, err := c.Conn.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query event types: %w", err)
	}
	defer rows.Close()

	var eventTypes []EventTypeStat
	for rows.Next() {
		var stat EventTypeStat
		if err := rows.Scan(&stat.EventType, &stat.Count); err != nil {
			return nil, fmt.Errorf("failed to scan event type: %w", err)
		}
		eventTypes = append(eventTypes, stat)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating event types: %w", err)
	}

	return eventTypes, nil
}

// EventFilters provides filtering options for events.
type EventFilters struct {
	TraceID                 string
	Source                  string
	EventType               string
	Server                  string
	Namespace               string
	TeamID                  string
	Cluster                 string
	HumanID                 string
	AgentID                 string
	SessionID               string
	Decision                string
	ToolName                string
	Reason                  string
	MatchedGrant            string
	MatchedGrantNamespace   string
	MatchedSession          string
	MatchedSessionNamespace string
	Since                   time.Time
	Until                   time.Time
	Limit                   int
	Offset                  int
}

// QueryEventsFiltered returns events filtered by various fields.
func (c *Client) QueryEventsFiltered(ctx context.Context, filters EventFilters) ([]EventRow, error) {
	filters.Limit = normalizeEventLimit(filters.Limit)
	filters.Offset = normalizeEventOffset(filters.Offset)

	whereClause, args := buildEventFilterWhereClause(filters)

	query := buildEventFilterQuery(c.DBName, whereClause, filters.Limit, filters.Offset)

	rows, err := c.Conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query filtered events: %w", err)
	}
	defer rows.Close()

	events := make([]EventRow, 0, filters.Limit)
	for rows.Next() {
		var row EventRow
		if err := scanEventRow(rows, &row); err != nil {
			return nil, fmt.Errorf("failed to scan event row: %w", err)
		}
		events = append(events, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	return events, nil
}

func buildEventFilterWhereClause(filters EventFilters) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	if filters.TraceID != "" {
		conditions = append(conditions, "trace_id = ?")
		args = append(args, filters.TraceID)
	}
	if filters.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, filters.Source)
	}
	if filters.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, filters.EventType)
	}
	if filters.Server != "" {
		conditions = append(conditions, "server = ?")
		args = append(args, filters.Server)
	}
	if filters.Namespace != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, filters.Namespace)
	}
	if filters.TeamID != "" {
		conditions = append(conditions, "team_id = ?")
		args = append(args, filters.TeamID)
	}
	if filters.Cluster != "" {
		conditions = append(conditions, "cluster = ?")
		args = append(args, filters.Cluster)
	}
	if filters.HumanID != "" {
		conditions = append(conditions, "human_id = ?")
		args = append(args, filters.HumanID)
	}
	if filters.AgentID != "" {
		conditions = append(conditions, "agent_id = ?")
		args = append(args, filters.AgentID)
	}
	if filters.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, filters.SessionID)
	}
	if filters.Decision != "" {
		conditions = append(conditions, "decision = ?")
		args = append(args, filters.Decision)
	}
	if filters.ToolName != "" {
		conditions = append(conditions, "tool_name = ?")
		args = append(args, filters.ToolName)
	}
	if filters.Reason != "" {
		conditions = append(conditions, "JSONExtractString(payload, 'reason') = ?")
		args = append(args, filters.Reason)
	}
	for _, filter := range []struct{ field, value string }{
		{"matched_grant", filters.MatchedGrant}, {"matched_grant_namespace", filters.MatchedGrantNamespace},
		{"matched_session", filters.MatchedSession}, {"matched_session_namespace", filters.MatchedSessionNamespace},
	} {
		if filter.value != "" {
			conditions = append(conditions, "JSONExtractString(payload, '"+filter.field+"') = ?")
			args = append(args, filter.value)
		}
	}
	if !filters.Since.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, filters.Since.UTC())
	}
	if !filters.Until.IsZero() {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, filters.Until.UTC())
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + joinConditions(conditions, " AND ")
	}
	return whereClause, args
}

func buildEventFilterQuery(dbName, whereClause string, limit, offset int) string {
	offset = normalizeEventOffset(offset)
	return fmt.Sprintf("SELECT %s FROM %s.events %s ORDER BY timestamp DESC LIMIT %d OFFSET %d",
		eventSelectColumns, dbName, whereClause, limit, offset)
}

// QueryDashboardSummary returns summary statistics for the dashboard.
func (c *Client) QueryDashboardSummary(ctx context.Context) (*DashboardSummary, error) {
	totalEvents, err := c.QueryStats(ctx)
	if err != nil {
		return nil, err
	}

	summary := &DashboardSummary{
		TotalEvents:   totalEvents,
		LatestSource:  "-",
		LastEventType: "-",
	}
	if totalEvents == 0 {
		return summary, nil
	}

	serversQuery := fmt.Sprintf("SELECT count(DISTINCT server) FROM %s.events WHERE server != ''", c.DBName)
	var serversCount uint64
	if err := c.Conn.QueryRow(ctx, serversQuery).Scan(&serversCount); err != nil {
		return nil, fmt.Errorf("failed to query active servers: %w", err)
	}
	summary.ActiveServers = int(serversCount)

	latestQuery := fmt.Sprintf("SELECT source, event_type, timestamp FROM %s.events ORDER BY timestamp DESC LIMIT 1", c.DBName)
	var latestSource, latestEventType string
	var latestTime time.Time
	if err := c.Conn.QueryRow(ctx, latestQuery).Scan(&latestSource, &latestEventType, &latestTime); err != nil {
		return nil, fmt.Errorf("failed to query latest event: %w", err)
	}
	if latestSource = strings.TrimSpace(latestSource); latestSource != "" {
		summary.LatestSource = latestSource
	}
	if latestEventType = strings.TrimSpace(latestEventType); latestEventType != "" {
		summary.LastEventType = latestEventType
	}

	if !latestTime.IsZero() {
		summary.LastEventTime = latestTime.Format(time.RFC3339)
	}

	return summary, nil
}

func scanEventRow(scanner RowScanner, row *EventRow) error {
	var payloadStr string
	if err := scanner.Scan(
		&row.Timestamp,
		&row.TraceID,
		&row.Source,
		&row.EventType,
		&row.Server,
		&row.Namespace,
		&row.TeamID,
		&row.Cluster,
		&row.HumanID,
		&row.AgentID,
		&row.SessionID,
		&row.Decision,
		&row.ToolName,
		&payloadStr,
	); err != nil {
		return err
	}
	if json.Valid([]byte(payloadStr)) {
		row.Payload = json.RawMessage(payloadStr)
		return nil
	}
	raw, _ := json.Marshal(payloadStr)
	row.Payload = raw
	return nil
}

func joinConditions(conditions []string, sep string) string {
	result := ""
	for i, c := range conditions {
		if i > 0 {
			result += sep
		}
		result += c
	}
	return result
}

func normalizeEventLimit(limit int) int {
	if limit < 1 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func normalizeEventOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > maxEventOffset {
		return maxEventOffset
	}
	return offset
}
