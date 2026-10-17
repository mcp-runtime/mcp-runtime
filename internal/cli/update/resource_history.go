package update

import (
	"encoding/json"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"mcp-runtime/pkg/resourcehistory"
)

type resourceHistoryTokenChange struct {
	previousVolume *corev1.Volume
	previousMounts []corev1.VolumeMount
}

func resourceHistoryTokenSatisfied(spec corev1.PodSpec, container *corev1.Container) bool {
	volumeMatches, mountMatches := false, false
	for _, volume := range spec.Volumes {
		if volume.Name == resourcehistory.TokenVolumeName {
			volumeMatches = reflect.DeepEqual(volume, resourcehistory.PrometheusTokenVolume())
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name != resourcehistory.TokenVolumeName && mount.MountPath != resourcehistory.TokenMountPath {
			continue
		}
		if !reflect.DeepEqual(mount, resourcehistory.PrometheusTokenMount()) {
			return false
		}
		mountMatches = true
	}
	return volumeMatches && mountMatches
}

func planResourceHistoryToken(spec corev1.PodSpec, container *corev1.Container) *resourceHistoryTokenChange {
	if resourceHistoryTokenSatisfied(spec, container) {
		return nil
	}
	change := &resourceHistoryTokenChange{}
	for _, volume := range spec.Volumes {
		if volume.Name == resourcehistory.TokenVolumeName {
			change.previousVolume = volume.DeepCopy()
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == resourcehistory.TokenVolumeName || mount.MountPath == resourcehistory.TokenMountPath {
			change.previousMounts = append(change.previousMounts, *mount.DeepCopy())
		}
	}
	return change
}

func tokenObject(value any) map[string]any {
	// Kubernetes API structs contain only JSON-compatible fields.
	encoded, _ := json.Marshal(value)
	object := map[string]any{}
	_ = json.Unmarshal(encoded, &object)
	// Clear omitted fields on this owned entry so an old Secret/hostPath or
	// writable subPath does not survive strategic merge beside the new source.
	var fields reflect.Type
	switch value.(type) {
	case corev1.Volume, *corev1.Volume:
		fields = reflect.TypeFor[corev1.VolumeSource]()
	case corev1.VolumeMount:
		fields = reflect.TypeFor[corev1.VolumeMount]()
	}
	if fields != nil {
		for i := 0; i < fields.NumField(); i++ {
			key := strings.Split(fields.Field(i).Tag.Get("json"), ",")[0]
			if key != "" && key != "-" {
				if _, present := object[key]; !present {
					object[key] = nil
				}
			}
		}
	}
	return object
}

func (c *resourceHistoryTokenChange) volumePatch(rollback bool) map[string]any {
	if !rollback {
		return tokenObject(resourcehistory.PrometheusTokenVolume())
	}
	if c.previousVolume == nil {
		return map[string]any{"name": resourcehistory.TokenVolumeName, "$patch": "delete"}
	}
	return tokenObject(c.previousVolume)
}

func (c *resourceHistoryTokenChange) mountPatch(rollback bool) []map[string]any {
	var patch []map[string]any
	if rollback {
		hadCanonical := false
		for _, mount := range c.previousMounts {
			if mount.MountPath == resourcehistory.TokenMountPath {
				hadCanonical = true
			}
			patch = append(patch, tokenObject(mount))
		}
		if !hadCanonical {
			patch = append(patch, map[string]any{"mountPath": resourcehistory.TokenMountPath, "$patch": "delete"})
		}
		return patch
	}
	for _, mount := range c.previousMounts {
		if mount.MountPath != resourcehistory.TokenMountPath {
			patch = append(patch, map[string]any{"mountPath": mount.MountPath, "$patch": "delete"})
		}
	}
	return append(patch, tokenObject(resourcehistory.PrometheusTokenMount()))
}
