package usage

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/platformauth"
)

func (s *Service) HandleAdminUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		apihttp.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	limit := clampInt(queryInt(r, "limit", 10), 1, 50)
	if err := validateUsageFilters(r); err != nil {
		apihttp.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_filter", "message": err.Error()})
		return
	}
	windowDays := clampInt(queryInt(r, "window_days", defaultWindowDays), 1, maxWindowDays)
	scope := scopeFromRequest(r, windowDays, limit)
	applyAdminScopeFilters(r, &scope)

	response, err := s.queryUsage(r.Context(), scope)
	if err != nil {
		log.Printf("analytics usage query failed window_days=%d limit=%d since=%s filters=%+v err=%v", scope.WindowDays, scope.Limit, scope.Since, scope.Filters(), err)
		apihttp.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query_failed"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, response)
}

func (s *Service) HandleUserUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		apihttp.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	p, ok := platformauth.FromContext(r.Context())
	if !ok {
		apihttp.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if err := validateUsageFilters(r); err != nil {
		apihttp.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_filter", "message": err.Error()})
		return
	}

	limit := clampInt(queryInt(r, "limit", 10), 1, 50)
	windowDays := clampInt(queryInt(r, "window_days", defaultWindowDays), 1, maxWindowDays)
	scope := scopeFromRequest(r, windowDays, limit)

	requestedNamespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if p.Role == platformauth.RoleAdmin {
		applyAdminScopeFilters(r, &scope)
	} else {
		var principalScope PrincipalScope
		var allowed bool
		if requestedNamespace != "" {
			principalScope, allowed = principalScopeForNamespace(p, requestedNamespace)
			if !allowed {
				apihttp.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden namespace"})
				return
			}
		} else {
			principalScope = PrincipalOwnedScope(p)
			allowed = len(principalScope.Namespaces) > 0 || len(principalScope.TeamIDs) > 0
		}
		if !allowed {
			apihttp.WriteJSON(w, http.StatusOK, emptyUsageResponse(scope))
			return
		}
		scope.Namespaces = principalScope.Namespaces
		scope.TeamIDs = principalScope.TeamIDs
	}

	response, err := s.queryUsage(r.Context(), scope)
	if err != nil {
		log.Printf("user analytics usage query failed window_days=%d limit=%d since=%s filters=%+v err=%v", scope.WindowDays, scope.Limit, scope.Since, scope.Filters(), err)
		apihttp.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "query_failed"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, response)
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
