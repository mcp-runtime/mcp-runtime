package platformrelease

import (
	"encoding/json"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	"mcp-runtime/pkg/resourcehistory"
)

func validateResourceHistoryTokenSource(dir string, wiring ReleaseWiring) error {
	for name, required := range wiring.Components {
		if required == nil || !required.ResourceHistoryToken {
			continue
		}
		if name != "prometheus" {
			return fmt.Errorf("resourceHistoryToken is only supported for prometheus")
		}
		component, _ := Lookup(name)
		source, err := FindK8sDeployment(dir, component.Namespace, component.Deployment)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(source)
		if err != nil {
			return err
		}
		var deployment appsv1.Deployment
		if err = json.Unmarshal(encoded, &deployment); err != nil {
			return err
		}
		foundVolume, foundMount := false, false
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == resourcehistory.TokenVolumeName {
				foundVolume = reflect.DeepEqual(volume, resourcehistory.PrometheusTokenVolume())
			}
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "prometheus" {
				for _, mount := range container.VolumeMounts {
					if mount.Name == resourcehistory.TokenVolumeName {
						return fmt.Errorf("resource token may only be mounted by prometheus")
					}
				}
				continue
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == resourcehistory.TokenVolumeName || mount.MountPath == resourcehistory.TokenMountPath {
					if !reflect.DeepEqual(mount, resourcehistory.PrometheusTokenMount()) {
						return fmt.Errorf("canonical Prometheus resource token mount differs")
					}
					foundMount = true
				}
			}
		}
		if !foundVolume || !foundMount {
			return fmt.Errorf("canonical Prometheus resource token source/mount differs from release declaration")
		}
	}
	return nil
}
