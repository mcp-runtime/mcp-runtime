package update

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/platforminventory"
)

func scopedGrafanaPolicy(obj platformrelease.PlatformObject) bool {
	return obj.Kind == "NetworkPolicy" && (obj.Name == "mcp-scoped-observability-ingress" || obj.Name == "mcp-runtime-api-grafana-egress")
}

// Image/config updates must remain usable on existing admin-only clusters,
// including ordinary Kind. Only a completed supported setup opts in to the
// scoped Grafana policies; update never enables the auth proxy or feature flag.
func gateScopedGrafanaPolicies(ctx context.Context, cs kubernetes.Interface, plan *Plan) error {
	needed := false
	namespace := core.ComponentNamespace("ui")
	for _, obj := range plan.allObjects {
		if scopedGrafanaPolicy(obj) {
			needed = true
			if obj.Name == "mcp-runtime-api-grafana-egress" {
				namespace = obj.Namespace
			}
		}
	}
	if !needed {
		return nil
	}
	config, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, platforminventory.SharedConfigName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("verify scoped Grafana setup state before update: %w", err)
	}
	if err == nil && strings.EqualFold(config.Data["GRAFANA_SCOPED_USER_ACCESS"], "true") {
		return requireScopedGrafanaMigration(ctx, cs, plan.allObjects)
	}
	kept := make([]platformrelease.PlatformObject, 0, len(plan.allObjects))
	for _, obj := range plan.allObjects {
		if !scopedGrafanaPolicy(obj) {
			kept = append(kept, obj)
		}
	}
	plan.allObjects = kept
	for i := range plan.Rows {
		if plan.Rows[i].Component == "runtime-api" || plan.Rows[i].Component == "grafana" {
			plan.Rows[i].Notes = append(plan.Rows[i].Notes, "Scoped Grafana remains admin-only; its two isolation policies are skipped until setup verifies supported Cilium enforcement and migrates the session route (docs/platform-services.md#scoped-grafana-migration).")
		}
	}
	return nil
}

// Applying the new ingress policy to the old direct Traefik -> Grafana route
// would cut off administrators. setup owns this small ingress migration;
// update only maintains its policies after the supported migration succeeded.
func requireScopedGrafanaMigration(ctx context.Context, cs kubernetes.Interface, objects []platformrelease.PlatformObject) error {
	needed := false
	platformNamespace := core.ComponentNamespace("ui")
	observabilityNamespace := core.ComponentNamespace("grafana")
	for _, obj := range objects {
		if obj.Kind == "NetworkPolicy" {
			switch obj.Name {
			case "mcp-scoped-observability-ingress":
				needed, observabilityNamespace = true, obj.Namespace
			case "mcp-runtime-api-grafana-egress":
				needed, platformNamespace = true, obj.Namespace
			}
		}
	}
	if !needed {
		return nil
	}
	blocked := func(detail string) error {
		return fmt.Errorf("scoped Grafana setup migration required (%s); no update changes were made: next, run `mcp-runtime setup` from this release with the installation's existing TLS, host, storage and mcp-auth options, then re-run `mcp-runtime update`; see docs/platform-services.md#scoped-grafana-migration", detail)
	}
	config, err := cs.CoreV1().ConfigMaps(platformNamespace).Get(ctx, platforminventory.SharedConfigName, metav1.GetOptions{})
	if err != nil || !strings.EqualFold(config.Data["GRAFANA_SCOPED_USER_ACCESS"], "true") {
		return blocked("hardened setup has not completed")
	}
	grafana, err := cs.AppsV1().Deployments(observabilityNamespace).Get(ctx, "grafana", metav1.GetOptions{})
	if err != nil {
		return blocked("Grafana deployment cannot be verified")
	}
	authProxy := false
	for _, container := range grafana.Spec.Template.Spec.Containers {
		if container.Name == "grafana" {
			for _, env := range container.Env {
				if env.Name == "GF_AUTH_PROXY_ENABLED" && env.Value == "true" {
					authProxy = true
				}
			}
		}
	}
	if !authProxy {
		return blocked("Grafana auth proxy is not configured")
	}
	ingresses, err := cs.NetworkingV1().Ingresses(platformNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return blocked("platform Grafana ingress cannot be verified")
	}
	prefix := strings.TrimSuffix(config.Data["UI_GRAFANA_PATH"], "/")
	if prefix == "" {
		prefix = "/grafana"
	}
	proxied := false
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, route := range rule.HTTP.Paths {
				if route.Path == prefix && route.Backend.Service != nil && route.Backend.Service.Name == "mcp-ui" && route.Backend.Service.Port.Number == 8082 {
					proxied = true
				}
			}
		}
	}
	if !proxied {
		return blocked("Grafana ingress does not use the UI session proxy")
	}
	legacy, err := cs.NetworkingV1().Ingresses(observabilityNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return blocked("legacy Grafana routes cannot be verified")
	}
	for _, ingress := range legacy.Items {
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP != nil {
				for _, route := range rule.HTTP.Paths {
					if route.Backend.Service != nil && route.Backend.Service.Name == "grafana" {
						return blocked("a legacy direct Grafana route remains")
					}
				}
			}
		}
	}
	return nil
}
