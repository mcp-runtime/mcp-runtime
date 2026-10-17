package sessionapi

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	mcpv1 "mcp-runtime/api/v1alpha1"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type reviewClient struct {
	client.Client
	status   authv1.TokenReviewStatus
	t        *testing.T
	audience string
}

func (c reviewClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	review := obj.(*authv1.TokenReview)
	audience := c.audience
	if audience == "" {
		audience = sessionstate.GatewayAudience
	}
	if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != audience {
		c.t.Fatal("TokenReview audience not scoped")
	}
	review.Status = c.status
	return nil
}

func TestBrokerIdentityRequiresItsOwnAudienceAndLivePodBinding(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		objects, status := identityObjects()
		if accepted {
			status.Audiences = []string{cb.Audience}
		}
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = mcpv1.AddToScheme(scheme)
		kube := reviewClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), status: status, t: t, audience: cb.Audience}
		identity, err := (KubernetesIdentity{Client: kube}).AuthenticateAudience(t.Context(), "projected-broker-token", cb.Audience)
		if (err == nil) != accepted {
			t.Fatalf("dedicated audience accepted=%v err=%v", accepted, err)
		}
		if accepted && identity.UID != "server-uid" {
			t.Fatal("wrong live server generation")
		}
	}
}
func owner(name, kind, version, uid string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{Name: name, Kind: kind, APIVersion: version, UID: types.UID(uid), Controller: &yes}
}
func identityObjects() ([]client.Object, authv1.TokenReviewStatus) {
	objects := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "pod", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{owner("rs", "ReplicaSet", "apps/v1", "rs-uid")}}, Spec: corev1.PodSpec{ServiceAccountName: "shared-tenant"}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rs", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{owner("server", "Deployment", "apps/v1", "deployment-uid")}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "server", UID: "deployment-uid", OwnerReferences: []metav1.OwnerReference{owner("server", "MCPServer", mcpv1.GroupVersion.String(), "server-uid")}}},
		&mcpv1.MCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "server", UID: "server-uid"}},
	}
	status := authv1.TokenReviewStatus{Authenticated: true, Audiences: []string{sessionstate.GatewayAudience}, User: authv1.UserInfo{Username: "system:serviceaccount:tenant:shared-tenant", Extra: map[string]authv1.ExtraValue{"authentication.kubernetes.io/pod-name": {"pod"}, "authentication.kubernetes.io/pod-uid": {"pod-uid"}}}}
	return objects, status
}

func TestGatewayIdentityVerifiesPodAndServerGeneration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]client.Object, *authv1.TokenReviewStatus)
		allow  bool
	}{
		{"valid", func([]client.Object, *authv1.TokenReviewStatus) {}, true},
		{"wrong audience", func(_ []client.Object, s *authv1.TokenReviewStatus) { s.Audiences = []string{"other"} }, false},
		{"unbound shared service account", func(_ []client.Object, s *authv1.TokenReviewStatus) { s.User.Extra = nil }, false},
		{"recreated pod", func(o []client.Object, _ *authv1.TokenReviewStatus) { o[0].SetUID("new-pod") }, false},
		{"wrong service account", func(o []client.Object, _ *authv1.TokenReviewStatus) {
			o[0].(*corev1.Pod).Spec.ServiceAccountName = "other"
		}, false},
		{"labels cannot impersonate server", func(o []client.Object, _ *authv1.TokenReviewStatus) {
			o[0].SetOwnerReferences(nil)
			o[0].SetLabels(map[string]string{"mcpruntime.org/server": "server"})
		}, false},
		{"recreated replica set", func(o []client.Object, _ *authv1.TokenReviewStatus) { o[1].SetUID("new-rs") }, false},
		{"recreated deployment", func(o []client.Object, _ *authv1.TokenReviewStatus) { o[2].SetUID("new-deployment") }, false},
		{"republished server", func(o []client.Object, _ *authv1.TokenReviewStatus) { o[3].SetUID("new-server") }, false},
		{"retiring server", func(o []client.Object, _ *authv1.TokenReviewStatus) {
			o[3].SetAnnotations(map[string]string{"mcpruntime.org/retiring": "true"})
		}, false},
		{"non-controller reference", func(o []client.Object, _ *authv1.TokenReviewStatus) {
			refs := o[2].GetOwnerReferences()
			refs[0].Controller = nil
			o[2].SetOwnerReferences(refs)
		}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			objects, status := identityObjects()
			tt.mutate(objects, &status)
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)
			_ = mcpv1.AddToScheme(scheme)
			c := reviewClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), status: status, t: t}
			server, err := (KubernetesIdentity{Client: c}).Authenticate(context.Background(), "projected-token")
			if (err == nil) != tt.allow {
				t.Fatalf("allow=%v server=%+v err=%v", tt.allow, server, err)
			}
			if tt.allow && (server.Namespace != "tenant" || server.UID != "server-uid" || server.Name != "server") {
				t.Fatalf("wrong scope %+v", server)
			}
		})
	}
}
