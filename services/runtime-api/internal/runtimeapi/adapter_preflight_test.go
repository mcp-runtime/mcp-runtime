package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAdapterPreflightRejectsConfigurationAndActualRouteFailuresBeforeSessionCreation(t *testing.T) {
	for _, scenario := range []struct{ name, reason string }{
		{"missing issuer", "issuer is not configured"},
		{"missing trust domain", "trust domain is not configured"},
		{"operator drift", "configuration differs"},
		{"issuer not Ready", "issuer is not Ready"},
		{"plain route", "no TLS configuration"},
		{"wrong TLSOption", "does not request and verify"},
		{"old route owner", "current MCP server"},
		{"stale server readiness", "route is not Ready"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fx := newAdapterTestFixture(t, adapterTestGrant())
			dyn := fx.server.k8sClients.Dynamic
			update := func(gvr schema.GroupVersionResource, namespace, name string, mutate func(*unstructured.Unstructured)) {
				resource := dyn.Resource(gvr).Namespace(namespace)
				object, err := resource.Get(t.Context(), name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				mutate(object)
				if _, err := resource.Update(t.Context(), object, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario.name {
			case "missing issuer":
				t.Setenv("MCP_MTLS_CLUSTER_ISSUER", "")
			case "missing trust domain":
				t.Setenv("MCP_TRUST_DOMAIN", "")
			case "operator drift":
				t.Setenv("MCP_TRUST_DOMAIN", "different.example")
			case "issuer not Ready":
				update(clusterIssuerGVR, "", "mcp-runtime-ca", func(obj *unstructured.Unstructured) {
					_ = unstructured.SetNestedSlice(obj.Object, []any{map[string]any{"type": "Ready", "status": "False"}}, "status", "conditions")
				})
			case "plain route":
				update(schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}, "mcp-team-acme", "demo", func(obj *unstructured.Unstructured) { unstructured.RemoveNestedField(obj.Object, "spec", "tls") })
			case "wrong TLSOption":
				option := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "TLSOption", "metadata": map[string]any{"name": "no-client-auth", "namespace": "mcp-team-acme"}, "spec": map[string]any{"clientAuth": map[string]any{"clientAuthType": "NoClientCert"}}}}
				if _, err := dyn.Resource(schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "tlsoptions"}).Namespace("mcp-team-acme").Create(t.Context(), option, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				update(schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}, "mcp-team-acme", "demo", func(obj *unstructured.Unstructured) {
					_ = unstructured.SetNestedField(obj.Object, "no-client-auth", "spec", "tls", "options", "name")
				})
			case "old route owner":
				update(schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}, "mcp-team-acme", "demo", func(obj *unstructured.Unstructured) {
					owners := obj.GetOwnerReferences()
					owners[0].UID = "old-server"
					obj.SetOwnerReferences(owners)
				})
			case "stale server readiness":
				update(adapterMCPServerGVR, "mcp-team-acme", "demo", func(obj *unstructured.Unstructured) {
					obj.SetGeneration(2)
					_ = unstructured.SetNestedSlice(obj.Object, []any{map[string]any{"type": "IngressReady", "status": "True", "observedGeneration": int64(1)}}, "status", "conditions")
				})
			}
			req := adapterRequest(t, adapterSessionRequest{ServerName: "demo", Namespace: "mcp-team-acme", AgentID: "agt_01arz3ndektsv4rrffq69g5fav"})
			req = req.WithContext(withPrincipal(req.Context(), fx.principal))
			out := httptest.NewRecorder()
			fx.server.Access().HandleAdapterSession(out, req)
			if out.Code != http.StatusServiceUnavailable || !strings.Contains(out.Body.String(), scenario.reason) {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			sessions, err := fx.server.accessMgr.ListSessions(t.Context(), "mcp-team-acme")
			if err != nil || len(sessions.Items) != 0 {
				t.Fatalf("orphan sessions=%v err=%v", sessions, err)
			}
		})
	}
}

func TestAdapterPreflightUsesOperatorChoiceWithoutRuntimeFlagMigration(t *testing.T) {
	fx := newAdapterTestFixture(t, adapterTestGrant())
	t.Setenv("MCP_ADAPTER_CERTIFICATES", "")
	state := fx.server.Access().adapterCertificateReadiness(t.Context(), "mcp-team-acme", "demo")
	if !state.Ready || !state.AdapterCertificates {
		t.Fatalf("state=%+v", state)
	}
}
