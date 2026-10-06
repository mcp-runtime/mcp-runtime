package update

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/platformrelease"
)

// Plan actions.
const (
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	ActionSkip      = "skip"
	ActionBlocked   = "blocked"
)

// preservedResources is the fixed list of resource classes update never modifies.
var preservedResources = []string{
	"Secrets (credentials, registry pull secrets, TLS material)",
	"PersistentVolumeClaims and PersistentVolumes (ClickHouse, Kafka, Postgres, registry data)",
	"ConfigMaps (mcp-shared-config, operator and gateway config)",
	"cert-manager Issuers, ClusterIssuers, and Certificates",
	"Existing MCP Runtime custom resources (MCPServer, grants, sessions)",
	"Services, Ingresses, IngressRoutes, NetworkPolicies, and RBAC",
	"Deployment replicas, resources, env (except MCP_GATEWAY_PROXY_IMAGE), and probes",
}

// ClusterInfo identifies the target cluster shown before any change.
type ClusterInfo struct {
	Context   string `json:"context"`
	Server    string `json:"server"`
	ClusterID string `json:"clusterID"`
}

// Row is one component line in the plan.
type Row struct {
	Component      string   `json:"component"`
	Namespace      string   `json:"namespace,omitempty"`
	Deployment     string   `json:"deployment,omitempty"`
	Container      string   `json:"container,omitempty"`
	EnvVar         string   `json:"envVar,omitempty"`
	CurrentImage   string   `json:"currentImage,omitempty"`
	CurrentDigest  string   `json:"currentDigest,omitempty"`
	TargetImage    string   `json:"targetImage,omitempty"`
	CurrentVersion string   `json:"currentVersion,omitempty"`
	TargetVersion  string   `json:"targetVersion,omitempty"`
	Action         string   `json:"action"`
	Reason         string   `json:"reason,omitempty"`
	Notes          []string `json:"notes,omitempty"`

	component platformrelease.Component
}

// Plan is the full update plan.
type Plan struct {
	Cluster          ClusterInfo `json:"cluster"`
	ManifestSource   string      `json:"manifestSource"`
	TargetVersion    string      `json:"targetVersion"`
	InstalledVersion string      `json:"installedVersion,omitempty"`
	// ApplyCRDs is true when this release embeds CustomResourceDefinitions
	// that must be applied before image rollouts.
	ApplyCRDs bool `json:"applyCRDs,omitempty"`
	// CRDNames lists CustomResourceDefinition metadata.name values to apply.
	CRDNames []string `json:"crdNames,omitempty"`
	// CRDPreview lists per-CRD create/update/skipped intents after live compare.
	CRDPreview []CRDResult `json:"crdPreview,omitempty"`
	// ImageBuilds lists unique Built images to build or reuse when --build is set.
	ImageBuilds []ImageBuildAction `json:"imageBuilds,omitempty"`
	// crdsYAML is the multi-document YAML applied when ApplyCRDs is true.
	// It is omitted from JSON output (large); use CRDNames in plans.
	crdsYAML  string
	Rows      []Row    `json:"components"`
	Preserved []string `json:"preservedResources"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Changed returns the rows that will be updated.
func (p *Plan) Changed() []Row {
	var out []Row
	for _, r := range p.Rows {
		if r.Action == ActionUpdate {
			out = append(out, r)
		}
	}
	return out
}

// NeedsApply reports whether the plan would mutate the cluster (CRDs and/or images).
func (p *Plan) NeedsApply() bool {
	return p != nil && (p.ApplyCRDs || len(p.Changed()) > 0)
}

// Blocked returns rows blocked by a safety rule.
func (p *Plan) Blocked() []Row {
	var out []Row
	for _, r := range p.Rows {
		if r.Action == ActionBlocked {
			out = append(out, r)
		}
	}
	return out
}

// Selection controls which components are considered.
type Selection struct {
	Only               []string
	IncludeAuth        bool
	IncludeCertManager bool
	AllowDowngrade     bool
}

func (s Selection) onlySet() map[string]bool {
	if len(s.Only) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, n := range s.Only {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		set[n] = true
	}
	return set
}

// validate rejects unknown --only names.
func (s Selection) validate() error {
	var unknown []string
	for _, n := range s.Only {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := platformrelease.Lookup(n); !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return core.NewWithBase(core.ErrUpdateInvalidFlag, fmt.Sprintf("--only has unknown component(s) %s; known components: %s", strings.Join(unknown, ", "), strings.Join(platformrelease.ComponentNames(), ", ")))
	}
	return nil
}

// checkInstalled refuses clusters that are not MCP Runtime installs.
func checkInstalled(ctx context.Context, cs kubernetes.Interface) error {
	if _, err := cs.CoreV1().Namespaces().Get(ctx, platformrelease.OperatorNamespace, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return core.NewWithBase(core.ErrUpdateNotInstalled, fmt.Sprintf("namespace %q not found; this cluster does not look like an MCP Runtime install (run `mcp-runtime setup` first, or check --context)", platformrelease.OperatorNamespace))
		}
		return core.WrapWithBase(core.ErrUpdateInventoryFailed, err, fmt.Sprintf("read namespace %q: %v", platformrelease.OperatorNamespace, err))
	}
	if _, err := cs.AppsV1().Deployments(platformrelease.OperatorNamespace).Get(ctx, platformrelease.OperatorDeployment, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return core.NewWithBase(core.ErrUpdateNotInstalled, fmt.Sprintf("deployment %s/%s not found; this cluster does not look like an MCP Runtime install (run `mcp-runtime setup` first, or check --context)", platformrelease.OperatorNamespace, platformrelease.OperatorDeployment))
		}
		return core.WrapWithBase(core.ErrUpdateInventoryFailed, err, fmt.Sprintf("read operator deployment: %v", err))
	}
	return nil
}

// BuildPlan reads the installed inventory and diffs it with the manifest.
// It only reads: namespaces, deployments, and pods. When the manifest marks
// a CRD change, CRD YAML must already be present on the Manifest (embedded,
// --crds, or fetched platform-crds.yaml).
func BuildPlan(ctx context.Context, cs kubernetes.Interface, m *platformrelease.Manifest, sel Selection) (*Plan, error) {
	if err := sel.validate(); err != nil {
		return nil, err
	}
	if err := checkInstalled(ctx, cs); err != nil {
		return nil, err
	}
	plan := &Plan{TargetVersion: m.Version, Preserved: append([]string(nil), preservedResources...)}
	if m.CRDChange {
		if len(sel.Only) > 0 {
			// Scoped image updates must not silently mutate cluster CRDs.
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("release %s changes CustomResourceDefinitions, but --only is set so CRD apply is skipped; re-run without --only to apply schema changes", m.Version))
		} else {
			crds := strings.TrimSpace(m.CRDs)
			if crds == "" {
				return nil, core.NewWithBase(core.ErrUpdateCRDChange, fmt.Sprintf("release %s changes CustomResourceDefinitions but the manifest has no embedded crds; pass --crds <path|https-url> or use a release that publishes platform-manifest.json with crds / platform-crds.yaml", m.Version))
			}
			filtered, names, err := platformrelease.FilterCRDBundle(crds)
			if err != nil {
				return nil, core.WrapWithBase(core.ErrUpdateManifestInvalid, err, fmt.Sprintf("release %s CRD bundle: %v", m.Version, err))
			}
			if len(names) == 0 {
				return nil, core.NewWithBase(core.ErrUpdateManifestInvalid, fmt.Sprintf("release %s CRD bundle contains no CustomResourceDefinition objects", m.Version))
			}
			plan.ApplyCRDs = true
			plan.CRDNames = names
			plan.crdsYAML = filtered
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("release %s applies CustomResourceDefinitions before image rollouts: %s", m.Version, strings.Join(names, ", ")))
		}
	}
	only := sel.onlySet()
	installedVersions := map[string]bool{}

	for _, c := range platformrelease.Catalog() {
		row := Row{Component: c.Name, Namespace: c.Namespace, Deployment: c.Deployment, Container: c.Container, EnvVar: c.EnvVar, component: c}
		if reason, ok := selectionSkipReason(c, only, sel); !ok {
			row.Action, row.Reason = ActionSkip, reason
			plan.Rows = append(plan.Rows, row)
			continue
		}
		entry, ok := m.Component(c.Name)
		if !ok {
			row.Action, row.Reason = ActionSkip, "not in release manifest"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		row.TargetVersion = entry.Tag
		if c.Built {
			row.TargetVersion = platformrelease.ReleaseVersionFromImageTag(entry.Tag)
		}
		if !c.HasWorkload() {
			row.Action, row.Reason = ActionSkip, "on-demand image; no workload to update"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		if err := planComponent(ctx, cs, m, entry, &row); err != nil {
			return nil, err
		}
		if c.Built && row.CurrentVersion != "" && row.Action != ActionSkip {
			installedVersions[row.CurrentVersion] = true
		}
		applyVersionGuards(&row, sel)
		plan.Rows = append(plan.Rows, row)
	}
	plan.InstalledVersion = summarizeVersions(installedVersions)
	for _, r := range plan.Rows {
		if r.Action == ActionUpdate && r.component.Note != "" {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: %s", r.Component, r.component.Note))
		}
	}
	return plan, nil
}

func selectionSkipReason(c platformrelease.Component, only map[string]bool, sel Selection) (string, bool) {
	if only != nil {
		if !only[c.Name] {
			return "not selected (--only)", false
		}
		return "", true
	}
	switch c.OptIn {
	case platformrelease.OptInAuth:
		if !sel.IncludeAuth {
			return "opt-in; pass --include-auth", false
		}
	case platformrelease.OptInCertManager:
		if !sel.IncludeCertManager {
			return "opt-in; pass --include-cert-manager", false
		}
	}
	return "", true
}

func planComponent(ctx context.Context, cs kubernetes.Interface, m *platformrelease.Manifest, entry platformrelease.ManifestComponent, row *Row) error {
	c := row.component
	deploy, err := cs.AppsV1().Deployments(c.Namespace).Get(ctx, c.Deployment, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		row.Action, row.Reason = ActionSkip, "not installed"
		return nil
	}
	if err != nil {
		return core.WrapWithBase(core.ErrUpdateInventoryFailed, err, fmt.Sprintf("read deployment %s/%s: %v", c.Namespace, c.Deployment, err))
	}
	container := findContainer(deploy.Spec.Template.Spec.Containers, c.Container)
	if container == nil {
		row.Action, row.Reason = ActionSkip, fmt.Sprintf("container %q not found in deployment", c.Container)
		return nil
	}
	current := container.Image
	if c.EnvVar != "" {
		current = envValue(container.Env, c.EnvVar)
		if current == "" {
			row.Action, row.Reason = ActionSkip, fmt.Sprintf("%s not set on %s", c.EnvVar, c.Deployment)
			return nil
		}
	}
	row.CurrentImage = current
	currentRef, err := platformrelease.ParseImageRef(current)
	if err != nil {
		row.Action, row.Reason = ActionSkip, fmt.Sprintf("cannot parse current image: %v", err)
		return nil
	}
	row.CurrentVersion = currentRef.Tag
	if c.Built {
		row.CurrentVersion = platformrelease.ReleaseVersionFromImageTag(currentRef.Tag)
	}
	// A semver image tag is authoritative (it cannot go stale after a manual
	// `kubectl set image`); the setup/update annotation fills in for
	// non-semver tags such as test-mode "latest".
	if _, tagErr := platformrelease.ParseVersion(currentRef.Tag); tagErr != nil {
		if v := deploy.Annotations[platformrelease.AnnotationVersion]; c.Built && v != "" && c.EnvVar == "" {
			row.CurrentVersion = v
		}
	}
	row.CurrentDigest = currentRef.Digest
	if row.CurrentDigest == "" && c.EnvVar == "" {
		row.CurrentDigest = runningDigest(ctx, cs, deploy.Namespace, deploy.Spec.Selector, c.Container)
	}

	target, err := m.TargetRef(entry, currentRef.Registry)
	if err != nil {
		return core.WrapWithBase(core.ErrUpdateManifestInvalid, err, fmt.Sprintf("resolve %s target image: %v", c.Name, err))
	}
	row.TargetImage = target.String()

	sameName := target.Name() == currentRef.Name() && target.Tag == currentRef.Tag
	switch {
	case !sameName:
		row.Action = ActionUpdate
	case target.Digest == "":
		row.Action = ActionUnchanged
		if currentRef.Digest != "" {
			// Keep the pinned digest; the manifest does not ask for a different build.
			row.TargetImage = currentRef.String()
		}
	case currentRef.Digest == target.Digest:
		row.Action = ActionUnchanged
	case currentRef.Digest == "" && row.CurrentDigest == target.Digest:
		// Running the requested build, but pin the spec so later runs are exact.
		row.Action = ActionUpdate
		row.Notes = append(row.Notes, "running digest already matches; pinning spec to digest")
	default:
		row.Action = ActionUpdate
	}
	if row.Action == ActionUnchanged {
		row.Reason = "image matches release"
	}
	return nil
}

func applyVersionGuards(row *Row, sel Selection) {
	if row.Action != ActionUpdate {
		return
	}
	cmp, ok := platformrelease.CompareVersionStrings(row.TargetVersion, row.CurrentVersion)
	c := row.component
	if c.OptIn == platformrelease.OptInCertManager {
		cur, errA := platformrelease.ParseVersion(row.CurrentVersion)
		tgt, errB := platformrelease.ParseVersion(row.TargetVersion)
		if errA != nil || errB != nil || !cur.SameMinor(tgt) {
			row.Action = ActionBlocked
			row.Reason = fmt.Sprintf("cert-manager %s -> %s is not a patch update; update does not upgrade cert-manager CRDs, follow https://cert-manager.io/docs/installation/upgrade/", row.CurrentVersion, row.TargetVersion)
			return
		}
	}
	if !ok {
		row.Notes = append(row.Notes, fmt.Sprintf("version %q or %q is not semver; downgrade check skipped", row.CurrentVersion, row.TargetVersion))
		return
	}
	if cmp < 0 && !sel.AllowDowngrade {
		row.Action = ActionBlocked
		row.Reason = fmt.Sprintf("downgrade %s -> %s; pass --allow-downgrade to permit", row.CurrentVersion, row.TargetVersion)
	}
}

func summarizeVersions(set map[string]bool) string {
	if len(set) == 0 {
		return ""
	}
	vs := make([]string, 0, len(set))
	for v := range set {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	if len(vs) == 1 {
		return vs[0]
	}
	return "mixed (" + strings.Join(vs, ", ") + ")"
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return strings.TrimSpace(e.Value)
		}
	}
	return ""
}

// runningDigest returns the digest all ready pods report for container, or "".
func runningDigest(ctx context.Context, cs kubernetes.Interface, namespace string, selector *metav1.LabelSelector, container string) string {
	if selector == nil {
		return ""
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || sel == labels.Nothing() {
		return ""
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return ""
	}
	digest := ""
	for _, pod := range pods.Items {
		for _, st := range pod.Status.ContainerStatuses {
			if st.Name != container || !st.Ready {
				continue
			}
			d := platformrelease.DigestFromImageID(st.ImageID)
			if d == "" {
				return ""
			}
			if digest != "" && digest != d {
				return ""
			}
			digest = d
		}
	}
	return digest
}
