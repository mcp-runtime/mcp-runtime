package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestStandaloneServersDoNotAdvertiseOrProxyGatewayMetrics(t *testing.T) {
	for _, role := range []string{roleAdmin, roleUser} {
		for _, explicit := range []bool{false, true} {
			name := role + "/gateway-omitted"
			if explicit {
				name = role + "/gateway-disabled"
			}
			t.Run(name, func(t *testing.T) {
				target := ownedTestMCPServer("standalone", "user-1", "user-1")
				target.Spec.Gateway = nil
				if explicit {
					target.Spec.Gateway = &mcpv1alpha1.GatewayConfig{Enabled: false}
				}
				server := newRuntimeServerWithMCPServers(t, target)
				p := principal{Role: role, Subject: "user-1", Namespace: "user-1", AllowedNamespaces: []string{"user-1"}}
				request := httptest.NewRequest(http.MethodGet, "/api/runtime/servers", nil)
				request = request.WithContext(withPrincipal(request.Context(), p))
				recorder := httptest.NewRecorder()
				server.HandleRuntimeServers(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("catalog status=%d: %s", recorder.Code, recorder.Body.String())
				}
				var payload struct {
					Servers []serverInfo `json:"servers"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Servers) != 1 || payload.Servers[0].Observability != nil {
					t.Fatalf("standalone catalog advertised gateway metrics: %#v", payload.Servers)
				}
				for path, handler := range map[string]http.HandlerFunc{
					"links":            server.HandleRuntimeObservabilityLinks,
					"prometheus/query": server.HandleRuntimeObservabilityPrometheusQuery,
				} {
					request := httptest.NewRequest(http.MethodGet, "/api/runtime/observability/"+path+"?namespace=user-1&server=standalone", nil)
					request = request.WithContext(withPrincipal(request.Context(), p))
					recorder := httptest.NewRecorder()
					handler(recorder, request)
					if recorder.Code != http.StatusNotFound {
						t.Fatalf("%s status=%d, want 404: %s", path, recorder.Code, recorder.Body.String())
					}
				}
			})
		}
	}
}
