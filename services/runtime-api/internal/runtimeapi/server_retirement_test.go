package runtimeapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
)

func TestServerRetirementAuthorizationAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                string
		missing, deny, fail bool
	}{
		{name: "owned"}, {name: "orphan recovery", missing: true}, {name: "denied", deny: true}, {name: "partial cleanup", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			server := ownedTestMCPServer("demo", "team-a", "owner")
			grant := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-b"}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "team-a"}}}
			session := &mcpv1alpha1.MCPAgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "team-b"}, Spec: mcpv1alpha1.MCPAgentSessionSpec{ServerRef: grant.Spec.ServerRef}}
			objects := []runtime.Object{grant, session}
			if !tc.missing {
				objects = append(objects, server)
			}
			dynamic := dynamicfake.NewSimpleDynamicClient(scheme, objects...)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "demo-analytics-creds", Namespace: "team-a", Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}}
			pull := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-pull", Namespace: "team-a"}, Type: corev1.SecretTypeDockerConfigJson}
			api := &RuntimeServer{k8sClients: &k8sclient.Clients{Dynamic: dynamic, Clientset: kubernetesfake.NewSimpleClientset(secret, pull)}}
			failed := false
			if tc.fail {
				dynamic.PrependReactor("delete", mcpv1alpha1.MCPAgentSessionResource, func(a clienttesting.Action) (bool, runtime.Object, error) {
					if !failed {
						failed = true
						return true, nil, errors.New("injected failure")
					}
					return false, nil, nil
				})
			}
			p := principal{Role: roleAdmin, Subject: "owner", Namespace: "team-a"}
			if tc.deny {
				p.Role = roleUser
				p.Subject = "other"
			}
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodDelete, "/api/runtime/servers/team-a/demo", nil)
				r = r.WithContext(withPrincipal(r.Context(), p))
				w := httptest.NewRecorder()
				api.HandleRuntimeServerItem(w, r)
				return w
			}
			w := request()
			if tc.deny {
				if w.Code != http.StatusForbidden {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				if _, err := dynamic.Resource(controlplane.MCPServerGVR).Namespace("team-a").Get(context.Background(), "demo", metav1.GetOptions{}); err != nil {
					t.Fatal(err)
				}
				for _, a := range dynamic.Actions() {
					if a.GetVerb() == "patch" || a.GetVerb() == "delete" {
						t.Fatal("unauthorized mutation")
					}
				}
				return
			}
			if tc.fail {
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("status %d", w.Code)
				}
				current, err := api.controlPlane().GetServer(context.Background(), "team-a", "demo")
				if err != nil || current.Annotations[mcpaccess.ServerRetiringAnnotation] != "true" {
					t.Fatalf("missing retirement marker: %+v %v", current, err)
				}
				w = request()
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			manager := mcpaccess.NewManager(dynamic, nil)
			if _, err := manager.GetGrant(context.Background(), "grant", "team-b"); !apierrors.IsNotFound(err) {
				t.Fatalf("grant remains: %v", err)
			}
			if _, err := manager.GetSession(context.Background(), "session", "team-b"); !apierrors.IsNotFound(err) {
				t.Fatalf("session remains: %v", err)
			}
			if _, err := api.k8sClients.Clientset.CoreV1().Secrets("team-a").Get(context.Background(), secret.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("credential remains: %v", err)
			}
			if _, err := api.k8sClients.Clientset.CoreV1().Secrets("team-a").Get(context.Background(), pull.Name, metav1.GetOptions{}); err != nil {
				t.Fatalf("shared pull lost: %v", err)
			}
			if w := request(); w.Code != http.StatusOK {
				t.Fatalf("retry status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRetirementPreservesSharedAnalyticsCredential(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	other := ownedTestMCPServer("other", "team-a", "owner")
	other.Spec.Analytics = &mcpv1alpha1.AnalyticsConfig{APIKeySecretRef: &mcpv1alpha1.SecretKeyRef{Name: "demo-analytics-creds", Key: "api-key"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "demo-analytics-creds", Namespace: "team-a", Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}}
	api := &RuntimeServer{k8sClients: &k8sclient.Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(scheme, other), Clientset: kubernetesfake.NewSimpleClientset(secret)}}
	if err := api.retireServerCredentials(context.Background(), "team-a", "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := api.k8sClients.Clientset.CoreV1().Secrets("team-a").Get(context.Background(), secret.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("shared credential deleted")
	}
}
