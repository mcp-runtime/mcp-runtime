package update

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/platformrelease"
)

// refineConfigMapPlan compares each declared ConfigMap's keys with the live
// object and keeps only those that differ, so dry-run and apply stay targeted.
func refineConfigMapPlan(ctx context.Context, cs kubernetes.Interface, plan *Plan) error {
	plan.ConfigMaps = nil
	plan.configToWrite = nil
	for _, want := range plan.configMaps {
		live, err := cs.CoreV1().ConfigMaps(want.Namespace).Get(ctx, want.Name, metav1.GetOptions{})
		action := ObjectActionSkipped
		switch {
		case apierrors.IsNotFound(err):
			action = ObjectActionCreate
		case err != nil:
			return fmt.Errorf("read ConfigMap %s: %w", want.Key(), err)
		default:
			for key, value := range want.Data {
				if current, ok := live.Data[key]; !ok || current != value {
					action = ObjectActionUpdate
					break
				}
			}
		}
		plan.ConfigMaps = append(plan.ConfigMaps, ObjectResult{Key: want.Key(), Action: action})
		if action != ObjectActionSkipped {
			plan.configToWrite = append(plan.configToWrite, want)
		}
	}
	return nil
}

// configSnapshot records the previous value of every key a ConfigMap write
// changed so a failed rollout can restore it; a nil value means the key (or
// the whole ConfigMap) did not exist before.
type configSnapshot struct {
	cm       platformrelease.ManifestConfigMap
	created  bool
	previous map[string]*string
}

// applyConfigMaps writes the declared keys, leaving other keys untouched.
func applyConfigMaps(ctx context.Context, cs kubernetes.Interface, plan *Plan, progress func(string)) ([]ObjectResult, []configSnapshot, error) {
	var results []ObjectResult
	var snapshots []configSnapshot
	for _, want := range plan.configToWrite {
		api := cs.CoreV1().ConfigMaps(want.Namespace)
		live, err := api.Get(ctx, want.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			progress(fmt.Sprintf("Creating ConfigMap %s", want.Key()))
			created := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: want.Name, Namespace: want.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}},
				Data:       copyData(want.Data),
			}
			if _, err := api.Create(ctx, created, metav1.CreateOptions{FieldManager: objectFieldManager}); err != nil {
				results = append(results, ObjectResult{Key: want.Key(), Error: err.Error()})
				return results, snapshots, fmt.Errorf("create ConfigMap %s: %w", want.Key(), err)
			}
			snapshots = append(snapshots, configSnapshot{cm: want, created: true})
			results = append(results, ObjectResult{Key: want.Key(), Action: ObjectActionCreate})
			continue
		case err != nil:
			results = append(results, ObjectResult{Key: want.Key(), Error: err.Error()})
			return results, snapshots, fmt.Errorf("read ConfigMap %s: %w", want.Key(), err)
		}
		snap := configSnapshot{cm: want, previous: map[string]*string{}}
		if live.Data == nil {
			live.Data = map[string]string{}
		}
		changed := false
		keys := make([]string, 0, len(want.Data))
		for k := range want.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := want.Data[key]
			current, had := live.Data[key]
			if had && current == value {
				continue
			}
			if had {
				prev := current
				snap.previous[key] = &prev
			} else {
				snap.previous[key] = nil
			}
			live.Data[key] = value
			changed = true
		}
		if !changed {
			results = append(results, ObjectResult{Key: want.Key(), Action: ObjectActionSkipped})
			continue
		}
		progress(fmt.Sprintf("Updating ConfigMap %s (%d key(s))", want.Key(), len(snap.previous)))
		if _, err := api.Update(ctx, live, metav1.UpdateOptions{FieldManager: objectFieldManager}); err != nil {
			results = append(results, ObjectResult{Key: want.Key(), Error: err.Error()})
			return results, snapshots, fmt.Errorf("update ConfigMap %s: %w", want.Key(), err)
		}
		snapshots = append(snapshots, snap)
		results = append(results, ObjectResult{Key: want.Key(), Action: ObjectActionUpdate})
	}
	return results, snapshots, nil
}

// restoreConfigMaps puts back the keys a failed update changed.
func restoreConfigMaps(ctx context.Context, cs kubernetes.Interface, snapshots []configSnapshot, progress func(string)) {
	for i := len(snapshots) - 1; i >= 0; i-- {
		snap := snapshots[i]
		api := cs.CoreV1().ConfigMaps(snap.cm.Namespace)
		if snap.created {
			progress(fmt.Sprintf("Removing ConfigMap %s created by this update", snap.cm.Key()))
			_ = api.Delete(ctx, snap.cm.Name, metav1.DeleteOptions{})
			continue
		}
		live, err := api.Get(ctx, snap.cm.Name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		for key, prev := range snap.previous {
			if prev == nil {
				delete(live.Data, key)
			} else {
				live.Data[key] = *prev
			}
		}
		progress(fmt.Sprintf("Restoring ConfigMap %s", snap.cm.Key()))
		_, _ = api.Update(ctx, live, metav1.UpdateOptions{FieldManager: objectFieldManager})
	}
}

func copyData(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
