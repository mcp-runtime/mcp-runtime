package platform

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/assetpath"
)

func scopedTelemetryPolicySource() (*networkingv1.NetworkPolicy, error) {
	filename, err := assetpath.ResolveRepoAssetPath("k8s/23-scoped-observability-networkpolicy.yaml")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal([]byte(strings.Split(string(data), "\n---\n")[0]), &policy); err != nil {
		return nil, err
	}
	if policy.Kind != "NetworkPolicy" || policy.Name != "mcp-scoped-observability-ingress" {
		return nil, fmt.Errorf("scoped telemetry ingress policy source is invalid")
	}
	return &policy, nil
}

func verifyScopedTelemetryPolicy() error {
	want, err := scopedTelemetryPolicySource()
	if err != nil {
		return err
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, err := clients.Clientset.NetworkingV1().NetworkPolicies(core.ComponentNamespace("grafana")).Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("scoped telemetry policy must be applied before Grafana auth proxy: %w", err)
	}
	if !reflect.DeepEqual(live.Spec, want.Spec) {
		return fmt.Errorf("live scoped telemetry ingress policy differs from this release; Grafana auth proxy remains disabled")
	}
	return nil
}

// Do not infer enforcement from the existence of NetworkPolicy resources or
// Kubernetes distribution. In particular, kindnet accepts policies but does not
// enforce them. Only this explicit supported Cilium configuration is enabled;
// unknown providers stay on the existing admin-only path without an auth proxy.
func scopedGrafanaPolicyCapability() (bool, string) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return false, "cannot verify NetworkPolicy enforcement"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if supported, reason := ciliumPolicyCapability(ctx, clients.Clientset); !supported {
		return false, reason
	}
	if err := rejectAdditionalTelemetryPolicies(ctx, clients.Clientset); err != nil {
		return false, err.Error()
	}
	// Native Cilium rules are additive to Kubernetes rules too. This initial
	// supported mode refuses existing custom rules instead of guessing how
	// arbitrary Cilium selectors, entities, or L7 extensions affect the boundary.
	if clients.Dynamic == nil {
		return false, "cannot inspect additive Cilium policies"
	}
	for _, resource := range []string{"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies"} {
		gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: resource}
		list, err := clients.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, "cannot verify additive Cilium policies"
		}
		if err == nil && len(list.Items) > 0 {
			return false, "existing native Cilium policies require a supported isolation review before scoped Grafana can be enabled"
		}
	}
	return true, "ready Cilium explicitly enforces Kubernetes NetworkPolicy"
}

func rejectAdditionalTelemetryPolicies(ctx context.Context, cs kubernetes.Interface) error {
	policies, err := cs.NetworkingV1().NetworkPolicies(core.ComponentNamespace("grafana")).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify additive telemetry ingress policies")
	}
	for _, policy := range policies.Items {
		if policy.Name == "mcp-scoped-observability-ingress" || len(policy.Spec.Ingress) == 0 {
			continue
		}
		// Dropping non-app requirements is conservative: they might be added
		// to a pod by admission. An explicit app exclusion is enough to prove
		// this policy cannot select any managed telemetry backend.
		selector := metav1.LabelSelector{MatchLabels: map[string]string{}}
		if app := policy.Spec.PodSelector.MatchLabels["app"]; app != "" {
			selector.MatchLabels["app"] = app
		}
		for _, expr := range policy.Spec.PodSelector.MatchExpressions {
			if expr.Key == "app" {
				selector.MatchExpressions = append(selector.MatchExpressions, expr)
			}
		}
		parsed, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil {
			return fmt.Errorf("cannot verify ingress selector for %s", policy.Name)
		}
		for _, app := range []string{"grafana", "prometheus", "loki", "tempo"} {
			if parsed.Matches(labels.Set{"app": app}) {
				return fmt.Errorf("additional ingress policy %s may widen telemetry access", policy.Name)
			}
		}
	}
	return nil
}

func ciliumPolicyCapability(ctx context.Context, cs kubernetes.Interface) (bool, string) {
	if cs == nil {
		return false, "cannot verify NetworkPolicy enforcement"
	}
	kindnet, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "kindnet", metav1.GetOptions{})
	if err == nil && kindnet.Status.DesiredNumberScheduled > 0 {
		return false, "Kind kindnet does not enforce NetworkPolicy"
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, "cannot inspect the cluster network provider"
	}
	config, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
	if err != nil {
		return false, "network provider is not the supported verified Cilium configuration"
	}
	mode := strings.TrimSpace(config.Data["enable-policy"])
	if mode != "default" && mode != "always" || config.Data["enable-k8s-networkpolicy"] != "true" {
		return false, "Cilium must explicitly enable Kubernetes NetworkPolicy with enable-policy=default or always"
	}
	agents, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
	if err != nil || agents.Status.DesiredNumberScheduled < 1 || agents.Status.NumberReady != agents.Status.DesiredNumberScheduled ||
		agents.Status.UpdatedNumberScheduled != agents.Status.DesiredNumberScheduled || agents.Status.NumberAvailable != agents.Status.DesiredNumberScheduled || agents.Status.ObservedGeneration < agents.Generation {
		return false, "Cilium agents are not all current, ready and available"
	}
	found := false
	for _, container := range agents.Spec.Template.Spec.Containers {
		if container.Name != "cilium-agent" {
			continue
		}
		found = true
		if len(container.Command) != 1 || container.Command[0] != "cilium-agent" {
			return false, "Cilium agent must run its supported explicit command"
		}
		policyFlag, k8sFlag := false, false
		args := append(append([]string{}, container.Command...), container.Args...)
		for i, arg := range args {
			if strings.HasPrefix(arg, "--enable-policy=") && arg != "--enable-policy=default" && arg != "--enable-policy=always" || strings.HasPrefix(arg, "--enable-k8s-networkpolicy=") && arg != "--enable-k8s-networkpolicy=true" {
				return false, "Cilium agent arguments override policy enforcement"
			}
			if arg == "--enable-policy" && (i+1 >= len(args) || args[i+1] != "default" && args[i+1] != "always") || arg == "--enable-k8s-networkpolicy" && (i+1 >= len(args) || args[i+1] != "true") {
				return false, "Cilium agent arguments override policy enforcement"
			}
			policyFlag = policyFlag || arg == "--enable-policy=default" || arg == "--enable-policy=always" || arg == "--enable-policy" && i+1 < len(args) && (args[i+1] == "default" || args[i+1] == "always")
			k8sFlag = k8sFlag || arg == "--enable-k8s-networkpolicy=true" || arg == "--enable-k8s-networkpolicy" && i+1 < len(args) && args[i+1] == "true"
		}
		// Current/ready agents with these explicit bound flags establish running
		// policy intent independently of stale or node-overridden ConfigMaps.
		if !policyFlag || !k8sFlag {
			return false, "Cilium agents must explicitly set --enable-policy=default|always and --enable-k8s-networkpolicy=true"
		}
		for _, env := range container.Env {
			if env.Name == "CILIUM_ENABLE_POLICY" && env.Value != "default" && env.Value != "always" || env.Name == "CILIUM_ENABLE_K8S_NETWORKPOLICY" && env.Value != "true" {
				return false, "Cilium agent environment overrides policy enforcement"
			}
		}
	}
	if !found {
		return false, "supported Cilium agent container was not found"
	}
	return true, "ready Cilium explicitly enforces Kubernetes NetworkPolicy"
}

func renderGrafanaDeploymentAccess(content string, scoped bool) string {
	if !scoped {
		return content // canonical manifest disables proxy auth and tenant Explore
	}
	for _, name := range []string{"GF_AUTH_PROXY_ENABLED", "GF_AUTH_DISABLE_LOGIN_FORM", "GF_AUTH_DISABLE_SIGNOUT_MENU", "GF_USERS_VIEWERS_CAN_EDIT"} {
		content = strings.ReplaceAll(content, "- name: "+name+"\n              value: \"false\"", "- name: "+name+"\n              value: \"true\"")
	}
	return content
}

func renderGrafanaIngressAccess(content string, scoped bool) (string, error) {
	docs := strings.Split(content, "\n---\n")
	for i, doc := range docs {
		var header struct {
			Kind     string
			Metadata struct{ Name string }
		}
		if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
			return "", fmt.Errorf("decode Grafana access manifest: %w", err)
		}
		if header.Kind != "Ingress" || header.Metadata.Name != "mcp-platform-gateway-observability" && header.Metadata.Name != "mcp-platform-observability" {
			continue
		}
		var ingress networkingv1.Ingress
		if err := yaml.Unmarshal([]byte(doc), &ingress); err != nil {
			return "", err
		}
		if scoped {
			ingress.Namespace = core.ComponentNamespace("ui")
			delete(ingress.Annotations, "traefik.ingress.kubernetes.io/router.middlewares")
		} else {
			ingress.Namespace = core.ComponentNamespace("grafana")
			if ingress.Annotations == nil {
				ingress.Annotations = map[string]string{}
			}
			ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = "platform-admin-auth@file"
		}
		for j := range ingress.Spec.Rules {
			if ingress.Spec.Rules[j].HTTP == nil {
				continue
			}
			for k := range ingress.Spec.Rules[j].HTTP.Paths {
				backend := &ingress.Spec.Rules[j].HTTP.Paths[k].Backend
				if scoped {
					backend.Service = &networkingv1.IngressServiceBackend{Name: "mcp-ui", Port: networkingv1.ServiceBackendPort{Number: 8082}}
				} else {
					backend.Service = &networkingv1.IngressServiceBackend{Name: "grafana", Port: networkingv1.ServiceBackendPort{Number: 3000}}
				}
			}
		}
		data, err := yaml.Marshal(ingress)
		if err != nil {
			return "", err
		}
		docs[i] = strings.TrimSuffix(string(data), "\n")
	}
	return strings.Join(docs, "\n---\n"), nil
}
