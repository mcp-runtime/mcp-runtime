package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func routedServer(namespace, name, prefix, host string, created time.Time) mcpv1alpha1.MCPServer {
	return mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       mcpv1alpha1.MCPServerSpec{Image: "example.com/" + name, PublicPathPrefix: prefix, IngressHost: host},
	}
}

func TestPublicRouteOwner(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	owner := routedServer("team-a", "foo", "foo", "", t0)
	sameNameOtherTeam := routedServer("team-b", "foo", "foo", "", t0.Add(time.Hour))
	borrowedPrefix := routedServer("team-c", "bar", "foo", "", t0.Add(2*time.Hour))
	otherHost := routedServer("team-d", "foo", "foo", "a.example.com", t0.Add(time.Hour))
	otherHost2 := routedServer("team-e", "foo", "foo", "b.example.com", t0.Add(2*time.Hour))
	distinct := routedServer("team-f", "baz", "baz", "", t0.Add(time.Hour))
	all := []mcpv1alpha1.MCPServer{owner, sameNameOtherTeam, borrowedPrefix, distinct}

	if got := publicRouteOwner(&owner, all); got != nil {
		t.Fatalf("oldest claimant should own the route, got owner %s/%s", got.Namespace, got.Name)
	}
	for _, loser := range []mcpv1alpha1.MCPServer{sameNameOtherTeam, borrowedPrefix} {
		if got := publicRouteOwner(&loser, all); got == nil || got.Namespace != "team-a" {
			t.Fatalf("%s/%s should lose the route to team-a/foo, got %v", loser.Namespace, loser.Name, got)
		}
	}
	if got := publicRouteOwner(&distinct, all); got != nil {
		t.Fatalf("a distinct path must not conflict, got %s/%s", got.Namespace, got.Name)
	}
	// Different explicit hosts do not overlap; a host-less route overlaps both.
	hosts := []mcpv1alpha1.MCPServer{otherHost, otherHost2}
	if got := publicRouteOwner(&otherHost2, hosts); got != nil {
		t.Fatalf("routes on different hosts must not conflict, got %s/%s", got.Namespace, got.Name)
	}
	if got := publicRouteOwner(&otherHost, append(hosts, owner)); got == nil || got.Namespace != "team-a" {
		t.Fatalf("a host-less route matches every host and should win, got %v", got)
	}
}

func TestCheckPublicRouteOwnershipRefusesLaterClaimant(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	owner := routedServer("team-a", "foo", "foo", "", t0)
	loser := routedServer("team-b", "foo", "foo", "", t0.Add(time.Hour))
	staleIngress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "team-b"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&owner, &loser, staleIngress).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme}

	if err := r.checkPublicRouteOwnership(context.Background(), &owner); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	err := r.checkPublicRouteOwnership(context.Background(), &loser)
	if err == nil || !strings.Contains(err.Error(), "team-a/foo") {
		t.Fatalf("err = %v, want a conflict naming the owner", err)
	}
	var ingress networkingv1.Ingress
	if err := c.Get(context.Background(), types.NamespacedName{Name: "foo", Namespace: "team-b"}, &ingress); !apierrors.IsNotFound(err) {
		t.Fatalf("the losing server's Ingress should be removed, got %v", err)
	}
}

func TestReconcileBundledOAuthResourcesSkipsRouteConflicts(t *testing.T) {
	scheme := bundledOAuthScheme(t)
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	owner := oauthMCPServer("foo", "")
	owner.CreationTimestamp = metav1.NewTime(t0)
	owner.Namespace = "team-a"
	loser := oauthMCPServer("foo", "")
	loser.CreationTimestamp = metav1.NewTime(t0.Add(time.Hour))
	loser.Namespace = "team-b"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bundledAuthDeployment(), owner, loser).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, OAuthIssuerURL: testBundledIssuer, DefaultIngressHost: "mcp.example.com", DefaultIngressTLS: true}

	if err := r.reconcileBundledOAuthResources(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := bundledAuthEnv(t, c)["MCP_AUTH_RESOURCES"]; got != "https://mcp.example.com/foo/mcp" {
		t.Fatalf("MCP_AUTH_RESOURCES = %q, want only the route owner's audience", got)
	}
}
