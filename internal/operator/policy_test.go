package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/policy"
)

func TestRenderGatewayPolicyStampsAndValidates(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Tools: []mcpv1alpha1.ToolConfig{
				{Name: "refund_invoice", SideEffect: mcpv1alpha1.ToolSideEffectWrite},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
	r := MCPServerReconciler{Client: client, Scheme: scheme}

	doc, err := r.renderGatewayPolicy(context.Background(), mcpServer)
	if err != nil {
		t.Fatalf("renderGatewayPolicy() error = %v", err)
	}
	if doc.SchemaVersion != policy.SchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", doc.SchemaVersion, policy.SchemaVersion)
	}
	if doc.Revision == "" || !strings.HasPrefix(doc.Revision, "sha256:") {
		t.Fatalf("Revision = %q, want sha256: digest", doc.Revision)
	}
	if doc.GeneratedAt != "" {
		t.Fatalf("GeneratedAt = %q, want empty (set at write time)", doc.GeneratedAt)
	}
	want, err := policy.ComputeRevision(doc)
	if err != nil {
		t.Fatalf("ComputeRevision() error = %v", err)
	}
	if doc.Revision != want {
		t.Fatalf("Revision = %q, want recomputed %q", doc.Revision, want)
	}
	if err := policy.Validate(doc); err != nil {
		t.Fatalf("rendered policy failed validation: %v", err)
	}
}

func TestRenderGatewayPolicyIncludesAdapterTrustWithoutOAuth(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "servers"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Gateway: &mcpv1alpha1.GatewayConfig{Enabled: true},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer).Build()
	r := MCPServerReconciler{
		Client:                     client,
		Scheme:                     scheme,
		AdapterCertificatesEnabled: true,
		AdapterTrustDomain:         "example.org",
		MTLSClusterIssuer:          "mcp-runtime-ca",
	}
	doc, err := r.renderGatewayPolicy(context.Background(), mcpServer)
	if err != nil {
		t.Fatalf("renderGatewayPolicy() error = %v", err)
	}
	if doc.Auth == nil || doc.Auth.TrustDomain != "example.org" {
		t.Fatalf("Auth = %#v, want adapter trust domain", doc.Auth)
	}
	if policy.PolicyUsesOAuth(doc) {
		t.Fatal("adapter trust-only policy must not enable OAuth")
	}
}

func TestRenderPolicyConfigMapDataPreservesUnchangedRevision(t *testing.T) {
	doc := &policy.Document{Server: policy.Server{Name: "demo"}}
	if err := policy.Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}

	// First write: no existing data, generated_at is stamped.
	first, err := renderPolicyConfigMapData("", doc)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}
	var firstDoc policy.Document
	if err := json.Unmarshal([]byte(first), &firstDoc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if firstDoc.GeneratedAt == "" {
		t.Fatal("expected generated_at to be set on fresh write")
	}

	// Re-render with the same content: payload must be preserved verbatim so an
	// unchanged policy does not churn the ConfigMap.
	second := &policy.Document{Server: policy.Server{Name: "demo"}}
	if err := policy.Stamp(second, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	out, err := renderPolicyConfigMapData(first, second)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}
	if out != first {
		t.Fatalf("unchanged revision rewrote payload:\n old=%s\n new=%s", first, out)
	}
}

func TestRenderPolicyConfigMapDataDropsLegacyHeaderIdentityFields(t *testing.T) {
	doc := &policy.Document{
		Server: policy.Server{Name: "demo"},
		Auth: &policy.Auth{
			TrustDomain: "example.org",
			IssuerURL:   "https://issuer.example.com",
			Audience:    "https://mcp.example.com/demo/mcp",
			TokenHeader: "Authorization",
		},
	}
	if err := policy.Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	canonical, err := renderPolicyConfigMapData("", doc)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}

	// Simulate a ConfigMap still carrying removed governance-header identity
	// fields from an older operator. Revision matches, but the bytes must not
	// be preserved verbatim.
	var stored map[string]any
	if err := json.Unmarshal([]byte(canonical), &stored); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	auth, _ := stored["auth"].(map[string]any)
	auth["mode"] = "header"
	auth["human_id_header"] = "X-MCP-Human-ID"
	auth["agent_id_header"] = "X-MCP-Agent-ID"
	auth["team_id_header"] = "X-MCP-Team-ID"
	auth["session_id_header"] = "X-MCP-Agent-Session"
	stored["auth"] = auth
	legacy, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}

	next := &policy.Document{
		Server: policy.Server{Name: "demo"},
		Auth: &policy.Auth{
			TrustDomain: "example.org",
			IssuerURL:   "https://issuer.example.com",
			Audience:    "https://mcp.example.com/demo/mcp",
			TokenHeader: "Authorization",
		},
	}
	if err := policy.Stamp(next, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	out, err := renderPolicyConfigMapData(string(legacy), next)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}
	if strings.Contains(out, "human_id_header") || strings.Contains(out, `"mode"`) {
		t.Fatalf("legacy header identity fields were preserved:\n%s", out)
	}
	var cleaned policy.Document
	if err := json.Unmarshal([]byte(out), &cleaned); err != nil {
		t.Fatalf("Unmarshal(cleaned) error = %v", err)
	}
	if cleaned.Auth == nil || cleaned.Auth.TrustDomain != "example.org" {
		t.Fatalf("cleaned Auth = %#v", cleaned.Auth)
	}
	if cleaned.GeneratedAt == "" {
		t.Fatal("expected generated_at to stay set after schema cleanup")
	}
}

func TestRenderPolicyConfigMapDataRewritesOnChange(t *testing.T) {
	doc := &policy.Document{Server: policy.Server{Name: "demo"}}
	_ = policy.Stamp(doc, "")
	existing, err := renderPolicyConfigMapData("", doc)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}

	changed := &policy.Document{
		Server: policy.Server{Name: "demo"},
		Tools:  []policy.Tool{{Name: "echo", RequiredTrust: "low", SideEffect: "read"}},
	}
	_ = policy.Stamp(changed, "")
	out, err := renderPolicyConfigMapData(existing, changed)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}
	if out == existing {
		t.Fatal("changed revision did not rewrite payload")
	}
	var outDoc policy.Document
	if err := json.Unmarshal([]byte(out), &outDoc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if outDoc.Revision != changed.Revision {
		t.Fatalf("written revision = %q, want %q", outDoc.Revision, changed.Revision)
	}
}

func TestRenderPolicyConfigMapDataRewritesWhenRevisionMetadataTampered(t *testing.T) {
	doc := &policy.Document{Server: policy.Server{Name: "demo"}}
	if err := policy.Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	existing, err := renderPolicyConfigMapData("", doc)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}

	var tampered policy.Document
	if err := json.Unmarshal([]byte(existing), &tampered); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	tampered.Tools = []policy.Tool{{Name: "echo", RequiredTrust: "low", SideEffect: "read"}}
	tamperedData, err := json.MarshalIndent(&tampered, "", "  ")
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	unchanged := &policy.Document{Server: policy.Server{Name: "demo"}}
	if err := policy.Stamp(unchanged, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	out, err := renderPolicyConfigMapData(string(tamperedData), unchanged)
	if err != nil {
		t.Fatalf("renderPolicyConfigMapData() error = %v", err)
	}
	if out == string(tamperedData) {
		t.Fatal("tampered payload with matching revision metadata was preserved")
	}
	var outDoc policy.Document
	if err := json.Unmarshal([]byte(out), &outDoc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if outDoc.Revision != unchanged.Revision {
		t.Fatalf("written revision = %q, want %q", outDoc.Revision, unchanged.Revision)
	}
}

// A policy change (new session, grant, or revocation) must reach the running
// gateway without waiting for the kubelet's periodic ConfigMap volume resync:
// the operator stamps the new revision on the server's pods, which makes the
// kubelet sync the pod and re-project the policy volume immediately.
func TestReconcilePolicyConfigMapAnnotatesServerPodsOnChange(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	const namespace = "mcp-team-acme"
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace-demo", Namespace: namespace, UID: "server-uid"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Gateway: &mcpv1alpha1.GatewayConfig{Enabled: true},
			Tools: []mcpv1alpha1.ToolConfig{
				{Name: "echo", SideEffect: mcpv1alpha1.ToolSideEffectRead},
			},
		},
	}
	serverPod := func(name, app, track string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app":                          app,
				"app.kubernetes.io/managed-by": "mcp-runtime",
				"mcpruntime.org/rollout-track": track,
			},
		}}
	}
	stable := serverPod("workspace-demo-stable", "workspace-demo", "stable")
	canary := serverPod("workspace-demo-canary", "workspace-demo", "canary")
	other := serverPod("other-server-pod", "other-server", "stable")
	terminating := serverPod("workspace-demo-old", "workspace-demo", "stable")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	terminating.Finalizers = []string{"test/keep"}

	kube := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mcpServer, stable, canary, other, terminating).Build()
	r := MCPServerReconciler{Client: kube, Scheme: scheme}
	ctx := context.Background()

	getPod := func(name string) *corev1.Pod {
		t.Helper()
		pod := &corev1.Pod{}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pod); err != nil {
			t.Fatalf("get pod %s: %v", name, err)
		}
		return pod
	}

	// Initial create: pods mount this content at start, nothing to nudge.
	if err := r.reconcilePolicyConfigMap(ctx, mcpServer); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if got := getPod(stable.Name).Annotations[gatewayPolicyRevisionAnnotation]; got != "" {
		t.Fatalf("stable pod annotated on ConfigMap create: %q", got)
	}

	// An adapter session is issued: the ConfigMap changes and the server's
	// running pods must carry the new revision.
	session := &mcpv1alpha1.MCPAgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "adapter-0123456789abcdef", Namespace: namespace},
		Spec: mcpv1alpha1.MCPAgentSessionSpec{
			Subject:   mcpv1alpha1.SubjectRef{HumanID: "user-1", AgentID: "cursor"},
			ServerRef: mcpv1alpha1.ServerReference{Name: "workspace-demo"},
		},
	}
	if err := kube.Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := r.reconcilePolicyConfigMap(ctx, mcpServer); err != nil {
		t.Fatalf("reconcile after session: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: gatewayPolicyConfigMapName("workspace-demo")}, cm); err != nil {
		t.Fatalf("get policy ConfigMap: %v", err)
	}
	var doc policy.Document
	if err := json.Unmarshal([]byte(cm.Data[gatewayPolicyFileName]), &doc); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if len(doc.Sessions) != 1 || string(doc.Sessions[0].Name) != session.Name {
		t.Fatalf("rendered sessions = %+v, want %s", doc.Sessions, session.Name)
	}
	for _, name := range []string{stable.Name, canary.Name} {
		if got := getPod(name).Annotations[gatewayPolicyRevisionAnnotation]; got != doc.Revision {
			t.Fatalf("pod %s annotation = %q, want policy revision %q", name, got, doc.Revision)
		}
	}
	if got := getPod(other.Name).Annotations[gatewayPolicyRevisionAnnotation]; got != "" {
		t.Fatalf("another server's pod was annotated: %q", got)
	}
	if got := getPod(terminating.Name).Annotations[gatewayPolicyRevisionAnnotation]; got != "" {
		t.Fatalf("terminating pod was annotated: %q", got)
	}

	// Unchanged policy: no pod writes.
	before := getPod(stable.Name).ResourceVersion
	if err := r.reconcilePolicyConfigMap(ctx, mcpServer); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if after := getPod(stable.Name).ResourceVersion; after != before {
		t.Fatalf("unchanged policy patched pod: resourceVersion %s -> %s", before, after)
	}
}
