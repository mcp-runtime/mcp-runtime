// Package ingressmanifest builds YAML for the host-based platform UI Ingress.
package ingressmanifest

import (
	"strconv"
	"strings"

	"mcp-runtime/pkg/publicroutes"
)

const (
	// PlatformIngressName is the Kubernetes Ingress resource name for the dashboard.
	PlatformIngressName = "mcp-platform-ui"
	// PlatformObservabilityIngressName is the admin-gated platform Ingress for Grafana.
	PlatformObservabilityIngressName = "mcp-platform-observability"
	// PlatformAnalyticsIngressName is the public Ingress for analytics-api.
	PlatformAnalyticsIngressName = "mcp-platform-analytics"
	// PlatformHTTPRedirectIngressName is the HTTP-only redirect Ingress resource name.
	PlatformHTTPRedirectIngressName = "mcp-platform-ui-http"
	// PlatformTLSSecretName is the TLS secret name used when TLS is enabled.
	PlatformTLSSecretName = "mcp-platform-tls" // #nosec G101 -- Kubernetes Secret name, not a credential.
)

// RenderPlatformUIIngress emits Ingresses for platform.<domain>. The UI,
// platform-api, and runtime-api Ingress is created in platformNamespace.
// Grafana and analytics-api routes are created in observabilityNamespace,
// beside those Services. Server-side UI auth still uses API_UPSTREAM against
// platform-api. The observability Ingress uses the repo-managed
// platform-admin-auth@file Traefik middleware so Grafana is reachable from
// admin UI links without exposing it raw on the public platform host.
// Prometheus stays internal as Grafana's metrics datasource and is not
// exposed as a direct public route.
//
// When issuerName is set, only the platform Ingress gets a TLS section and
// cert-manager annotation. Ingress-shim then creates one Certificate named
// mcp-platform-tls in platformNamespace. The observability Ingress does not
// name a Secret and does not request a Certificate: a TLS Secret cannot be
// mounted across namespaces, and a second Certificate would open another ACME
// order for the same hostname. Traefik serves that host from the certificate
// loaded by the platform Ingress. An HTTP Ingress on the web entrypoint sends
// plain requests to the UI, which redirects to HTTPS. The prod overlay
// disables Traefik's entrypoint redirect so HTTP-01 challenges keep working.
func RenderPlatformUIIngress(host, issuerName string, tlsEnabled bool, platformNamespace, observabilityNamespace string, configured ...publicroutes.Routes) string {
	routes := publicroutes.Routes{}.WithDefaults()
	if len(configured) > 0 {
		routes = configured[0].WithDefaults()
	}
	host = strings.TrimSpace(host)
	issuerName = strings.TrimSpace(issuerName)
	platformNamespace = strings.TrimSpace(platformNamespace)
	observabilityNamespace = strings.TrimSpace(observabilityNamespace)

	var b strings.Builder
	b.WriteString("apiVersion: networking.k8s.io/v1\n")
	b.WriteString("kind: Ingress\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: ")
	b.WriteString(PlatformIngressName)
	b.WriteString("\n")
	b.WriteString("  namespace: ")
	b.WriteString(platformNamespace)
	b.WriteString("\n")
	b.WriteString("  annotations:\n")
	if tlsEnabled {
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: websecure\n")
		if issuerName != "" {
			b.WriteString("    cert-manager.io/cluster-issuer: ")
			b.WriteString(issuerName)
			b.WriteString("\n")
		}
	} else {
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: web\n")
	}
	b.WriteString("spec:\n")
	b.WriteString("  ingressClassName: traefik\n")
	if tlsEnabled {
		b.WriteString("  tls:\n")
		b.WriteString("    - hosts:\n")
		b.WriteString("        - ")
		b.WriteString(strconv.Quote(host))
		b.WriteString("\n")
		b.WriteString("      secretName: ")
		b.WriteString(PlatformTLSSecretName)
		b.WriteString("\n")
	}
	b.WriteString("  rules:\n")
	b.WriteString("    - host: ")
	b.WriteString(strconv.Quote(host))
	b.WriteString("\n")
	b.WriteString("      http:\n")
	b.WriteString("        paths:\n")
	writeAPIIngressPaths(&b, PlatformAPIPaths())
	writeAPIIngressPaths(&b, []APIPath{{Path: routes.Docs, PathType: "Prefix", Service: "mcp-ui", Port: 8082}, {Path: routes.Registry, PathType: "Prefix", Service: "mcp-ui", Port: 8082}})
	b.WriteString("          - path: " + routes.Platform + "\n")
	b.WriteString("            pathType: Prefix\n")
	b.WriteString("            backend:\n")
	b.WriteString("              service:\n")
	b.WriteString("                name: mcp-ui\n")
	b.WriteString("                port:\n")
	b.WriteString("                  number: 8082\n")

	b.WriteString("---\n")
	b.WriteString("apiVersion: networking.k8s.io/v1\n")
	b.WriteString("kind: Ingress\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: ")
	b.WriteString(PlatformObservabilityIngressName)
	b.WriteString("\n")
	b.WriteString("  namespace: ")
	b.WriteString(observabilityNamespace)
	b.WriteString("\n")
	b.WriteString("  annotations:\n")
	if tlsEnabled {
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: websecure\n")
	} else {
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: web\n")
	}
	b.WriteString("    traefik.ingress.kubernetes.io/router.middlewares: platform-admin-auth@file\n")
	b.WriteString("spec:\n")
	b.WriteString("  ingressClassName: traefik\n")
	b.WriteString("  rules:\n")
	b.WriteString("    - host: ")
	b.WriteString(strconv.Quote(host))
	b.WriteString("\n")
	b.WriteString("      http:\n")
	b.WriteString("        paths:\n")
	b.WriteString("          - path: " + routes.Grafana + "\n")
	b.WriteString("            pathType: Prefix\n")
	b.WriteString("            backend:\n")
	b.WriteString("              service:\n")
	b.WriteString("                name: grafana\n")
	b.WriteString("                port:\n")
	b.WriteString("                  number: 3000\n")

	b.WriteString("---\n")
	b.WriteString("apiVersion: networking.k8s.io/v1\n")
	b.WriteString("kind: Ingress\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: ")
	b.WriteString(PlatformAnalyticsIngressName)
	b.WriteString("\n")
	b.WriteString("  namespace: ")
	b.WriteString(observabilityNamespace)
	b.WriteString("\n")
	if tlsEnabled {
		b.WriteString("  annotations:\n")
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: websecure\n")
	} else {
		b.WriteString("  annotations:\n")
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: web\n")
	}
	b.WriteString("spec:\n")
	b.WriteString("  ingressClassName: traefik\n")
	b.WriteString("  rules:\n")
	b.WriteString("    - host: ")
	b.WriteString(strconv.Quote(host))
	b.WriteString("\n")
	b.WriteString("      http:\n")
	b.WriteString("        paths:\n")
	writeAPIIngressPaths(&b, ObservabilityAPIPaths())

	if tlsEnabled {
		// HTTP-only ingress on the same host so plain `http://platform.<domain>/`
		// hits the UI service (which 308s to HTTPS) instead of falling through to
		// the host-less dev gateway ingress in k8s/10-gateway.yaml.
		b.WriteString("---\n")
		b.WriteString("apiVersion: networking.k8s.io/v1\n")
		b.WriteString("kind: Ingress\n")
		b.WriteString("metadata:\n")
		b.WriteString("  name: ")
		b.WriteString(PlatformHTTPRedirectIngressName)
		b.WriteString("\n")
		b.WriteString("  namespace: ")
		b.WriteString(platformNamespace)
		b.WriteString("\n")
		b.WriteString("  annotations:\n")
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: web\n")
		b.WriteString("spec:\n")
		b.WriteString("  ingressClassName: traefik\n")
		b.WriteString("  rules:\n")
		b.WriteString("    - host: ")
		b.WriteString(strconv.Quote(host))
		b.WriteString("\n")
		b.WriteString("      http:\n")
		b.WriteString("        paths:\n")
		writeAPIIngressPaths(&b, []APIPath{{Path: routes.Docs, PathType: "Prefix", Service: "mcp-ui", Port: 8082}, {Path: routes.Registry, PathType: "Prefix", Service: "mcp-ui", Port: 8082}})
		b.WriteString("          - path: " + routes.Platform + "\n")
		b.WriteString("            pathType: Prefix\n")
		b.WriteString("            backend:\n")
		b.WriteString("              service:\n")
		b.WriteString("                name: mcp-ui\n")
		b.WriteString("                port:\n")
		b.WriteString("                  number: 8082\n")
	}

	return b.String()
}

func writeAPIIngressPaths(b *strings.Builder, routes []APIPath) {
	for _, route := range routes {
		b.WriteString("          - path: ")
		b.WriteString(route.Path)
		b.WriteString("\n            pathType: ")
		b.WriteString(route.PathType)
		b.WriteString("\n            backend:\n              service:\n                name: ")
		b.WriteString(route.Service)
		b.WriteString("\n                port:\n                  number: ")
		b.WriteString(strconv.Itoa(route.Port))
		b.WriteString("\n")
	}
}
