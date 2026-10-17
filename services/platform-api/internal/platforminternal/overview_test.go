package platforminternal

import (
	"context"
	"mcp-runtime/pkg/internalapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type overviewStoreStub struct{ fakeStore }

func (*overviewStoreStub) OverviewCounts(context.Context) (internalapi.OverviewCounts, error) {
	return internalapi.OverviewCounts{Users: 1000, Teams: 300, Agents: 1500, ActiveSessions: 800}, nil
}

func TestInternalOverviewCountsRequireTokenAndReturnCompleteTotals(t *testing.T) {
	handler := newTestServer(&overviewStoreStub{})
	for _, token := range []string{"", "wrong", "internal-token"} {
		r := httptest.NewRequest(http.MethodGet, "/internal/operations/counts", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if token != "internal-token" {
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized status=%d", w.Code)
			}
		} else if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"users":1000`) {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
}
