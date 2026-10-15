package analytics

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mcp-runtime/pkg/apihttp"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/serviceutil"
)

type EventReader interface {
	QueryEvents(context.Context, int, int) ([]clickhousepkg.EventRow, error)
	QueryStats(context.Context) (uint64, error)
	QuerySources(context.Context) ([]clickhousepkg.SourceStat, error)
	QueryEventTypes(context.Context) ([]clickhousepkg.EventTypeStat, error)
	QueryEventsFiltered(context.Context, clickhousepkg.EventFilters) ([]clickhousepkg.EventRow, error)
}

type Handler struct {
	events EventReader
}

func NewHandler(events EventReader) *Handler {
	return &Handler{events: events}
}

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	limit, err := apihttp.ParseLimit(r, 100, 1000)
	if err != nil {
		apihttp.WriteError(w, nil, err)
		return
	}
	offset, err := apihttp.ParseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		apihttp.WriteError(w, nil, err)
		return
	}

	var events []clickhousepkg.EventRow
	if hasEventFilters(r) {
		filters, filterErr := eventFiltersFromRequest(r, limit, time.Now().UTC())
		if filterErr != nil {
			apihttp.WriteError(w, nil, filterErr)
			return
		}
		filters.Offset = offset
		events, err = h.events.QueryEventsFiltered(r.Context(), filters)
	} else {
		events, err = h.events.QueryEvents(r.Context(), limit, offset)
	}
	if err != nil {
		apihttp.WriteError(w, nil, &apihttp.Error{
			Status:  http.StatusInternalServerError,
			Code:    apihttp.CodeQueryFailed,
			Message: "query failed",
			Cause:   err,
		})
		return
	}
	meta := apihttp.ListMeta(limit, offset, len(events))
	body := map[string]any{
		"events": events,
		"meta":   meta,
	}
	if next := apihttp.NextLink(r, meta.NextCursor, limit); next != "" {
		body["next"] = next
	}
	serviceutil.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	count, err := h.events.QueryStats(r.Context())
	if err != nil {
		apihttp.WriteError(w, nil, &apihttp.Error{
			Status:  http.StatusInternalServerError,
			Code:    apihttp.CodeQueryFailed,
			Message: "query failed",
			Cause:   err,
		})
		return
	}
	serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"events_total": count})
}

func (h *Handler) Sources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.events.QuerySources(r.Context())
	if err != nil {
		apihttp.WriteError(w, nil, &apihttp.Error{
			Status:  http.StatusInternalServerError,
			Code:    apihttp.CodeQueryFailed,
			Message: "query failed",
			Cause:   err,
		})
		return
	}
	serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

func (h *Handler) EventTypes(w http.ResponseWriter, r *http.Request) {
	eventTypes, err := h.events.QueryEventTypes(r.Context())
	if err != nil {
		apihttp.WriteError(w, nil, &apihttp.Error{
			Status:  http.StatusInternalServerError,
			Code:    apihttp.CodeQueryFailed,
			Message: "query failed",
			Cause:   err,
		})
		return
	}
	serviceutil.WriteJSON(w, http.StatusOK, map[string]any{"event_types": eventTypes})
}

func hasEventFilters(r *http.Request) bool {
	q := r.URL.Query()
	for _, key := range []string{
		"trace_id", "source", "event_type", "server", "namespace", "team_id",
		"cluster", "human_id", "agent_id", "session_id", "decision", "tool_name", "reason",
		"matched_grant", "matched_grant_namespace", "matched_session", "matched_session_namespace", "since", "until",
	} {
		if q.Get(key) != "" || ((key == "since" || key == "until") && q.Has(key)) {
			return true
		}
	}
	return false
}

func eventFiltersFromRequest(r *http.Request, limit int, now time.Time) (clickhousepkg.EventFilters, error) {
	q := r.URL.Query()
	filters := clickhousepkg.EventFilters{
		TraceID:      q.Get("trace_id"),
		Source:       q.Get("source"),
		EventType:    q.Get("event_type"),
		Server:       q.Get("server"),
		Namespace:    q.Get("namespace"),
		TeamID:       q.Get("team_id"),
		Cluster:      q.Get("cluster"),
		HumanID:      q.Get("human_id"),
		AgentID:      q.Get("agent_id"),
		SessionID:    q.Get("session_id"),
		Decision:     q.Get("decision"),
		ToolName:     q.Get("tool_name"),
		Reason:       q.Get("reason"),
		Limit:        limit,
		MatchedGrant: q.Get("matched_grant"), MatchedGrantNamespace: q.Get("matched_grant_namespace"),
		MatchedSession: q.Get("matched_session"), MatchedSessionNamespace: q.Get("matched_session_namespace"),
	}
	for _, pair := range []struct{ name, namespace string }{{"matched_grant", "matched_grant_namespace"}, {"matched_session", "matched_session_namespace"}} {
		name, namespace := q.Get(pair.name), q.Get(pair.namespace)
		if (name == "") != (namespace == "") {
			return filters, apihttp.BadRequest(apihttp.CodeInvalidQueryParam, fmt.Sprintf("%s and %s must be supplied together", pair.name, pair.namespace))
		}
		if len(name) > 256 || len(namespace) > 256 || strings.TrimSpace(name) != name || strings.TrimSpace(namespace) != namespace {
			return filters, apihttp.BadRequest(apihttp.CodeInvalidQueryParam, "matched access names and namespaces must be exact values of at most 256 bytes")
		}
	}
	if filters.MatchedGrant != "" || filters.MatchedSession != "" || q.Has("since") || q.Has("until") {
		var err error
		filters.Since, err = apihttp.QueryRFC3339Time(r, "since")
		if err != nil {
			return filters, err
		}
		filters.Until, err = apihttp.QueryRFC3339Time(r, "until")
		if err != nil {
			return filters, err
		}
		if filters.Until.IsZero() {
			filters.Until = now
		}
		if filters.Since.IsZero() {
			filters.Since = filters.Until.Add(-7 * 24 * time.Hour)
		}
		if !filters.Since.Before(filters.Until) {
			return filters, apihttp.BadRequest(apihttp.CodeInvalidQueryParam, "since must be earlier than until")
		}
		if filters.Until.Sub(filters.Since) > 365*24*time.Hour {
			return filters, apihttp.BadRequest(apihttp.CodeInvalidQueryParam, "event time window must not exceed 365 days")
		}
	}
	return filters, nil
}
