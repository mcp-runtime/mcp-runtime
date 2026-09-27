package update

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/k8sclient"
)

// Workload outcome states.
const (
	StatusUpdated        = "updated"
	StatusSkipped        = "skipped"
	StatusFailed         = "failed"
	StatusRolledBack     = "rolled-back"
	StatusRollbackFailed = "rollback-failed"
	StatusNotAttempted   = "not-attempted"
)

// RolloutWaiter waits for a Deployment rollout to complete.
type RolloutWaiter func(ctx context.Context, namespace, name string, timeout time.Duration) error

// DefaultWaiter waits using k8sclient rollout semantics.
func DefaultWaiter(cs kubernetes.Interface) RolloutWaiter {
	clients := &k8sclient.Clients{Clientset: cs}
	return func(ctx context.Context, namespace, name string, timeout time.Duration) error {
		return k8sclient.WaitForDeploymentRolledOut(ctx, clients, namespace, name, timeout)
	}
}

// WorkloadResult reports what happened to one Deployment.
type WorkloadResult struct {
	Namespace  string   `json:"namespace"`
	Deployment string   `json:"deployment"`
	Components []string `json:"components"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
	// Recovery lists manual commands to restore the previous images.
	Recovery []string `json:"recovery,omitempty"`

	changes []imageChange
}

// CRDResult reports one CustomResourceDefinition apply outcome.
type CRDResult struct {
	Name   string `json:"name"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Result is the outcome of applying a plan.
type Result struct {
	CRDs      []CRDResult      `json:"crds,omitempty"`
	Workloads []WorkloadResult `json:"workloads"`
	Failed    bool             `json:"failed"`
}

type imageChange struct {
	component platformrelease.Component
	previous  string
	target    string
}

// ApplyOptions controls rollout behaviour.
type ApplyOptions struct {
	Timeout           time.Duration
	RollbackOnFailure bool
	Waiter            RolloutWaiter
	// Clients applies CRD YAML when the plan sets ApplyCRDs. Required only then.
	Clients *k8sclient.Clients
	// Progress receives human-readable progress lines (may be nil).
	Progress func(string)
}

// groupByWorkload groups changed rows per Deployment, preserving catalog order.
func groupByWorkload(rows []Row) []*WorkloadResult {
	var out []*WorkloadResult
	index := map[string]*WorkloadResult{}
	for _, r := range rows {
		key := r.Namespace + "/" + r.Deployment
		w, ok := index[key]
		if !ok {
			w = &WorkloadResult{Namespace: r.Namespace, Deployment: r.Deployment, Status: StatusNotAttempted}
			index[key] = w
			out = append(out, w)
		}
		w.Components = append(w.Components, r.Component)
		w.changes = append(w.changes, imageChange{component: r.component, previous: r.CurrentImage, target: r.TargetImage})
	}
	for _, w := range out {
		w.Recovery = recoveryCommands(w)
	}
	return out
}

// Apply applies release CRDs (when planned) then patches only the changed
// workloads, one at a time, waiting for each rollout. On image-rollout
// failure it stops and, when enabled, restores previous images of every
// workload touched in this run in reverse order. CRD applies are not rolled
// back (Kubernetes CRD downgrades are unsafe).
func Apply(ctx context.Context, cs kubernetes.Interface, plan *Plan, opts ApplyOptions) *Result {
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	waiter := opts.Waiter
	if waiter == nil {
		waiter = DefaultWaiter(cs)
	}
	workloads := groupByWorkload(plan.Changed())
	res := &Result{}

	if plan.ApplyCRDs {
		crdTimeout := opts.Timeout
		if crdTimeout < 2*time.Minute {
			crdTimeout = 2 * time.Minute
		}
		crdResults, err := applyReleaseCRDs(ctx, opts.Clients, plan, crdTimeout, progress)
		res.CRDs = crdResults
		if err != nil {
			res.Failed = true
			return res
		}
	}

	var touched []*WorkloadResult

	for _, w := range workloads {
		current, err := workloadAlreadyCurrent(ctx, cs, w)
		if err != nil {
			w.Status, w.Error = StatusFailed, fmt.Sprintf("read deployment: %v", err)
			res.Failed = true
			break
		}
		if current {
			w.Status = StatusSkipped
			progress(fmt.Sprintf("Skipping %s/%s (already on target images)", w.Namespace, w.Deployment))
			continue
		}
		progress(fmt.Sprintf("Updating %s/%s (%v)", w.Namespace, w.Deployment, w.Components))
		touched = append(touched, w)
		if err := patchWorkload(ctx, cs, w, plan.TargetVersion, false); err != nil {
			w.Status, w.Error = StatusFailed, fmt.Sprintf("patch: %v", err)
			res.Failed = true
			break
		}
		if err := waiter(ctx, w.Namespace, w.Deployment, opts.Timeout); err != nil {
			w.Status, w.Error = StatusFailed, fmt.Sprintf("rollout did not complete within %s: %v", opts.Timeout, err)
			res.Failed = true
			break
		}
		w.Status = StatusUpdated
		progress(fmt.Sprintf("Rolled out %s/%s", w.Namespace, w.Deployment))
	}

	if res.Failed && opts.RollbackOnFailure {
		for i := len(touched) - 1; i >= 0; i-- {
			w := touched[i]
			progress(fmt.Sprintf("Rolling back %s/%s to previous images", w.Namespace, w.Deployment))
			if err := patchWorkload(ctx, cs, w, "", true); err != nil {
				w.Status = StatusRollbackFailed
				w.Error = joinErr(w.Error, fmt.Sprintf("rollback patch: %v", err))
				continue
			}
			if err := waiter(ctx, w.Namespace, w.Deployment, opts.Timeout); err != nil {
				w.Status = StatusRollbackFailed
				w.Error = joinErr(w.Error, fmt.Sprintf("rollback rollout: %v", err))
				continue
			}
			w.Status = StatusRolledBack
		}
	}
	for _, w := range workloads {
		res.Workloads = append(res.Workloads, *w)
	}
	return res
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func stringsJoin(parts []string) string {
	if len(parts) == 0 {
		return "-"
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += ", " + parts[i]
	}
	return out
}

// buildPatch renders the strategic-merge patch for a workload. It sets only
// container images (or the gateway proxy env var) plus Deployment metadata.
func buildPatch(w *WorkloadResult, version string, rollback bool) ([]byte, error) {
	annotations := map[string]string{}
	labels := map[string]string{}
	containers := map[string]map[string]any{}
	var order []string
	for _, ch := range w.changes {
		c := ch.component
		image := ch.target
		if rollback {
			image = ch.previous
		} else {
			annotations[platformrelease.PreviousImageAnnotation(c.Name)] = ch.previous
		}
		entry, ok := containers[c.Container]
		if !ok {
			entry = map[string]any{"name": c.Container}
			containers[c.Container] = entry
			order = append(order, c.Container)
		}
		if c.EnvVar != "" {
			env, _ := entry["env"].([]map[string]string)
			entry["env"] = append(env, map[string]string{"name": c.EnvVar, "value": image})
		} else {
			entry["image"] = image
			meta := platformrelease.MetadataPatch(c, version)
			for k, v := range meta["labels"].(map[string]string) {
				labels[k] = v
			}
			if !rollback {
				if a, ok := meta["annotations"].(map[string]string); ok {
					for k, v := range a {
						annotations[k] = v
					}
				}
			}
		}
	}
	list := make([]map[string]any, 0, len(order))
	for _, name := range order {
		list = append(list, containers[name])
	}
	metadata := map[string]any{}
	if len(annotations) > 0 {
		metadata["annotations"] = annotations
	}
	if len(labels) > 0 {
		metadata["labels"] = labels
	}
	patch := map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": list}}},
	}
	if len(metadata) > 0 {
		patch["metadata"] = metadata
	}
	return json.Marshal(patch)
}

func patchWorkload(ctx context.Context, cs kubernetes.Interface, w *WorkloadResult, version string, rollback bool) error {
	patch, err := buildPatch(w, version, rollback)
	if err != nil {
		return err
	}
	_, err = cs.AppsV1().Deployments(w.Namespace).Patch(ctx, w.Deployment, types.StrategicMergePatchType, patch, metav1.PatchOptions{FieldManager: "mcp-runtime-update"})
	return err
}

// workloadAlreadyCurrent reports whether every planned image/env change is
// already present on the live Deployment (no-op patch).
func workloadAlreadyCurrent(ctx context.Context, cs kubernetes.Interface, w *WorkloadResult) (bool, error) {
	deploy, err := cs.AppsV1().Deployments(w.Namespace).Get(ctx, w.Deployment, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	for _, ch := range w.changes {
		c := ch.component
		container := findContainer(deploy.Spec.Template.Spec.Containers, c.Container)
		if container == nil {
			return false, nil
		}
		if c.EnvVar != "" {
			if envValue(container.Env, c.EnvVar) != ch.target {
				return false, nil
			}
			continue
		}
		if container.Image != ch.target {
			return false, nil
		}
	}
	return true, nil
}

func recoveryCommands(w *WorkloadResult) []string {
	cmds := []string{fmt.Sprintf("kubectl -n %s rollout undo deployment/%s", w.Namespace, w.Deployment)}
	for _, ch := range w.changes {
		c := ch.component
		if c.EnvVar != "" {
			cmds = append(cmds, fmt.Sprintf("kubectl -n %s set env deployment/%s -c %s %s=%s", w.Namespace, w.Deployment, c.Container, c.EnvVar, ch.previous))
			continue
		}
		cmds = append(cmds, fmt.Sprintf("kubectl -n %s set image deployment/%s %s=%s", w.Namespace, w.Deployment, c.Container, ch.previous))
	}
	return cmds
}
