package platformclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

func TestSessionClientDrainsScopedPages(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer control-token" || r.URL.Path != sessionstate.AccessPath || r.URL.Query().Get("namespace") != "tenant" || r.URL.Query().Get("limit") != "1000" {
			t.Errorf("unexpected scoped request %s %s", r.Method, r.URL)
		}
		if calls == 1 {
			if r.URL.Query().Get("after") != "" {
				t.Error("initial cursor supplied")
			}
			_ = json.NewEncoder(w).Encode(sessionstate.Page{Sessions: []sessionstate.Session{{ID: "tenant/a"}}, NextCursor: "tenant/a"})
		} else {
			if r.URL.Query().Get("after") != "tenant/a" {
				t.Error("cursor not advanced")
			}
			_ = json.NewEncoder(w).Encode(sessionstate.Page{Sessions: []sessionstate.Session{{ID: "tenant/b"}}})
		}
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL, Token: "control-token", HTTP: server.Client()}
	items, err := c.ListSessions(context.Background(), "tenant")
	if err != nil || len(items) != 2 || calls != 2 {
		t.Fatalf("items=%+v calls=%d err=%v", items, calls, err)
	}
}

func TestSessionClientPreservesUIDAndMetadata(t *testing.T) {
	item := sessionstate.Session{ID: "tenant/sess", Namespace: "tenant", UID: "immutable-uid", ServerUID: "server-uid", Binding: policy.Binding{Name: "sess", Namespace: "tenant"}, Labels: map[string]string{"owner": "agent"}, Annotations: map[string]string{"grant": "grant-1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != sessionstate.AccessPath || r.Header.Get("Authorization") != "Bearer control-token" {
			t.Error("incorrect session write request")
		}
		var received sessionstate.Session
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		if received.UID != item.UID || received.Labels["owner"] != "agent" || received.Annotations["grant"] != "grant-1" {
			t.Errorf("metadata lost %+v", received)
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(received)
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL, Token: "control-token"}
	out, err := c.PutSession(context.Background(), item)
	if err != nil || out.UID != item.UID {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestSessionClientUsesConditionalEnrollmentMutations(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != sessionstate.AccessPath+"/tenant/sess/enrollment" || r.Header.Get("Authorization") != "Bearer control-token" {
			t.Errorf("unexpected enrollment request %s", r.URL)
		}
		switch r.Method {
		case http.MethodPatch:
			var request sessionstate.EnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.UID != "session-uid" {
				t.Errorf("request=%+v error=%v", request, err)
			}
		case http.MethodDelete:
			if r.URL.Query().Get("uid") != "session-uid" || r.URL.Query().Get("human_id") != "owner" {
				t.Errorf("cleanup query=%s", r.URL.RawQuery)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, Token: "control-token", HTTP: server.Client()}
	if err := client.CompleteSessionEnrollment(context.Background(), "tenant", "sess", "session-uid"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeletePendingSession(context.Background(), "tenant", "sess", "session-uid", "owner"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestSessionClientMapsErrorsAndNeverHidesStoreOutage(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   error
	}{{404, sessionstate.ErrNotFound}, {409, sessionstate.ErrConflict}, {403, sessionstate.ErrInactiveAgent}, {503, nil}} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != sessionstate.AccessPath+"/tenant/sess" {
					t.Errorf("path=%q", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"test_error","message":"test failure"}`))
			}))
			defer server.Close()
			c := &Client{BaseURL: server.URL, Token: "control-token"}
			err := c.RevokeSession(context.Background(), "tenant", "sess", "test")
			if err == nil {
				t.Fatal("failure hidden")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error=%v want=%v", err, tt.want)
			}
			_, ok, err := c.GetSession(context.Background(), "tenant", "sess")
			if tt.status == 404 {
				if err != nil || ok {
					t.Fatalf("missing session ok=%v err=%v", ok, err)
				}
			} else if err == nil {
				t.Fatal("get hid store/auth error")
			}
		})
	}
}

func TestSessionClientRejectsNonAdvancingPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sessionstate.Page{Sessions: []sessionstate.Session{}, NextCursor: "tenant/a"})
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL, Token: "control-token"}
	if _, err := c.ListSessions(context.Background(), "tenant"); err == nil {
		t.Fatal("nonadvancing cursor accepted")
	}
}
