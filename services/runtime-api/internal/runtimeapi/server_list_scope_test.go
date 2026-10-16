package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/k8sclient"
)

func TestServerListVisibilityScopes(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespaces := []string{defaultPublicCatalogNamespace, defaultOrgCatalogNamespace, "user-1", "mcp-team-acme", "mcp-team-other"}
	var objects []runtime.Object
	for _, ns := range namespaces {
		objects = append(objects, &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}})
	}
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(scheme, objects...), Clientset: kubernetesfake.NewSimpleClientset()}}
	member := principal{Role: roleUser, Subject: "user-1", Namespace: "user-1", Teams: []principalTeam{{ID: "team-acme", Slug: "acme", Namespace: "mcp-team-acme", Role: teamRoleMember}}}
	for _, tc := range []struct {
		name      string
		p         principal
		namespace string
		status    int
		want      []string
	}{
		{"member defaults", member, "", http.StatusOK, []string{defaultOrgCatalogNamespace, defaultPublicCatalogNamespace, "mcp-team-acme", "user-1"}},
		{"admin defaults", principal{Role: roleAdmin, Subject: "admin"}, "", http.StatusOK, []string{defaultOrgCatalogNamespace, defaultPublicCatalogNamespace, "mcp-team-acme", "mcp-team-other", "user-1"}},
		{"member own team", member, "mcp-team-acme", http.StatusOK, []string{"mcp-team-acme"}},
		{"member other team denied", member, "mcp-team-other", http.StatusForbidden, nil},
		{"admin other team", principal{Role: roleAdmin, Subject: "admin"}, "mcp-team-other", http.StatusOK, []string{"mcp-team-other"}},
		{"organization catalog only", member, defaultOrgCatalogNamespace, http.StatusOK, []string{defaultOrgCatalogNamespace}},
		{"personal only", member, "user-1", http.StatusOK, []string{"user-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers?namespace="+tc.namespace, nil)
			req = req.WithContext(withPrincipal(req.Context(), tc.p))
			rec := httptest.NewRecorder()
			server.HandleRuntimeServers(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				return
			}
			var payload struct {
				Servers []serverInfo `json:"servers"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range payload.Servers {
				got = append(got, item.Namespace)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("namespaces=%v want %v", got, tc.want)
			}
		})
	}
}
