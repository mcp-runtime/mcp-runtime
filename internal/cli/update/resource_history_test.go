package update

import (
	"cmp"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/resourcehistory"
)

func applyResourceTokenChange(t *testing.T, original *appsv1.Deployment, change *wiringChange, rollback bool) *appsv1.Deployment {
	t.Helper()
	parts := &patchParts{podSpec: map[string]any{}, annotations: map[string]any{}}
	change.apply(parts, rollback)
	parts.podSpec["containers"] = []map[string]any{{"name": "prometheus", "volumeMounts": change.resourceHistoryToken.mountPatch(rollback)}}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": parts.podSpec}}})
	before, _ := json.Marshal(original)
	after, err := strategicpatch.StrategicMergePatch(before, patch, appsv1.Deployment{})
	if err != nil {
		t.Fatal(err)
	}
	var result appsv1.Deployment
	if err = json.Unmarshal(after, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func TestResourceTokenUpdateCorrectsOnlyFixedMountAndRollsBack(t *testing.T) {
	for _, mode := range []string{"absent", "wrong audience", "wrong expiry", "wrong mount", "wrong source"} {
		t.Run(mode, func(t *testing.T) {
			original := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "mcp-observability"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "keep", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, Containers: []corev1.Container{{Name: "prometheus", VolumeMounts: []corev1.VolumeMount{{Name: "keep", MountPath: "/keep"}}}}}}}}
			if mode != "absent" {
				volume, mount := resourcehistory.PrometheusTokenVolume(), resourcehistory.PrometheusTokenMount()
				switch mode {
				case "wrong audience":
					volume.Projected.Sources[0].ServiceAccountToken.Audience = "foreign"
				case "wrong expiry":
					value := int64(3600)
					volume.Projected.Sources[0].ServiceAccountToken.ExpirationSeconds = &value
				case "wrong mount":
					mount.MountPath = "/wrong"
					mount.ReadOnly = false
				case "wrong source":
					volume.VolumeSource = corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "old-custom"}}
				}
				original.Spec.Template.Spec.Volumes = append(original.Spec.Template.Spec.Volumes, volume)
				original.Spec.Template.Spec.Containers[0].VolumeMounts = append(original.Spec.Template.Spec.Containers[0].VolumeMounts, mount)
			}
			change, drift := planWiring(original, &original.Spec.Template.Spec.Containers[0], &platformrelease.ComponentWiring{ResourceHistoryToken: true}, nil)
			if change == nil || len(drift) == 0 {
				t.Fatal("resource token drift not planned")
			}
			updated := applyResourceTokenChange(t, original, change, false)
			if !resourceHistoryTokenSatisfied(updated.Spec.Template.Spec, &updated.Spec.Template.Spec.Containers[0]) {
				t.Fatalf("fixed token not installed: %+v", updated.Spec.Template.Spec)
			}
			var preservedVolume, preservedMount bool
			for _, volume := range updated.Spec.Template.Spec.Volumes {
				if volume.Name == "keep" {
					preservedVolume = reflect.DeepEqual(volume, original.Spec.Template.Spec.Volumes[0])
				}
			}
			for _, mount := range updated.Spec.Template.Spec.Containers[0].VolumeMounts {
				if mount.Name == "keep" {
					preservedMount = reflect.DeepEqual(mount, original.Spec.Template.Spec.Containers[0].VolumeMounts[0])
				}
			}
			if !preservedVolume || !preservedMount {
				t.Fatal("unrelated data changed")
			}
			if next, _ := planWiring(updated, &updated.Spec.Template.Spec.Containers[0], &platformrelease.ComponentWiring{ResourceHistoryToken: true}, nil); next != nil {
				t.Fatal("matching token has drift")
			}
			restored := applyResourceTokenChange(t, updated, change, true)
			normalize := func(deployment *appsv1.Deployment) {
				slices.SortFunc(deployment.Spec.Template.Spec.Volumes, func(a, b corev1.Volume) int { return cmp.Compare(a.Name, b.Name) })
				slices.SortFunc(deployment.Spec.Template.Spec.Containers[0].VolumeMounts, func(a, b corev1.VolumeMount) int { return cmp.Compare(a.MountPath, b.MountPath) })
			}
			normalize(restored)
			normalize(original)
			if !reflect.DeepEqual(restored.Spec, original.Spec) {
				t.Fatalf("rollback differs:\n%+v\n%+v", restored.Spec, original.Spec)
			}
		})
	}
}
