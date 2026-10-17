package platform

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	cb "mcp-runtime/pkg/credentialbroker"
)

const credentialBrokerOriginEnv = "MCP_GATEWAY_CREDENTIAL_BROKER_URL" // #nosec G101 -- environment variable name, not a credential.

func preservedCredentialBrokerOrigin(existing func(string) string) (string, error) {
	if existing == nil {
		return "", nil
	}
	origin := strings.TrimSpace(existing(credentialBrokerOriginEnv))
	if origin == "" {
		return "", nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("configured credential broker origin is invalid; repair it with credential configure before setup")
	}
	return origin, nil
}

// Retain only the explicit, metadata-only credential configure contract. Do
// not retain arbitrary old environment variables, injected containers/images
// or secret volumes when refreshing the shipped platform Deployment.
func preserveCredentialBrokerDeployment(desired, live *appsv1.Deployment) error {
	if desired.Name != "mcp-platform-api" {
		return nil
	}
	if live.Spec.Template.Labels["azure.workload.identity/use"] == "true" {
		if desired.Spec.Template.Labels == nil {
			desired.Spec.Template.Labels = map[string]string{}
		}
		desired.Spec.Template.Labels["azure.workload.identity/use"] = "true"
	}
	if revision := live.Spec.Template.Annotations["mcpruntime.org/credential-workload-identity"]; revision != "" {
		if desired.Spec.Template.Annotations == nil {
			desired.Spec.Template.Annotations = map[string]string{}
		}
		desired.Spec.Template.Annotations["mcpruntime.org/credential-workload-identity"] = revision
	}
	for _, source := range live.Spec.Template.Spec.Containers {
		if source.Name != "platform-api" {
			continue
		}
		settings := map[string]corev1.EnvVar{}
		for _, env := range source.Env {
			switch env.Name {
			case "CREDENTIAL_VAULT_ADDR", "CREDENTIAL_VAULT_ROLE", "CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE":
				settings[env.Name] = env
			}
		}
		if len(settings) == 0 {
			return nil
		}
		fail := func() error {
			return fmt.Errorf("configured Vault workload identity is incomplete or differs from the supported credential configure contract")
		}
		if len(settings) != 3 {
			return fail()
		}
		for _, env := range settings {
			if env.ValueFrom != nil {
				return fail()
			}
		}
		u, err := url.Parse(settings["CREDENTIAL_VAULT_ADDR"].Value)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !cb.NameValid(settings["CREDENTIAL_VAULT_ROLE"].Value) || settings["CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE"].Value != "/var/run/mcp-runtime/vault/token" {
			return fail()
		}
		var approved *corev1.Volume
		for _, volume := range live.Spec.Template.Spec.Volumes {
			if volume.Name != "credential-vault-identity" {
				continue
			}
			projected := volume.Projected
			if projected == nil || len(projected.Sources) != 1 {
				return fail()
			}
			token := projected.Sources[0].ServiceAccountToken
			if token == nil || token.Audience != "mcp-runtime-vault" || token.Path != "token" || token.ExpirationSeconds == nil || *token.ExpirationSeconds != 600 {
				return fail()
			}
			only := corev1.VolumeProjection{ServiceAccountToken: token}
			if !reflect.DeepEqual(projected.Sources[0], only) {
				return fail()
			}
			remaining := volume.VolumeSource
			remaining.Projected = nil
			if !reflect.DeepEqual(remaining, corev1.VolumeSource{}) {
				return fail()
			}
			approved = volume.DeepCopy()
		}
		if approved == nil {
			return fail()
		}
		mount := corev1.VolumeMount{Name: "credential-vault-identity", MountPath: "/var/run/mcp-runtime/vault", ReadOnly: true}
		mounted := false
		for _, existing := range source.VolumeMounts {
			if existing.Name == mount.Name {
				if !reflect.DeepEqual(existing, mount) {
					return fail()
				}
				mounted = true
			}
		}
		if !mounted {
			return fail()
		}
		for i := range desired.Spec.Template.Spec.Containers {
			target := &desired.Spec.Template.Spec.Containers[i]
			if target.Name != "platform-api" {
				continue
			}
			for _, key := range []string{"CREDENTIAL_VAULT_ADDR", "CREDENTIAL_VAULT_ROLE", "CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE"} {
				setNativePreservedEnv(target, settings[key])
			}
			mountFound := false
			for j := range target.VolumeMounts {
				if target.VolumeMounts[j].Name == mount.Name {
					target.VolumeMounts[j] = mount
					mountFound = true
				}
			}
			if !mountFound {
				target.VolumeMounts = append(target.VolumeMounts, mount)
			}
			volumeFound := false
			for j := range desired.Spec.Template.Spec.Volumes {
				if desired.Spec.Template.Spec.Volumes[j].Name == approved.Name {
					desired.Spec.Template.Spec.Volumes[j] = *approved
					volumeFound = true
				}
			}
			if !volumeFound {
				desired.Spec.Template.Spec.Volumes = append(desired.Spec.Template.Spec.Volumes, *approved)
			}
			return nil
		}
		return fail()
	}
	return nil
}
