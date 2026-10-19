package update

import (
	"context"
	"fmt"
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/ingressmanifest"
	"mcp-runtime/pkg/platformauth"
)

func audienceMigrationRequired(detail string) error {
	return fmt.Errorf("token audience setup migration required (%s); no update changes were made: next, run `mcp-runtime setup` from this release with the installation's existing TLS, host, storage and mcp-auth options (setup first, then update), then re-run `mcp-runtime update`; see docs/troubleshooting.md#update-refuses-token-audience-setup-migration-required", detail)
}

// This is a read-only guard, not an ingress migrator. A release that narrows
// login audiences must never install images/config behind old ingress routes.
func requireAudienceExchangeMigration(ctx context.Context, cs kubernetes.Interface) error {
	platformNS := core.ComponentNamespace("runtime-api")
	analyticsNS := core.ComponentNamespace("analytics-api")
	expected := append(ingressmanifest.PlatformAPIPaths(), ingressmanifest.ObservabilityAPIPaths()...)
	expectedByPath := map[string]ingressmanifest.APIPath{}
	for _, route := range expected {
		expectedByPath[route.Path] = route
	}
	seen := map[string]map[string]bool{}
	hosts := map[string]bool{}
	namespaces := map[string]bool{platformNS: true, analyticsNS: true}
	for ns := range namespaces {
		ingresses, err := cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return audienceMigrationRequired("split API ingresses cannot be verified")
		}
		for _, ingress := range ingresses.Items {
			middlewares := strings.Split(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"], ",")
			for _, rule := range ingress.Spec.Rules {
				if rule.HTTP == nil {
					continue
				}
				for _, route := range rule.HTTP.Paths {
					required, canonical := expectedByPath[route.Path]
					if canonical && (route.Backend.Service == nil || route.Backend.Service.Name != required.Service || int(route.Backend.Service.Port.Number) != required.Port || route.PathType == nil || string(*route.PathType) != required.PathType) {
						return audienceMigrationRequired("an API route has the wrong backend, port or match type")
					}
					if route.Backend.Service == nil {
						continue
					}
					service := route.Backend.Service.Name
					if service != "mcp-platform-api" && service != "mcp-runtime-api" && service != "mcp-analytics-api" {
						continue
					}
					backendNS := platformNS
					if service == "mcp-analytics-api" {
						backendNS = analyticsNS
					}
					if ingress.Namespace != backendNS {
						return audienceMigrationRequired("a split API ingress is in the wrong backend namespace")
					}
					if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName != "traefik" {
						return audienceMigrationRequired("API ingress does not use the supported Traefik class")
					}
					if route.Path == "/api/v1/auth" && service == "mcp-platform-api" {
						hosts[rule.Host] = true
					}
					want := ""
					if service == "mcp-runtime-api" {
						want = "runtime-token-exchange@file"
					}
					if service == "mcp-analytics-api" {
						want = "analytics-token-exchange@file"
					}
					if want != "" && (len(middlewares) == 0 || strings.TrimSpace(middlewares[len(middlewares)-1]) != want) {
						return audienceMigrationRequired("a split API route lacks its final token-exchange middleware")
					}
					if want == "" {
						for _, middleware := range middlewares {
							if strings.Contains(middleware, "token-exchange") {
								return audienceMigrationRequired("platform routes incorrectly exchange their login token")
							}
						}
					}
					if service == "mcp-platform-api" && (route.Path == "/" || route.Path == "/internal" || strings.HasPrefix(route.Path, "/internal/")) {
						return audienceMigrationRequired("an internal platform route is publicly exposed")
					}
					if canonical {
						if seen[rule.Host] == nil {
							seen[rule.Host] = map[string]bool{}
						}
						seen[rule.Host][required.Path] = true
					}
				}
			}
		}
	}
	if len(hosts) == 0 {
		return audienceMigrationRequired("platform login ingress is missing")
	}
	for host := range hosts {
		for _, required := range expected {
			if !seen[host][required.Path] {
				return audienceMigrationRequired("a platform host is missing a split API route")
			}
		}
	}
	controllers, err := cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return audienceMigrationRequired("Traefik controllers cannot be verified")
	}
	found := false
	for _, deployment := range controllers.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "traefik" {
				continue
			}
			args := map[string]string{}
			for _, arg := range container.Args {
				key, value, _ := strings.Cut(arg, "=")
				args[strings.ToLower(key)] = value
			}
			if value, configured := args["--providers.kubernetesingress"]; !configured || value != "" && !strings.EqualFold(value, "true") {
				continue
			}
			watched := args["--providers.kubernetesingress.namespaces"]
			if watched != "" && !containsNamespace(watched, platformNS) && !containsNamespace(watched, analyticsNS) {
				continue
			}
			if watched != "" && (!containsNamespace(watched, platformNS) || !containsNamespace(watched, analyticsNS)) {
				return audienceMigrationRequired("Traefik must watch both platform and analytics namespaces")
			}
			found = true
			filename := args["--providers.file.filename"]
			if filename != "/etc/traefik/dynamic/dynamic.yml" || args["--providers.file.watch"] != "true" {
				return audienceMigrationRequired("Traefik does not watch the managed middleware file")
			}
			configMapName := ""
			for _, mount := range container.VolumeMounts {
				if mount.MountPath != path.Dir(filename) || mount.SubPath != "" {
					continue
				}
				for _, volume := range deployment.Spec.Template.Spec.Volumes {
					if volume.Name != mount.Name || volume.ConfigMap == nil {
						continue
					}
					if len(volume.ConfigMap.Items) == 0 {
						configMapName = volume.ConfigMap.Name
					}
					for _, item := range volume.ConfigMap.Items {
						if item.Key == "dynamic.yml" && item.Path == "dynamic.yml" {
							configMapName = volume.ConfigMap.Name
						}
					}
				}
			}
			if configMapName == "" {
				return audienceMigrationRequired("Traefik's middleware ConfigMap is not mounted")
			}
			config, err := cs.CoreV1().ConfigMaps(deployment.Namespace).Get(ctx, configMapName, metav1.GetOptions{})
			if err != nil || !validAudienceMiddlewares(config.Data["dynamic.yml"], platformNS) {
				return audienceMigrationRequired("mounted Traefik exchange middleware is missing or unsafe")
			}
		}
	}
	if !found {
		return audienceMigrationRequired("no supported Traefik ingress controller was found")
	}
	return nil
}

func containsNamespace(csv, namespace string) bool {
	for _, entry := range strings.Split(csv, ",") {
		if strings.TrimSpace(entry) == namespace {
			return true
		}
	}
	return false
}

func validAudienceMiddlewares(raw, platformNS string) bool {
	var dynamic struct {
		HTTP struct {
			Middlewares map[string]struct {
				ForwardAuth struct {
					Address        string   `json:"address"`
					Trust          bool     `json:"trustForwardHeader"`
					Body           bool     `json:"forwardBody"`
					PreserveMethod bool     `json:"preserveRequestMethod"`
					Request        []string `json:"authRequestHeaders"`
					Response       []string `json:"authResponseHeaders"`
				} `json:"forwardAuth"`
			} `json:"middlewares"`
		} `json:"http"`
	}
	// Decode only the exchange contract; other managed middleware/plugin fields
	// are independently owned by setup.
	if yaml.Unmarshal([]byte(raw), &dynamic) != nil {
		return false
	}
	for _, target := range []string{platformauth.AudienceRuntime, platformauth.AudienceAnalytics} {
		name := strings.TrimSuffix(target, "-api")
		auth := dynamic.HTTP.Middlewares[name+"-token-exchange"].ForwardAuth
		if auth.Address != "http://mcp-platform-api."+platformNS+".svc.cluster.local:8080"+platformauth.ExchangePath(target) || auth.Trust || auth.Body || auth.PreserveMethod {
			return false
		}
		if !exchangeHeaderSet(auth.Request) || !exchangeHeaderSet(auth.Response) {
			return false
		}
	}
	return true
}

func exchangeHeaderSet(headers []string) bool {
	if len(headers) != 2 {
		return false
	}
	return strings.EqualFold(headers[0], "Authorization") && strings.EqualFold(headers[1], "X-Api-Key") || strings.EqualFold(headers[1], "Authorization") && strings.EqualFold(headers[0], "X-Api-Key")
}
