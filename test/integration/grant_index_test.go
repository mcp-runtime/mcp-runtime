package integration

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
	"time"
)

// Exercise the actual manager field index and informer updates, rather than
// the fake client's field-selector implementation. No existing cluster is used.
func testGrantTargetCacheIndex(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) {
	mgr, c, cancel := startManager(t, cfg, scheme)
	defer cancel()
	ctx := context.Background()
	for _, ns := range []string{"grant-index-a", "grant-index-b", "grant-index-recipient"} {
		createNamespace(t, c, ns)
	}
	grant := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "cross", Namespace: "grant-index-recipient"}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "server", Namespace: "grant-index-a"}, Subject: mcpv1alpha1.SubjectRef{HumanID: "human"}}}
	local := &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: "grant-index-a"}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: "server"}, Subject: mcpv1alpha1.SubjectRef{HumanID: "human"}}}
	for _, g := range []*mcpv1alpha1.MCPAccessGrant{grant, local} {
		if err := c.Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(a, b int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			counts := []int{}
			for _, ns := range []string{"grant-index-a", "grant-index-b"} {
				var list mcpv1alpha1.MCPAccessGrantList
				if err := mgr.GetClient().List(ctx, &list, client.MatchingFields{"mcpruntime.org/effective-target-server": ns + "/server"}); err != nil {
					t.Fatal(err)
				}
				counts = append(counts, len(list.Items))
			}
			if counts[0] == a && counts[1] == b {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("index counts=%v, want [%d %d]", counts, a, b)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait(2, 0)
	// Use the uncached reader to avoid updating a stale resource version.
	if err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(grant), grant); err != nil {
		t.Fatal(err)
	}
	grant.Spec.ServerRef.Namespace = "grant-index-b"
	if err := c.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	wait(1, 1)
	if err := c.Delete(ctx, grant); err != nil {
		t.Fatal(err)
	}
	wait(1, 0)
}
