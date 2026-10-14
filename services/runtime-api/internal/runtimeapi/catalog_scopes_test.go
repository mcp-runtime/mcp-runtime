package runtimeapi

import (
	"context"
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
	"mcp-runtime/pkg/publishscope"
)

func TestPublicModeScopeRoutingKeepsOrgAndPublicSeparate(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	for _, role := range []string{roleAdmin, roleUser} {
		p := principal{Role: role, Subject: "publisher", AuthType: "user_api_key"}
		ctx := withPrincipal(context.Background(), p)
		server := &RuntimeServer{}
		for _, scope := range []publishscope.Scope{publishscope.Org, publishscope.Public} {
			wantNS, _ := publishscope.CatalogNamespace(scope)
			gotNS, err := server.scopedNamespaceForServerApply(ctx, "", scope)
			if err != nil || gotNS != wantNS {
				t.Fatalf("role=%s scope=%s deploy namespace=%q err=%v, want %q", role, scope, gotNS, err, wantNS)
			}
			pushNS, _, err := registryPushAuthContext("registry.example.com/"+string(scope)+"/demo:v1", scope, p)
			if err != nil || pushNS != wantNS {
				t.Fatalf("role=%s scope=%s push namespace=%q err=%v, want %q", role, scope, pushNS, err, wantNS)
			}
			other := defaultOrgCatalogNamespace
			if scope == publishscope.Org {
				other = defaultPublicCatalogNamespace
			}
			if _, err := server.scopedNamespaceForServerApply(ctx, other, scope); err == nil {
				t.Fatalf("role=%s scope=%s accepted mismatched namespace %s", role, scope, other)
			}
			if alias := defaultImageRepositoryScope(wantNS, ""); alias != string(scope) {
				t.Fatalf("namespace=%s image prefix=%q, want %s", wantNS, alias, scope)
			}
			if allowed := allowedImageRepositoryScopes(wantNS, ""); stringInSlice(string(scope), allowed) == false || stringInSlice(defaultImageRepositoryScope(other, ""), allowed) {
				t.Fatalf("scope=%s allowed image prefixes=%v", scope, allowed)
			}
		}
	}
}

func TestAnonymousCatalogExcludesOrgAndTeamServers(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "public")
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := &RuntimeServer{k8sClients: &k8sclient.Clients{
		Dynamic: dynamicfake.NewSimpleDynamicClient(scheme,
			&mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "org-secret", Namespace: defaultOrgCatalogNamespace}},
			&mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "team-secret", Namespace: "mcp-team-acme"}},
		),
		Clientset: kubernetesfake.NewSimpleClientset(),
	}}
	p := PublicCatalogPrincipal()
	if got := catalogNamespacesForPrincipal(p); !reflect.DeepEqual(got, []string{defaultPublicCatalogNamespace}) {
		t.Fatalf("anonymous namespaces=%v", got)
	}
	for _, namespace := range []string{defaultOrgCatalogNamespace, "mcp-team-acme"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/servers?namespace="+namespace, nil)
		req = req.WithContext(withPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		server.HandleRuntimeServers(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("namespace=%s status=%d body=%s", namespace, rec.Code, rec.Body.String())
		}
	}
	if principalCanPublishNamespace(p, defaultPublicCatalogNamespace) {
		t.Fatal("anonymous reader can publish")
	}
	for _, namespace := range []string{defaultPublicCatalogNamespace, defaultOrgCatalogNamespace} {
		entry := catalogNamespaceEntry(namespace)
		if entry["is_public"] != (namespace == defaultPublicCatalogNamespace) {
			t.Fatalf("wrong catalog visibility: %v", entry)
		}
	}
}
