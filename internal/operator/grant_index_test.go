package operator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

func indexedGrant(name, namespace, server, targetNamespace string) *mcpv1alpha1.MCPAccessGrant {
	return &mcpv1alpha1.MCPAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: mcpv1alpha1.MCPAccessGrantSpec{ServerRef: mcpv1alpha1.ServerReference{Name: server, Namespace: targetNamespace}, Subject: mcpv1alpha1.SubjectRef{HumanID: "human"}}}
}

func TestGrantIndexRenderingAndLifecycle(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	a := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "a"}}
	b := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "b"}}
	moved := indexedGrant("cross", "recipient", "server", " a ")
	c := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&mcpv1alpha1.MCPAccessGrant{}, grantTargetServerIndex, grantTargetServerKeys).WithObjects(indexedGrant("implicit", "a", "server", ""), indexedGrant("other", "b", "server", ""), indexedGrant("unrelated", "a", "other", ""), moved).Build()
	r := &MCPServerReconciler{Client: c, Scheme: scheme}
	check := func(server *mcpv1alpha1.MCPServer, count int) {
		t.Helper()
		doc, err := r.renderGatewayPolicy(ctx, server)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Grants) != count {
			t.Fatalf("%s grants = %v, want %d", server.Namespace, doc.Grants, count)
		}
	}
	check(a, 2)
	check(b, 1)
	previous := moved.DeepCopy()
	moved.Spec.ServerRef.Namespace = "b"
	if err := c.Update(ctx, moved); err != nil {
		t.Fatal(err)
	}
	// The controller-runtime map handler queues both the old and new targets.
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]())
	defer q.ShutDown()
	handler.EnqueueRequestsFromMapFunc(r.requestsForReferencedServer).Update(ctx, event.UpdateEvent{ObjectOld: previous, ObjectNew: moved}, q)
	if q.Len() != 2 {
		t.Fatalf("move queued %d servers, want 2", q.Len())
	}
	seen := map[types.NamespacedName]bool{}
	for q.Len() > 0 {
		req, _ := q.Get()
		seen[req.NamespacedName] = true
		q.Done(req)
	}
	if !seen[types.NamespacedName{Namespace: "a", Name: "server"}] || !seen[types.NamespacedName{Namespace: "b", Name: "server"}] {
		t.Fatalf("wrong targets: %v", seen)
	}
	check(a, 1)
	check(b, 2)
	if err := c.Delete(ctx, moved); err != nil {
		t.Fatal(err)
	}
	check(a, 1)
	check(b, 1)
	if err := c.Create(ctx, indexedGrant("created", "recipient", "server", "a")); err != nil {
		t.Fatal(err)
	}
	check(a, 2)
}

func TestGrantIndexRequired(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	r := &MCPServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	if _, err := r.renderGatewayPolicy(context.Background(), &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "a"}}); err == nil {
		t.Fatal("missing index was hidden")
	}
}

type failingGrantIndexer struct{ err error }

func (f failingGrantIndexer) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return f.err
}
func TestGrantIndexRegistrationError(t *testing.T) {
	want := errors.New("index unavailable")
	if err := registerGrantTargetServerIndex(context.Background(), failingGrantIndexer{want}); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

// BenchmarkGrantTargetLookup isolates the informer lookup and deep-copy work
// replaced by MatchingFields. It does not measure full reconciliation or API I/O.
func BenchmarkGrantTargetLookup(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{grantTargetServerIndex: func(obj interface{}) ([]string, error) { return grantTargetServerKeys(obj.(client.Object)), nil }})
		for i := 0; i < n; i++ {
			_ = store.Add(indexedGrant(fmt.Sprintf("unrelated-%d", i), "other", "unrelated", ""))
		}
		for i := 0; i < 3; i++ {
			_ = store.Add(indexedGrant(fmt.Sprintf("target-%d", i), "recipient", "server", "target"))
		}
		server := &mcpv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "target"}}
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("unrelated=%d/indexed=%t", n, indexed), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var objects []interface{}
					if indexed {
						var err error
						objects, err = store.ByIndex(grantTargetServerIndex, targetServerKey(server.Namespace, server.Name))
						if err != nil {
							b.Fatal(err)
						}
					} else {
						objects = store.List()
					}
					grants := make([]mcpv1alpha1.MCPAccessGrant, 0, len(objects))
					for _, obj := range objects {
						grants = append(grants, *obj.(*mcpv1alpha1.MCPAccessGrant).DeepCopy())
					}
					count := 0
					for _, grant := range grants {
						if serverReferenceMatches(grant.Namespace, grant.Spec.ServerRef, server) {
							count++
						}
					}
					if count != 3 {
						b.Fatalf("matches=%d", count)
					}
				}
			})
		}
	}
}
