package doctor

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
)

const platformPullSecretsCheckName = "platform registry pull secrets"

// platformPullSecretNamespaces lists the namespaces that hold platform
// Deployments setup renders, including the operator and the optional mcp-auth
// server.
func platformPullSecretNamespaces() []string {
	seen := map[string]bool{}
	var namespaces []string
	for _, key := range []string{"operator", "platform-api", "mcp-auth", "analytics-api"} {
		namespace := componentNamespace(key)
		if namespace == "" || seen[namespace] {
			continue
		}
		seen[namespace] = true
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}

// checkPlatformPullSecrets reports platform Deployments that run an image from
// the platform's public registry without any pull Secret. Kubelet pulls from
// that registry need credentials, so such a Deployment fails with
// ImagePullBackOff on its next rollout (issue #623).
func checkPlatformPullSecrets(kubectl core.KubectlRunner) DoctorCheck {
	configName := platforminventory.SharedConfigName
	configHost, _ := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "jsonpath={.data.MCP_REGISTRY_INGRESS_HOST}"})
	configDomain, _ := readKubectlOutput(kubectl, []string{"get", "configmap", configName, "-n", componentNamespace("platform-api"), "-o", "jsonpath={.data.MCP_PLATFORM_DOMAIN}"})
	registryHost, _ := k8sclient.ChooseRegistryPublicHost(k8sclient.RegistryHostSources{
		ConfigIngressHost:    strings.TrimSpace(configHost),
		ConfigPlatformDomain: strings.TrimSpace(configDomain),
	})
	if k8sclient.IsPlaceholderRegistryHost(registryHost) {
		return DoctorCheck{Name: platformPullSecretsCheckName, OK: true, Detail: "no public platform registry host configured; skipping"}
	}

	var problems []string
	checked := 0
	for _, namespace := range platformPullSecretNamespaces() {
		deploymentsJSON, err := readKubectlOutput(kubectl, []string{"get", "deployments", "-n", namespace, "-o", "json"})
		if err != nil {
			return DoctorCheck{Name: platformPullSecretsCheckName, OK: false, Detail: fmt.Sprintf("failed listing deployments in %s: %v", namespace, err), Remedy: "check Kubernetes API access to deployments in platform namespaces"}
		}
		var deployments appsv1.DeploymentList
		if err := json.Unmarshal([]byte(deploymentsJSON), &deployments); err != nil {
			return DoctorCheck{Name: platformPullSecretsCheckName, OK: false, Detail: fmt.Sprintf("failed parsing deployments in %s: %v", namespace, err), Remedy: fmt.Sprintf("inspect kubectl get deployments -n %s -o json", namespace)}
		}
		var serviceAccounts *corev1.ServiceAccountList
		for _, deployment := range deployments.Items {
			if !podSpecUsesRegistryHost(deployment.Spec.Template.Spec, registryHost) {
				continue
			}
			checked++
			if len(deployment.Spec.Template.Spec.ImagePullSecrets) > 0 {
				continue
			}
			if serviceAccounts == nil {
				serviceAccounts = &corev1.ServiceAccountList{}
				saJSON, err := readKubectlOutput(kubectl, []string{"get", "serviceaccounts", "-n", namespace, "-o", "json"})
				if err != nil {
					return DoctorCheck{Name: platformPullSecretsCheckName, OK: false, Detail: fmt.Sprintf("failed listing serviceaccounts in %s: %v", namespace, err), Remedy: "check Kubernetes API access to serviceaccounts in platform namespaces"}
				}
				if err := json.Unmarshal([]byte(saJSON), serviceAccounts); err != nil {
					return DoctorCheck{Name: platformPullSecretsCheckName, OK: false, Detail: fmt.Sprintf("failed parsing serviceaccounts in %s: %v", namespace, err), Remedy: fmt.Sprintf("inspect kubectl get serviceaccounts -n %s -o json", namespace)}
				}
			}
			if serviceAccountHasPullSecret(*serviceAccounts, deployment.Spec.Template.Spec.ServiceAccountName) {
				continue
			}
			problems = append(problems, namespace+"/"+deployment.Name)
		}
	}
	if len(problems) == 0 {
		return DoctorCheck{Name: platformPullSecretsCheckName, OK: true, Detail: fmt.Sprintf("%d platform deployment(s) pulling from %s have an image pull secret", checked, registryHost)}
	}
	sort.Strings(problems)
	return DoctorCheck{
		Name:   platformPullSecretsCheckName,
		OK:     false,
		Detail: fmt.Sprintf("deployment(s) %s use images from platform registry %s but have no imagePullSecrets and their service account provides none", strings.Join(problems, ", "), registryHost),
		Remedy: "rerun mcp-runtime setup so it attaches the platform pull secret (mcp-runtime-registry-pull), or set MCP_PLATFORM_IMAGE_PULL_SECRET to an existing pull secret",
	}
}

// podSpecUsesRegistryHost reports whether any container or init container
// image is hosted on registryHost.
func podSpecUsesRegistryHost(spec corev1.PodSpec, registryHost string) bool {
	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	for _, container := range containers {
		if strings.EqualFold(registryHostFromImageRef(container.Image), registryHost) {
			return true
		}
	}
	return false
}

// serviceAccountHasPullSecret reports whether the named service account (the
// namespace default when empty) carries an image pull secret, which the
// admission controller copies into every pod that uses it.
func serviceAccountHasPullSecret(accounts corev1.ServiceAccountList, name string) bool {
	if strings.TrimSpace(name) == "" {
		name = "default"
	}
	for _, account := range accounts.Items {
		if account.Name == name {
			return len(account.ImagePullSecrets) > 0
		}
	}
	return false
}
