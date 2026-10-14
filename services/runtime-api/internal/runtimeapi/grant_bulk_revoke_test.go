package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

type bulkGrantSessionClient struct {
	fakeSessionClient
	rows      []sessionstate.Session
	bulkCalls int
	filter    sessionstate.Filter
	cause     string
	fail      bool
}

func (c *bulkGrantSessionClient) ListSessions(_ context.Context, ns string) ([]sessionstate.Session, error) {
	return nil, errors.New("global session listing is forbidden in this test")
}
func (c *bulkGrantSessionClient) ListSessionsFiltered(_ context.Context, filter sessionstate.Filter) ([]sessionstate.Session, error) {
	if filter.Namespace != "" || filter.GrantNamespace != "team-a" || filter.GrantName != "grant" {
		return nil, errors.New("incorrect grant filter")
	}
	rows := []sessionstate.Session{}
	for _, row := range c.rows {
		if row.GrantNamespace == filter.GrantNamespace && row.GrantName == filter.GrantName {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (c *bulkGrantSessionClient) SetSessionRevoked(context.Context, string, string, bool, string) error {
	return errors.New("per-session revocation is forbidden in this test")
}
func (c *bulkGrantSessionClient) RevokeSessions(_ context.Context, filter sessionstate.Filter, cause string) (int64, error) {
	c.bulkCalls++
	c.filter = filter
	c.cause = cause
	if c.fail {
		return 0, errors.New("transaction failed")
	}
	var count int64
	for i := range c.rows {
		row := &c.rows[i]
		if row.GrantNamespace == filter.GrantNamespace && row.GrantName == filter.GrantName && !row.Binding.Revoked {
			row.Binding.Revoked = true
			count++
		}
	}
	return count, nil
}

func TestGrantBulkRevocationCrossNamespaceAuthorizationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		denied, fail bool
	}{{name: "owner revokes all linked namespaces"}, {name: "unrelated owner denied", denied: true}, {name: "atomic store failure", fail: true}} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			grant := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-a"}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "server", Namespace: "team-a"}, Subject: mcpv1alpha1.SubjectRef{TeamID: "team-b"}}}
			server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "team-a"}, Spec: mcpv1alpha1.MCPServerSpec{TeamID: "team-a"}}
			manager := mcpaccess.NewManager(dynamicfake.NewSimpleDynamicClient(scheme, grant, server), nil)
			client := &bulkGrantSessionClient{fail: tc.fail, rows: []sessionstate.Session{
				{Namespace: "team-a", GrantNamespace: "team-a", GrantName: "grant", Binding: policy.Binding{Name: "local", TeamID: "team-b"}},
				{Namespace: "team-b", GrantNamespace: "team-a", GrantName: "grant", Binding: policy.Binding{Name: "foreign", TeamID: "team-b"}},
				{Namespace: "team-c", GrantNamespace: "team-c", GrantName: "grant", Binding: policy.Binding{Name: "unrelated", TeamID: "team-c"}},
			}}
			manager.WithSessionBackend(&platformSessionBackend{client: client, access: manager})
			audit := &fakeAuditWriter{}
			s := &RuntimeServer{accessMgr: manager, audit: audit}
			p := principal{Role: roleUser, Subject: "owner-a", Namespace: "team-a", Teams: []principalTeam{{ID: "team-a", Slug: "a", Namespace: "team-a", Role: teamRoleOwner}}}
			if tc.denied {
				p.Namespace = "team-b"
				p.Teams = []principalTeam{{ID: "team-b", Slug: "b", Namespace: "team-b", Role: teamRoleOwner}}
			}
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/grants/team-a/grant/revoke-sessions", nil)
				r = r.WithContext(withPrincipal(r.Context(), p))
				w := httptest.NewRecorder()
				s.Access().HandleGrantItemPath(w, r)
				return w
			}
			w := request()
			if tc.denied {
				if w.Code != http.StatusForbidden || client.bulkCalls != 0 {
					t.Fatalf("status=%d calls=%d", w.Code, client.bulkCalls)
				}
				return
			}
			if client.bulkCalls != 1 || client.filter.Namespace != "" || client.filter.GrantNamespace != "team-a" || client.filter.GrantName != "grant" || client.cause != "grant_revoked" {
				t.Fatalf("bulk revocation scope=%+v cause=%s calls=%d", client.filter, client.cause, client.bulkCalls)
			}
			if tc.fail {
				if w.Code != http.StatusInternalServerError || client.rows[0].Binding.Revoked || len(audit.events) != 1 || audit.events[0].Status != "error" {
					t.Fatalf("failed transaction status=%d rows=%+v audit=%+v", w.Code, client.rows, audit.events)
				}
				return
			}
			if w.Code != http.StatusOK || !client.rows[0].Binding.Revoked || !client.rows[1].Binding.Revoked || client.rows[2].Binding.Revoked {
				t.Fatalf("status=%d rows=%+v", w.Code, client.rows)
			}
			var response map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			if response["revokedSessions"] != float64(2) || len(audit.events) != 3 {
				t.Fatalf("response=%v audit=%+v", response, audit.events)
			}
			for _, event := range audit.events {
				var payload map[string]any
				_ = json.Unmarshal([]byte(event.Message), &payload)
				if payload["subject_team_id"] != "team-b" || payload["resource_team_id"] != "team-a" {
					t.Fatalf("lost actor/authority attribution: %s", event.Message)
				}
			}
			if w = request(); w.Code != http.StatusOK {
				t.Fatalf("retry status=%d", w.Code)
			}
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			if response["revokedSessions"] != float64(0) {
				t.Fatalf("retry response=%v", response)
			}
		})
	}
}
