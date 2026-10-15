package clickhouse

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAccessEventFiltersQualifyNamesAndTimeBeforeLimit(t *testing.T) {
	since := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	until := since.Add(7 * 24 * time.Hour)
	filters := EventFilters{MatchedGrant: "same-name", MatchedGrantNamespace: "grant-team", MatchedSession: "same-name", MatchedSessionNamespace: "session-team", Since: since, Until: until, Limit: 1000}
	where, args := buildEventFilterWhereClause(filters)
	query := buildEventFilterQuery("mcp", where, filters.Limit, 0)
	for _, field := range []string{"matched_grant", "matched_grant_namespace", "matched_session", "matched_session_namespace"} {
		condition := "JSONExtractString(payload, '" + field + "') = ?"
		position := strings.Index(query, condition)
		if position < 0 || position > strings.Index(query, "LIMIT") {
			t.Fatalf("exact %s filter does not precede limit: %s", field, query)
		}
	}
	for _, condition := range []string{"timestamp >= ?", "timestamp < ?"} {
		if position := strings.Index(query, condition); position < 0 || position > strings.Index(query, "LIMIT") {
			t.Fatalf("time filter does not precede limit: %s", query)
		}
	}
	want := []interface{}{"same-name", "grant-team", "same-name", "session-team", since, until}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v want=%#v", args, want)
	}
	if strings.Contains(query, " LIKE ") || strings.Contains(query, "session_id =") {
		t.Fatalf("access matches broadened to ambiguous identity: %s", query)
	}
}

func TestAccessEventFilterValuesAreAlwaysParameters(t *testing.T) {
	value := "x' OR 1=1 --"
	where, args := buildEventFilterWhereClause(EventFilters{MatchedGrant: value, MatchedGrantNamespace: "team"})
	query := buildEventFilterQuery("mcp", where, 100, 0)
	if strings.Contains(query, value) || len(args) != 2 || args[0] != value {
		t.Fatalf("query interpolated user input: query=%s args=%#v", query, args)
	}
}

func TestAccessEventNamespaceIsDistinctFromServerNamespace(t *testing.T) {
	where, args := buildEventFilterWhereClause(EventFilters{Namespace: "authority-team", MatchedGrant: "grant", MatchedGrantNamespace: "subject-team"})
	if !strings.Contains(where, "namespace = ?") || !strings.Contains(where, "JSONExtractString(payload, 'matched_grant_namespace') = ?") {
		t.Fatalf("namespace qualifiers conflated: %s", where)
	}
	if !reflect.DeepEqual(args, []interface{}{"authority-team", "grant", "subject-team"}) {
		t.Fatalf("args=%#v", args)
	}
}
