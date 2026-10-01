package k8sclient

import (
	"context"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	"mcp-runtime/pkg/platforminventory"
)

const (
	// RegistryIngressNamespace and RegistryIngressName identify the bundled
	// registry Ingress that nodes pull through on public installs.
	RegistryIngressNamespace = "registry"
	RegistryIngressName      = "registry"
	// RegistryPlaceholderHost is the dev placeholder host baked into
	// config/registry manifests.
	RegistryPlaceholderHost = "registry.local"

	platformConfigNamespace = platforminventory.PlatformNamespace
	platformConfigName      = platforminventory.SharedConfigName
)

// Registry public host sources, in precedence order.
const (
	RegistryHostSourceExplicit       = "explicit"
	RegistryHostSourceIngressTLS     = "registry Ingress TLS host"
	RegistryHostSourceIngressRule    = "registry Ingress rule host"
	RegistryHostSourceConfigHost     = "mcp-shared-config MCP_REGISTRY_INGRESS_HOST"
	RegistryHostSourceConfigDomain   = "mcp-shared-config MCP_PLATFORM_DOMAIN"
	RegistryHostSourcePlaceholder    = "default placeholder"
	registryHostSourceDomainTemplate = "registry.%s"
)

// IsPlaceholderRegistryHost reports whether host is empty or a dev-only
// placeholder that no public client or node can pull from.
func IsPlaceholderRegistryHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", RegistryPlaceholderHost, "localhost":
		return true
	}
	return false
}

// RegistryHostSources collects every place the registry public host can come
// from. Explicit is the caller's configured value (env/flag), which may be the
// placeholder default when the caller's shell has no MCP_* env.
type RegistryHostSources struct {
	Explicit             string
	IngressTLSHosts      []string
	IngressRuleHosts     []string
	ConfigIngressHost    string
	ConfigPlatformDomain string
}

// ChooseRegistryPublicHost picks the registry public host. An explicit
// non-placeholder value wins (setup changing the domain on purpose); otherwise
// authoritative cluster state wins over the placeholder, so an env-less caller
// never downgrades a public host to registry.local.
func ChooseRegistryPublicHost(s RegistryHostSources) (string, string) {
	if h := strings.TrimSpace(s.Explicit); !IsPlaceholderRegistryHost(h) {
		return h, RegistryHostSourceExplicit
	}
	if h := firstPublicHost(s.IngressTLSHosts); h != "" {
		return h, RegistryHostSourceIngressTLS
	}
	if h := firstPublicHost(s.IngressRuleHosts); h != "" {
		return h, RegistryHostSourceIngressRule
	}
	if h := strings.TrimSpace(s.ConfigIngressHost); !IsPlaceholderRegistryHost(h) {
		return h, RegistryHostSourceConfigHost
	}
	if d := strings.Trim(strings.ToLower(strings.TrimSpace(s.ConfigPlatformDomain)), "."); d != "" {
		return fmt.Sprintf(registryHostSourceDomainTemplate, d), RegistryHostSourceConfigDomain
	}
	return RegistryPlaceholderHost, RegistryHostSourcePlaceholder
}

// ResolveRegistryPublicHost reads the live registry Ingress and platform
// config ConfigMap and applies ChooseRegistryPublicHost. Read errors are
// treated as "no cluster state" so fresh installs still work.
func ResolveRegistryPublicHost(ctx context.Context, clients *Clients, explicit string) (string, string) {
	sources := RegistryHostSources{Explicit: explicit}
	if clients != nil && clients.Clientset != nil {
		if ing, err := clients.Clientset.NetworkingV1().Ingresses(RegistryIngressNamespace).Get(ctx, RegistryIngressName, metav1.GetOptions{}); err == nil {
			for _, tls := range ing.Spec.TLS {
				sources.IngressTLSHosts = append(sources.IngressTLSHosts, tls.Hosts...)
			}
			for _, rule := range ing.Spec.Rules {
				sources.IngressRuleHosts = append(sources.IngressRuleHosts, rule.Host)
			}
		}
		if cm, err := clients.Clientset.CoreV1().ConfigMaps(platformConfigNamespace).Get(ctx, platformConfigName, metav1.GetOptions{}); err == nil {
			sources.ConfigIngressHost = cm.Data["MCP_REGISTRY_INGRESS_HOST"]
			sources.ConfigPlatformDomain = cm.Data["MCP_PLATFORM_DOMAIN"]
		}
	}
	return ChooseRegistryPublicHost(sources)
}

// CheckRegistryIngressDowngrade refuses a registry Ingress whose rule host is
// the placeholder while the desired TLS hosts or the live Ingress already name
// a public host. Traefik matches routers on the rule host, so that combination
// returns a plain 404 for every request to the public registry hostname and
// breaks node image pulls.
func CheckRegistryIngressDowngrade(desiredRuleHosts, desiredTLSHosts, liveRuleHosts, liveTLSHosts []string) error {
	placeholder := false
	for _, h := range desiredRuleHosts {
		if IsPlaceholderRegistryHost(h) {
			placeholder = true
			break
		}
	}
	if !placeholder {
		return nil
	}
	public := firstPublicHost(desiredTLSHosts)
	if public == "" {
		public = firstPublicHost(liveTLSHosts)
	}
	if public == "" {
		public = firstPublicHost(liveRuleHosts)
	}
	if public == "" {
		return nil
	}
	return fmt.Errorf(
		"refusing to apply registry Ingress %s/%s with rule host %q while it serves public host %q (Traefik would return 404 for https://%s/v2/ and node pulls would fail); "+
			"set MCP_PLATFORM_DOMAIN or MCP_REGISTRY_INGRESS_HOST, or run: kubectl patch ingress %s -n %s --type=json -p '[{\"op\":\"replace\",\"path\":\"/spec/rules/0/host\",\"value\":\"%s\"}]'",
		RegistryIngressNamespace, RegistryIngressName, RegistryPlaceholderHost, public, public,
		RegistryIngressName, RegistryIngressNamespace, public)
}

// CheckRegistryIngressManifest runs CheckRegistryIngressDowngrade for the
// registry Ingress in a rendered multi-document manifest against the live
// object. Used by kubectl-based apply paths that bypass ApplyManifestYAML.
func CheckRegistryIngressManifest(ctx context.Context, clients *Clients, manifest string) error {
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode registry manifest: %w", err)
		}
		if !isRegistryIngress(obj, obj.GetNamespace()) {
			continue
		}
		var liveRules, liveTLS []string
		if clients != nil && clients.Clientset != nil {
			ing, err := clients.Clientset.NetworkingV1().Ingresses(RegistryIngressNamespace).Get(ctx, RegistryIngressName, metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("get registry ingress: %w", err)
			}
			if err == nil {
				for _, tls := range ing.Spec.TLS {
					liveTLS = append(liveTLS, tls.Hosts...)
				}
				for _, rule := range ing.Spec.Rules {
					liveRules = append(liveRules, rule.Host)
				}
			}
		}
		rules, tls := unstructuredIngressHosts(obj.Object)
		return CheckRegistryIngressDowngrade(rules, tls, liveRules, liveTLS)
	}
}

func isRegistryIngress(obj *unstructured.Unstructured, namespace string) bool {
	if obj == nil || obj.GetKind() != "Ingress" || obj.GetName() != RegistryIngressName {
		return false
	}
	ns := strings.TrimSpace(namespace)
	return ns == "" || ns == RegistryIngressNamespace
}

// unstructuredIngressHosts returns spec.rules[].host and spec.tls[].hosts[].
func unstructuredIngressHosts(obj map[string]any) ([]string, []string) {
	var rules, tlsHosts []string
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return nil, nil
	}
	if items, ok := spec["rules"].([]any); ok {
		for _, item := range items {
			if rule, ok := item.(map[string]any); ok {
				if host, ok := rule["host"].(string); ok {
					rules = append(rules, host)
				}
			}
		}
	}
	if items, ok := spec["tls"].([]any); ok {
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			hosts, _ := entry["hosts"].([]any)
			for _, h := range hosts {
				if s, ok := h.(string); ok {
					tlsHosts = append(tlsHosts, s)
				}
			}
		}
	}
	return rules, tlsHosts
}

func firstPublicHost(hosts []string) string {
	for _, h := range hosts {
		if h = strings.TrimSpace(h); !IsPlaceholderRegistryHost(h) {
			return h
		}
	}
	return ""
}
