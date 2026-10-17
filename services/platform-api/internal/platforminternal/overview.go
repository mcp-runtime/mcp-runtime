package platforminternal

import (
	"context"
	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/internalapi"
	"net/http"
)

func (h Handler) overviewCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	store, ok := h.Store.(interface {
		OverviewCounts(context.Context) (internalapi.OverviewCounts, error)
	})
	if !ok {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeServiceUnavailable, "overview unavailable")
		return
	}
	out, err := store.OverviewCounts(r.Context())
	if err != nil {
		apihttp.WriteEnvelope(w, http.StatusServiceUnavailable, apihttp.CodeQueryFailed, "overview counts unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
