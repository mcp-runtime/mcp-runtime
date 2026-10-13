package update

import (
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"mcp-runtime/internal/platformrelease"
)

// wiringChange is the planned non-image Deployment change for a component,
// plus the values to restore on rollback.
type wiringChange struct {
	serviceAccount string // target; "" means unchanged
	prevSA         string

	automount     *bool // target; nil means unchanged
	setAutomount  bool
	prevAutomount *bool

	env     map[string]string  // target literal values
	prevEnv map[string]*string // previous literal value; nil entry = was absent
	// prevEnvFrom lists vars that were Secret/ConfigMap references; rollback
	// leaves them as the literal rather than guessing the reference.
	prevEnvFrom map[string]bool
}

// planWiring compares a release's wiring with the live Deployment and returns
// the drift to correct (nil when the Deployment already matches).
func planWiring(deploy *appsv1.Deployment, container *corev1.Container, want *platformrelease.ComponentWiring) (*wiringChange, []string) {
	if want.IsZero() {
		return nil, nil
	}
	change := &wiringChange{prevEnv: map[string]*string{}, prevEnvFrom: map[string]bool{}, env: map[string]string{}}
	var drift []string
	spec := deploy.Spec.Template.Spec

	if want.ServiceAccountName != "" && spec.ServiceAccountName != want.ServiceAccountName {
		change.serviceAccount = want.ServiceAccountName
		change.prevSA = spec.ServiceAccountName
		drift = append(drift, fmt.Sprintf("serviceAccountName %q -> %q", spec.ServiceAccountName, want.ServiceAccountName))
	}
	if want.AutomountServiceAccountToken != nil {
		live := spec.AutomountServiceAccountToken
		if live == nil || *live != *want.AutomountServiceAccountToken {
			change.setAutomount = true
			change.automount = want.AutomountServiceAccountToken
			change.prevAutomount = live
			drift = append(drift, fmt.Sprintf("automountServiceAccountToken -> %t", *want.AutomountServiceAccountToken))
		}
	}
	names := make([]string, 0, len(want.Env))
	for name := range want.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := want.Env[name]
		var existing *corev1.EnvVar
		for i := range container.Env {
			if container.Env[i].Name == name {
				existing = &container.Env[i]
			}
		}
		switch {
		case existing == nil:
			change.env[name] = target
			change.prevEnv[name] = nil
			drift = append(drift, fmt.Sprintf("env %s added", name))
		case existing.ValueFrom != nil || existing.Value != target:
			change.env[name] = target
			prev := existing.Value
			change.prevEnv[name] = &prev
			if existing.ValueFrom != nil {
				change.prevEnvFrom[name] = true
			}
			drift = append(drift, fmt.Sprintf("env %s corrected", name))
		}
	}
	if len(drift) == 0 {
		return nil, nil
	}
	return change, drift
}

// wiringSatisfied reports whether the live Deployment already matches change.
func wiringSatisfied(deploy *appsv1.Deployment, container *corev1.Container, change *wiringChange) bool {
	if change == nil {
		return true
	}
	spec := deploy.Spec.Template.Spec
	if change.serviceAccount != "" && spec.ServiceAccountName != change.serviceAccount {
		return false
	}
	if change.setAutomount && (spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken != *change.automount) {
		return false
	}
	for name, target := range change.env {
		if envValue(container.Env, name) != target {
			return false
		}
		for _, e := range container.Env {
			if e.Name == name && e.ValueFrom != nil {
				return false
			}
		}
	}
	return true
}

// podSpecPatch adds the wiring (or, when rollback is true, its previous
// values) to the pod-spec portion of a strategic-merge patch and returns the
// container env entries to merge into container.
func (c *wiringChange) podSpecPatch(podSpec map[string]any, rollback bool) []map[string]any {
	if c == nil {
		return nil
	}
	if c.serviceAccount != "" {
		if rollback {
			podSpec["serviceAccountName"] = c.prevSA
		} else {
			podSpec["serviceAccountName"] = c.serviceAccount
		}
	}
	if c.setAutomount {
		switch {
		case rollback && c.prevAutomount == nil:
			podSpec["automountServiceAccountToken"] = nil // null removes the field
		case rollback:
			podSpec["automountServiceAccountToken"] = *c.prevAutomount
		default:
			podSpec["automountServiceAccountToken"] = *c.automount
		}
	}
	names := make([]string, 0, len(c.env))
	for name := range c.env {
		names = append(names, name)
	}
	sort.Strings(names)
	var entries []map[string]any
	for _, name := range names {
		switch {
		case !rollback:
			entries = append(entries, map[string]any{"name": name, "value": c.env[name], "valueFrom": nil})
		case c.prevEnvFrom[name]:
			// Previous value was a reference; keep the literal rather than guess it.
		case c.prevEnv[name] == nil:
			entries = append(entries, map[string]any{"name": name, "$patch": "delete"})
		default:
			entries = append(entries, map[string]any{"name": name, "value": *c.prevEnv[name], "valueFrom": nil})
		}
	}
	return entries
}

func hasWiring(plan *Plan) bool {
	for _, r := range plan.Rows {
		if r.wiring != nil {
			return true
		}
	}
	return false
}
