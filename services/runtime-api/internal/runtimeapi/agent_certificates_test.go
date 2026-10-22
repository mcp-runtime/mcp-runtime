package runtimeapi

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/internalapi"
)

type certificateIdentity struct {
	agentIdentityStub
	human  string
	called bool
	err    error
}

func (s *certificateIdentity) AgentCertificates(_ context.Context, _ string, human string) (internalapi.CertificateActivity, error) {
	s.called, s.human = true, human
	return internalapi.CertificateActivity{Recorded: 3, Recent: []internalapi.CertificateIssuance{}}, s.err
}

func TestAgentCertificateHistoryVisibilityAndIndependentFailure(t *testing.T) {
	for _, tc := range []struct {
		name, role, team, human string
		want                    int
		historyError            bool
	}{
		{"admin", roleAdmin, "", "", 200, false},
		{"owner", roleUser, teamRoleOwner, "", 200, false},
		{"member", roleUser, teamRoleMember, "member", 200, false},
		{"foreign", roleUser, "foreign", "", 404, false},
		{"history failure preserves details", roleUser, teamRoleOwner, "", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := &certificateIdentity{}
			if tc.historyError {
				identity.err = errors.New("history offline")
			}
			kinds := map[schema.GroupVersionResource]string{
				{Group: mcpaccess.APIGroup, Version: mcpaccess.APIVersion, Resource: mcpaccess.AccessGrantResource}:   "MCPAccessGrantList",
				{Group: mcpaccess.APIGroup, Version: mcpaccess.APIVersion, Resource: mcpaccess.AccessSessionResource}: "MCPAgentSessionList",
			}
			grant := &unstructured.Unstructured{Object: map[string]any{"apiVersion": mcpaccess.APIGroup + "/" + mcpaccess.APIVersion, "kind": "MCPAccessGrant", "metadata": map[string]any{"name": "grant", "namespace": "mcp-team-core"}, "spec": map[string]any{"subject": map[string]any{"humanID": "member", "teamID": "team-id", "agentID": "agt_01arz3ndektsv4rrffq69g5fav"}, "serverRef": map[string]any{"name": "demo"}}}}
			s := &RuntimeServer{identity: identity, accessMgr: mcpaccess.NewManager(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, grant), nil)}
			team := "core"
			if tc.team == "foreign" {
				team = "other"
			}
			p := principal{Role: tc.role, Subject: "member", Teams: []principalTeam{{Slug: team, Role: tc.team}}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav", nil)
			req = req.WithContext(withPrincipal(req.Context(), p))
			rec := httptest.NewRecorder()
			s.HandleRuntimeAgentPath(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.want != 200 {
				if identity.called {
					t.Fatal("read certificate history before agent authorization")
				}
				return
			}
			if !identity.called || identity.human != tc.human {
				t.Fatalf("wrong human visibility filter %q", identity.human)
			}
			if tc.historyError && (!strings.Contains(rec.Body.String(), `"certificates_error"`) || !strings.Contains(rec.Body.String(), `"agent"`) || strings.Contains(rec.Body.String(), `"recorded":0`)) {
				t.Fatal("history failure erased agent details or became zero")
			}
		})
	}
}

func TestCertificateIssuanceMetadataContainsNoCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	metadata, err := certificateIssuanceMetadata(string(certificate), "team-a", "session-a", "demo", "human-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Fingerprint) != 64 || metadata.Serial == "" || metadata.NotAfter.IsZero() || metadata.HumanID != "human-a" {
		t.Fatalf("incomplete metadata %+v", metadata)
	}
	data, _ := json.Marshal(metadata)
	if strings.Contains(string(data), "BEGIN") || strings.Contains(string(data), "PRIVATE") || strings.Contains(string(data), string(certificate)) {
		t.Fatal("credential material entered certificate history")
	}
	if _, err := certificateIssuanceMetadata("invalid", "team-a", "session-a", "demo", "human-a"); err == nil {
		t.Fatal("accepted invalid certificate metadata")
	}
	audit := &fakeAuditWriter{}
	svc := &AccessService{audit: audit}
	session := &mcpaccess.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session-a", Namespace: "team-a"}, Spec: mcpaccess.MCPAgentSessionSpec{ServerRef: mcpaccess.ServerReference{Name: "demo"}, Subject: mcpaccess.SubjectRef{HumanID: "human-a", AgentID: "agent-a", TeamID: "team-a"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/adapter/certificates", nil)
	for range 2 {
		svc.recordCertificateIssuance(req, principal{Subject: "human-a"}, session, string(certificate))
	}
	if len(audit.events) != 2 {
		t.Fatal("multiple issuances in one session were conflated")
	}
	event := audit.events[0]
	if event.AgentID != "agent-a" || event.UserID != "human-a" || event.Action != "adapter.certificate.issued" || event.Resource != "session/session-a" {
		t.Fatalf("issuance attribution %+v", event)
	}
	if strings.Contains(event.Message, "BEGIN") || strings.Contains(event.Message, "PRIVATE") {
		t.Fatal("audit contains credential material")
	}
}
