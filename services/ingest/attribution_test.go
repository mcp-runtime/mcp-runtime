package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// fakeCluster is an HTTP stand-in for the Kubernetes API: TokenReview plus the
// Pod -> ReplicaSet -> Deployment -> MCPServer ownership chain.
type fakeCluster struct {
	t             *testing.T
	username      string
	authenticated bool
	audiences     []string
	podName       string // empty omits the Pod-binding extras
	podUID        string
	objects       map[string]map[string]any // API path -> object
	reviewCalls   atomic.Int32
	api           *httptest.Server
}

func owner(kind, apiVersion, name, uid string) []any {
	return []any{map[string]any{"kind": kind, "apiVersion": apiVersion, "name": name, "uid": uid, "controller": true}}
}

func obj(name, uid string, owners []any) map[string]any {
	meta := map[string]any{"name": name, "uid": uid}
	if owners != nil {
		meta["ownerReferences"] = owners
	}
	return map[string]any{"metadata": meta}
}

func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()
	fc := &fakeCluster{
		t: t, username: "system:serviceaccount:mcp-servers:mcp-workload", authenticated: true,
		audiences: []string{"mcp-ingest"}, podName: "srv-abc-1", podUID: "pod-uid",
	}
	pod := obj("srv-abc-1", "pod-uid", owner("ReplicaSet", "apps/v1", "srv-abc", "rs-uid"))
	pod["spec"] = map[string]any{"serviceAccountName": "mcp-workload"}
	fc.objects = map[string]map[string]any{
		"/api/v1/namespaces/mcp-servers/pods/srv-abc-1":                       pod,
		"/apis/apps/v1/namespaces/mcp-servers/replicasets/srv-abc":            obj("srv-abc", "rs-uid", owner("Deployment", "apps/v1", "srv", "dep-uid")),
		"/apis/apps/v1/namespaces/mcp-servers/deployments/srv":                obj("srv", "dep-uid", owner("MCPServer", "mcpruntime.org/v1alpha1", "srv", "srv-uid")),
		"/apis/mcpruntime.org/v1alpha1/namespaces/mcp-servers/mcpservers/srv": obj("srv", "srv-uid", nil),
	}
	fc.api = httptest.NewTLSServer(http.HandlerFunc(fc.serve))
	t.Cleanup(fc.api.Close)
	return fc
}

func (fc *fakeCluster) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer ingest-own" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		fc.reviewCalls.Add(1)
		var req tokenReviewRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Spec.Audiences) != 1 || req.Spec.Audiences[0] != "mcp-ingest" {
			fc.t.Errorf("audiences = %v", req.Spec.Audiences)
		}
		var resp tokenReviewResponse
		resp.Status.Authenticated = fc.authenticated
		resp.Status.Audiences = fc.audiences
		resp.Status.User.Username = fc.username
		if fc.podName != "" {
			resp.Status.User.Extra = map[string][]string{
				"authentication.kubernetes.io/pod-name": {fc.podName},
				"authentication.kubernetes.io/pod-uid":  {fc.podUID},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	o, ok := fc.objects[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(o)
}

func (fc *fakeCluster) authenticator() *serviceAccountAuthenticator {
	return &serviceAccountAuthenticator{
		apiURL:         fc.api.URL + "/apis/authentication.k8s.io/v1/tokenreviews",
		apiBase:        fc.api.URL,
		audience:       "mcp-ingest",
		serviceAccount: "mcp-workload",
		client:         fc.api.Client(),
		readToken:      func() (string, error) { return "ingest-own", nil },
		now:            time.Now,
		cache:          map[string]cachedIdentity{},
	}
}

func TestAuthenticateResolvesVerifiedWorkloadIdentity(t *testing.T) {
	t.Parallel()
	id, ok := newFakeCluster(t).authenticator().Authenticate(context.Background(), "tok")
	if !ok {
		t.Fatal("expected authenticated")
	}
	if want := (workloadIdentity{Namespace: "mcp-servers", Server: "srv", UID: "srv-uid"}); id != want {
		t.Fatalf("identity = %+v, want %+v", id, want)
	}
}

func TestAuthenticateRejectsUnverifiableBinding(t *testing.T) {
	t.Parallel()
	tests := map[string]func(fc *fakeCluster){
		"missing Pod binding": func(fc *fakeCluster) { fc.podName, fc.podUID = "", "" },
		"Pod UID differs":     func(fc *fakeCluster) { fc.podUID = "other-pod" },
		"Pod gone":            func(fc *fakeCluster) { delete(fc.objects, "/api/v1/namespaces/mcp-servers/pods/srv-abc-1") },
		"ReplicaSet UID differs": func(fc *fakeCluster) {
			fc.objects["/apis/apps/v1/namespaces/mcp-servers/replicasets/srv-abc"]["metadata"].(map[string]any)["uid"] = "x"
		},
		"Deployment UID differs": func(fc *fakeCluster) {
			fc.objects["/apis/apps/v1/namespaces/mcp-servers/deployments/srv"]["metadata"].(map[string]any)["uid"] = "x"
		},
		"MCPServer replaced (stale)": func(fc *fakeCluster) {
			fc.objects["/apis/mcpruntime.org/v1alpha1/namespaces/mcp-servers/mcpservers/srv"]["metadata"].(map[string]any)["uid"] = "new-uid"
		},
		"no MCPServer controller": func(fc *fakeCluster) {
			delete(fc.objects["/apis/apps/v1/namespaces/mcp-servers/deployments/srv"]["metadata"].(map[string]any), "ownerReferences")
		},
		"deployment name unrelated to server": func(fc *fakeCluster) {
			fc.objects["/apis/apps/v1/namespaces/mcp-servers/deployments/srv"]["metadata"].(map[string]any)["ownerReferences"] = owner("MCPServer", "mcpruntime.org/v1alpha1", "other", "srv-uid")
			fc.objects["/apis/mcpruntime.org/v1alpha1/namespaces/mcp-servers/mcpservers/other"] = obj("other", "srv-uid", nil)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fc := newFakeCluster(t)
			mutate(fc)
			if _, ok := fc.authenticator().Authenticate(context.Background(), "tok"); ok {
				t.Fatal("token must not authenticate")
			}
		})
	}
}

type captureWriter struct{ msgs []kafka.Message }

func (c *captureWriter) WriteMessages(_ context.Context, m ...kafka.Message) error {
	c.msgs = append(c.msgs, m...)
	return nil
}

func postEvent(t *testing.T, s *ingestServer, headers map[string]string, payload string) (*http.Response, string) {
	t.Helper()
	body := `{"source":"srv-gateway","event_type":"mcp.request","payload":` + payload + `}`
	ts := httptest.NewServer(s.auth(http.HandlerFunc(s.handleEvents)))
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func storedPayload(t *testing.T, w *captureWriter) map[string]any {
	t.Helper()
	if len(w.msgs) != 1 {
		t.Fatalf("messages written = %d, want 1", len(w.msgs))
	}
	var env struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(w.msgs[0].Value, &env); err != nil {
		t.Fatal(err)
	}
	return env.Payload
}

func TestIngestBindsAttributionToVerifiedWorkload(t *testing.T) {
	t.Parallel()
	bearer := map[string]string{"Authorization": "Bearer tok"}
	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{"matching attribution", `{"server":"srv","namespace":"mcp-servers","server_uid":"srv-uid","status":200}`, http.StatusAccepted},
		{"absent attribution is filled in", `{"status":200}`, http.StatusAccepted},
		{"spoofed server name", `{"server":"victim","namespace":"mcp-servers","server_uid":"srv-uid"}`, http.StatusForbidden},
		{"spoofed namespace", `{"server":"srv","namespace":"team-victim","server_uid":"srv-uid"}`, http.StatusForbidden},
		{"spoofed UID", `{"server":"srv","namespace":"mcp-servers","server_uid":"victim-uid"}`, http.StatusForbidden},
		{"stale UID of earlier generation", `{"server":"srv","namespace":"mcp-servers","server_uid":"old-uid"}`, http.StatusForbidden},
		{"non-object payload", `[1,2]`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &captureWriter{}
			s := &ingestServer{writer: w, saAuth: newFakeCluster(t).authenticator()}
			resp, body := postEvent(t, s, bearer, tc.payload)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, body, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusAccepted {
				if len(w.msgs) != 0 {
					t.Fatal("rejected event must not reach Kafka")
				}
				if strings.Contains(body, "victim") || strings.Contains(body, "srv-uid") {
					t.Fatalf("response leaks attribution detail: %s", body)
				}
				return
			}
			p := storedPayload(t, w)
			if p["server"] != "srv" || p["namespace"] != "mcp-servers" || p["server_uid"] != "srv-uid" {
				t.Fatalf("stored attribution = %v", p)
			}
		})
	}
}

func TestIngestRejectsUnboundTokenAtHTTP(t *testing.T) {
	t.Parallel()
	fc := newFakeCluster(t)
	fc.podName, fc.podUID = "", ""
	w := &captureWriter{}
	s := &ingestServer{writer: w, saAuth: fc.authenticator()}
	resp, _ := postEvent(t, s, map[string]string{"Authorization": "Bearer tok"}, `{"server":"srv","namespace":"mcp-servers","server_uid":"srv-uid"}`)
	if resp.StatusCode != http.StatusUnauthorized || len(w.msgs) != 0 {
		t.Fatalf("status = %d, written = %d; want 401 and none", resp.StatusCode, len(w.msgs))
	}
}

func TestIngestServiceKeyAttributionUnchanged(t *testing.T) {
	t.Parallel()
	w := &captureWriter{}
	s := &ingestServer{writer: w, apiKeys: map[string]struct{}{"k": {}}, saAuth: newFakeCluster(t).authenticator()}
	resp, body := postEvent(t, s, map[string]string{"x-api-key": "k"}, `{"server":"any","namespace":"elsewhere","server_uid":"whatever"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	p := storedPayload(t, w)
	if p["server"] != "any" || p["namespace"] != "elsewhere" || p["server_uid"] != "whatever" {
		t.Fatalf("service-key payload altered: %v", p)
	}
}
