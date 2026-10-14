package update

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/platforminventory"
	"mcp-runtime/pkg/platformmode"
)

var modeDependents = []string{"runtime-api", "platform-api", "ui"}

func validatePlatformModeOptions(opts Options) error {
	if opts.PlatformMode == "" {
		return nil
	}
	if _, ok := platformmode.Normalize(opts.PlatformMode); !ok || strings.TrimSpace(opts.PlatformMode) == "" {
		return core.NewWithBase(core.ErrUpdateInvalidFlag, "--platform-mode must be org or public; tenant is a publish scope")
	}
	if len(opts.Only) > 0 {
		return core.NewWithBase(core.ErrUpdateInvalidFlag, "--platform-mode cannot be combined with --only (scoped updates skip configuration)")
	}
	return nil
}

// setManifestPlatformMode moves configuration-only rollouts into update: callers
// no longer need to manufacture ConfigMap/restart entries in a release manifest.
func setManifestPlatformMode(ctx context.Context, cs kubernetes.Interface, m *platformrelease.Manifest, raw string) error {
	mode, _ := platformmode.Normalize(raw)
	for _, ns := range []string{platforminventory.PlatformNamespace, platforminventory.ObservabilityNamespace} {
		_, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, "mcp-shared-config", metav1.GetOptions{})
		if apierrors.IsNotFound(err) && ns == platforminventory.ObservabilityNamespace {
			continue // Observability is optional; do not install it as a side effect.
		}
		if err != nil {
			return fmt.Errorf("read mode configuration %s/mcp-shared-config: %w", ns, err)
		}
		index := -1
		for i, cm := range m.ConfigMaps {
			if cm.Namespace == ns && cm.Name == "mcp-shared-config" {
				index = i
				break
			}
		}
		if index < 0 {
			m.ConfigMaps = append(m.ConfigMaps, platformrelease.ManifestConfigMap{Namespace: ns, Name: "mcp-shared-config"})
			index = len(m.ConfigMaps) - 1
		}
		cm := &m.ConfigMaps[index]
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["PLATFORM_MODE"] = string(mode)
		if ns == platforminventory.PlatformNamespace {
			for _, dependent := range modeDependents {
				if !stringContains(cm.Restart, dependent) {
					cm.Restart = append(cm.Restart, dependent)
				}
				if _, ok := m.Component(dependent); !ok {
					m.Components = append(m.Components, platformrelease.ManifestComponent{Name: dependent, ConfigOnly: true})
				}
			}
		}
	}
	return m.Validate()
}

// catalogTargets resolves only the target cluster's configuration. The
// workstation's PLATFORM_* environment must never affect production namespaces.
func catalogTargets(data map[string]string) (map[string]string, error) {
	mode, valid := platformmode.Normalize(data["PLATFORM_MODE"])
	if !valid || strings.TrimSpace(data["PLATFORM_MODE"]) == "" {
		return nil, fmt.Errorf("PLATFORM_MODE must explicitly be org or public")
	}
	value := func(fallback string, keys ...string) string {
		for _, key := range keys {
			if v := strings.TrimSpace(data[key]); v != "" {
				return v
			}
		}
		return fallback
	}
	org := value(platformmode.DefaultOrgCatalogNamespace, "PLATFORM_ORG_NAMESPACE")
	primary := org
	if mode == platformmode.ModePublic {
		primary = value(platformmode.DefaultPublicCatalogNamespace, "PLATFORM_PUBLIC_NAMESPACE")
	}
	primary = value(primary, "PLATFORM_CATALOG_NAMESPACE", "MCP_PLATFORM_CATALOG_NAMESPACE")
	extra := value("", "PLATFORM_CATALOG_NAMESPACES", "MCP_PLATFORM_CATALOG_NAMESPACES")
	if extra == "" && mode == platformmode.ModePublic {
		extra = value("", "PLATFORM_PUBLIC_NAMESPACES", "MCP_PLATFORM_PUBLIC_NAMESPACES")
	}
	targets := map[string]string{}
	if mode == platformmode.ModePublic {
		targets[org] = "org"
	}
	for _, name := range append([]string{primary}, strings.Split(extra, ",")...) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if mode == platformmode.ModePublic && (name == org || name == platformmode.DefaultOrgCatalogNamespace) {
			return nil, fmt.Errorf("organization catalog %q cannot be a public catalog", name)
		}
		targets[name] = string(mode)
	}
	for name := range targets {
		if errors := validation.IsDNS1123Label(name); len(errors) > 0 {
			return nil, fmt.Errorf("invalid catalog namespace %q: %s", name, strings.Join(errors, ", "))
		}
		if name == "default" || strings.HasPrefix(name, "kube-") {
			return nil, fmt.Errorf("catalog namespace %q is reserved", name)
		}
		for _, namespace := range platforminventory.OwnerNamespaces() {
			if name == namespace {
				return nil, fmt.Errorf("catalog namespace %q is a platform namespace", name)
			}
		}
	}
	return targets, nil
}

func planCatalogNamespaces(ctx context.Context, cs kubernetes.Interface, plan *Plan) error {
	for _, cm := range plan.configMaps {
		if cm.Namespace != platforminventory.PlatformNamespace || cm.Name != "mcp-shared-config" {
			continue
		}
		if _, managesMode := cm.Data["PLATFORM_MODE"]; !managesMode {
			continue
		}
		data := map[string]string{}
		live, err := cs.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if live != nil {
			data = copyData(live.Data)
		}
		for k, v := range cm.Data {
			data[k] = v
		}
		targets, err := catalogTargets(data)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(targets))
		for name := range targets {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			want := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: catalogLabels(targets[name])}}
			action, err := catalogNamespaceAction(ctx, cs, want)
			if err != nil {
				return err
			}
			plan.CatalogNamespaces = append(plan.CatalogNamespaces, ObjectResult{Key: name, Action: action})
			if action != ObjectActionSkipped {
				plan.namespacesToWrite = append(plan.namespacesToWrite, want)
			}
		}
	}
	return nil
}

func catalogLabels(scope string) map[string]string {
	return map[string]string{
		"platform.mcpruntime.org/managed": "true", "mcpruntime.org/scope": scope,
		"app.kubernetes.io/managed-by":       "mcp-runtime",
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/audit":   "restricted", "pod-security.kubernetes.io/warn": "restricted",
	}
}

func stringContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func catalogNamespaceAction(ctx context.Context, cs kubernetes.Interface, want *corev1.Namespace) (string, error) {
	live, err := cs.CoreV1().Namespaces().Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ObjectActionCreate, nil
	}
	if err != nil {
		return "", fmt.Errorf("read catalog namespace %s: %w", want.Name, err)
	}
	if err := validateExistingCatalog(live, want); err != nil {
		return "", err
	}
	for k, v := range want.Labels {
		if live.Labels[k] != v {
			return ObjectActionUpdate, nil
		}
	}
	return ObjectActionSkipped, nil
}

func validateExistingCatalog(live, want *corev1.Namespace) error {
	if live.DeletionTimestamp != nil || live.Labels["mcpruntime.org/scope"] != want.Labels["mcpruntime.org/scope"] || live.Labels["platform.mcpruntime.org/managed"] != "true" {
		return fmt.Errorf("refusing to adopt or change scope of existing namespace %q; expected an active managed %s catalog", want.Name, want.Labels["mcpruntime.org/scope"])
	}
	return nil
}

func applyCatalogNamespaces(ctx context.Context, cs kubernetes.Interface, plan *Plan, progress func(string)) ([]ObjectResult, error) {
	var results []ObjectResult
	for _, want := range plan.namespacesToWrite {
		// Compare again to refuse a namespace created with another scope since planning.
		action, err := catalogNamespaceAction(ctx, cs, want)
		if err == nil && action == ObjectActionCreate {
			_, err = cs.CoreV1().Namespaces().Create(ctx, want, metav1.CreateOptions{FieldManager: objectFieldManager})
		} else if err == nil && action == ObjectActionUpdate {
			var live *corev1.Namespace
			live, err = cs.CoreV1().Namespaces().Get(ctx, want.Name, metav1.GetOptions{})
			if err == nil {
				err = validateExistingCatalog(live, want)
			}
			if err == nil {
				for key, value := range want.Labels {
					live.Labels[key] = value
				}
				_, err = cs.CoreV1().Namespaces().Update(ctx, live, metav1.UpdateOptions{FieldManager: objectFieldManager})
			}
		}
		result := ObjectResult{Key: want.Name, Action: action}
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			return results, fmt.Errorf("prepare catalog namespace %s: %w", want.Name, err)
		}
		progress(fmt.Sprintf("Catalog namespace %s: %s", want.Name, action))
		results = append(results, result)
	}
	return results, nil
}
