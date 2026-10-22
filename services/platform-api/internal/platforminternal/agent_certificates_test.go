package platforminternal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-runtime/pkg/internalapi"
)

type certificateStore struct {
	fakeStore
	agent, human string
}

func (s *certificateStore) AgentCertificates(_ context.Context, agent, human string) (internalapi.CertificateActivity, error) {
	s.agent, s.human = agent, human
	return internalapi.CertificateActivity{Recorded: 2, Recent: []internalapi.CertificateIssuance{}}, nil
}

func TestAgentCertificateHistoryRequiresInternalAuthAndExactFilters(t *testing.T) {
	store := &certificateStore{}
	mux := newTestServer(store)
	path := "/internal/identity/agents/agent-1/certificates?human_id=human-1"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusUnauthorized || store.agent != "" {
		t.Fatal("unauthenticated history access")
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authorizedRequest(http.MethodGet, path, ""))
	if rec.Code != http.StatusOK || store.agent != "agent-1" || store.human != "human-1" || !strings.Contains(rec.Body.String(), `"recorded":2`) {
		t.Fatalf("status=%d body=%s filters=%s/%s", rec.Code, rec.Body.String(), store.agent, store.human)
	}
}
