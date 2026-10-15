package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		name                              string
		missing, deny, fail, secretDenied bool
	}{
		{name: "owned"}, {name: "orphan recovery", missing: true}, {name: "denied", deny: true}, {name: "partial cleanup", fail: true},
		{name: "credential permission denied", secretDenied: true},
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
			kube := kubernetesfake.NewSimpleClientset(secret, pull)
			api := &RuntimeServer{k8sClients: &k8sclient.Clients{Dynamic: dynamic, Clientset: kube}}
			failed := false
			if tc.secretDenied {
				kube.PrependReactor("delete", "secrets", func(a clienttesting.Action) (bool, runtime.Object, error) {
					if !failed {
						failed = true
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, secret.Name, errors.New("RBAC denied"))
					}
					return false, nil, nil
				})
			}
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
				if len(kube.Actions()) != 0 {
					t.Fatal("unauthorized credential or RBAC access")
				}
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
			if tc.fail || tc.secretDenied {
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("status %d", w.Code)
				}
				current, err := api.controlPlane().GetServer(context.Background(), "team-a", "demo")
				if err != nil || current.Annotations[mcpaccess.ServerRetiringAnnotation] != "true" {
					t.Fatalf("missing retirement marker: %+v %v", current, err)
				}
				if tc.secretDenied {
					manager := mcpaccess.NewManager(dynamic, nil)
					if _, err := manager.GetGrant(context.Background(), "grant", "team-b"); !apierrors.IsNotFound(err) {
						t.Fatalf("grant survived credential cleanup failure: %v", err)
					}
					if _, err := manager.GetSession(context.Background(), "session", "team-b"); !apierrors.IsNotFound(err) {
						t.Fatalf("session survived credential cleanup failure: %v", err)
					}
					if _, err := kube.CoreV1().Secrets("team-a").Get(context.Background(), secret.Name, metav1.GetOptions{}); err != nil {
						t.Fatalf("credential lost after denied cleanup: %v", err)
					}
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

func TestRetirementRepairsMissingNamespaceCredentialBinding(t *testing.T) {
	for _, tc := range []struct {
		name, namespace            string
		missing, denied, protected bool
	}{
		{name: "existing credential", namespace: "mcp-servers"},
		{name: "absent credential", namespace: "mcp-servers", missing: true},
		{name: "persistent denial", namespace: "mcp-servers", denied: true},
		{name: "protected namespace", namespace: "mcp-platform", protected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			name := "demo-analytics-creds"
			kube := kubernetesfake.NewSimpleClientset()
			if !tc.missing {
				_, err := kube.CoreV1().Secrets(tc.namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tc.namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			kube.PrependReactor("get", "secrets", func(a clienttesting.Action) (bool, runtime.Object, error) {
				reads++
				// Simulate an authorizer which observes the new binding after one retry.
				if reads <= 2 || tc.denied || tc.protected {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, name, errors.New("namespace binding missing"))
				}
				return false, nil, nil
			})
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			api := &RuntimeServer{k8sClients: &k8sclient.Clients{Clientset: kube, Dynamic: dynamicfake.NewSimpleDynamicClient(scheme)}}
			err := api.retireServerCredentials(ctx, tc.namespace, "demo", nil)
			if tc.denied || tc.protected {
				if err == nil {
					t.Fatal("credential denial was ignored")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			bindings, err := kube.RbacV1().RoleBindings(tc.namespace).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.protected {
				if len(bindings.Items) != 0 {
					t.Fatal("granted credential access to infrastructure")
				}
				return
			}
			if len(bindings.Items) != 1 {
				t.Fatalf("expected one namespace binding: %+v", bindings.Items)
			}
			binding := bindings.Items[0]
			if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != platformNamespaceAPISecretAccessName || len(binding.Subjects) != 1 || binding.Subjects[0].Name != platformNamespaceAPIServiceAccountName || binding.Subjects[0].Namespace != "mcp-platform" {
				t.Fatalf("unexpected access binding: %+v", binding)
			}
			if !tc.denied && !tc.missing {
				if _, err := kube.CoreV1().Secrets(tc.namespace).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
					t.Fatalf("credential survived: %v", err)
				}
			}
		})
	}
}

func TestRetiringServerAccessManagementHandlers(t *testing.T) {
	operations := []struct{ kind, method, suffix, body string }{
		{"grants", http.MethodDelete, "", ""},
		{"grants", http.MethodPatch, "", `{"disabled":true}`},
		{"grants", http.MethodPatch, "", `{"disabled":false}`},
		{"grants", http.MethodPost, "/disable", ""},
		{"grants", http.MethodPost, "/enable", ""},
		{"sessions", http.MethodDelete, "", ""},
		{"sessions", http.MethodPatch, "", `{"revoked":true}`},
		{"sessions", http.MethodPatch, "", `{"revoked":false}`},
		{"sessions", http.MethodPost, "/revoke", ""},
		{"sessions", http.MethodPost, "/unrevoke", ""},
	}
	for _, deleting := range []bool{false, true} {
		for _, role := range []string{"admin", "owner", "member"} {
			for _, op := range operations {
				t.Run(fmt.Sprintf("%t/%s/%s/%s/%s/%s", deleting, role, op.kind, op.method, op.suffix, op.body), func(t *testing.T) {
					scheme := runtime.NewScheme()
					_ = mcpv1alpha1.AddToScheme(scheme)
					server := ownedTestMCPServer("demo", "team-a", "owner")
					if deleting {
						now := metav1.Now()
						server.DeletionTimestamp = &now
					} else {
						server.Annotations = map[string]string{mcpaccess.ServerRetiringAnnotation: "true"}
					}
					grant := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: "team-a"}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "demo", Namespace: "team-a"}}}
					session := &mcpv1alpha1.MCPAgentSession{ObjectMeta: grant.ObjectMeta, Spec: mcpv1alpha1.MCPAgentSessionSpec{ServerRef: grant.Spec.ServerRef}}
					dyn := dynamicfake.NewSimpleDynamicClient(scheme, server, grant, session)
					api := &RuntimeServer{accessMgr: mcpaccess.NewManager(dyn, nil)}
					p := principal{Role: roleAdmin, Subject: "owner", Namespace: "team-a"}
					if role != "admin" {
						p.Role = roleUser
						p.Subject = role
						teamRole := teamRoleOwner
						if role == "member" {
							teamRole = teamRoleMember
						}
						p.Teams = []principalTeam{{ID: "team-a-id", Namespace: "team-a", Role: teamRole}}
					}
					req := httptest.NewRequest(op.method, "/api/runtime/"+op.kind+"/team-a/access"+op.suffix, strings.NewReader(op.body))
					req = req.WithContext(withPrincipal(req.Context(), p))
					w := httptest.NewRecorder()
					if op.kind == "grants" {
						api.Access().HandleGrantItemPath(w, req)
					} else {
						api.Access().HandleSessionItemPath(w, req)
					}
					expected := http.StatusOK
					if role == "member" {
						expected = http.StatusForbidden
					}
					if w.Code != expected {
						t.Fatalf("status=%d expected=%d body=%s", w.Code, expected, w.Body.String())
					}
					if role == "member" {
						for _, a := range dyn.Actions() {
							if a.GetVerb() == "delete" || a.GetVerb() == "patch" || a.GetVerb() == "create" || a.GetVerb() == "update" {
								t.Fatal("unauthorized access mutation")
							}
						}
					}
				})
			}
		}
	}
}

func TestRetiringServerIssuanceHandlersRejectConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	server := ownedTestMCPServer("demo", "team-a", "owner")
	server.Annotations = map[string]string{mcpaccess.ServerRetiringAnnotation: "true"}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, server)
	api := &RuntimeServer{accessMgr: mcpaccess.NewManager(dyn, nil)}
	for _, kind := range []string{"grants", "sessions"} {
		body := `{"name":"new","namespace":"team-a","serverRef":{"name":"demo"},"subject":{"humanID":"owner"},"maxTrust":"low","allowedSideEffects":["read"]}`
		if kind == "sessions" {
			body = `{"name":"new","namespace":"team-a","serverRef":{"name":"demo"},"subject":{"humanID":"owner"},"consentedTrust":"low"}`
		}
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/"+kind, strings.NewReader(body))
		req = req.WithContext(withPrincipal(req.Context(), principal{Role: roleAdmin, Subject: "owner"}))
		w := httptest.NewRecorder()
		if kind == "grants" {
			api.Access().HandleRuntimeGrants(w, req)
		} else {
			api.Access().HandleRuntimeSessions(w, req)
		}
		if w.Code != http.StatusConflict {
			t.Fatalf("%s status=%d body=%s", kind, w.Code, w.Body.String())
		}
	}
	if _, _, err := api.Access().selectAdapterGrant(context.Background(), "team-a", "demo", "owner", "", nil, "", true); !apierrors.IsConflict(err) {
		t.Fatalf("adapter issuance accepted retiring target: %v", err)
	}
}
