package operator

import (
	"net/url"
	"strings"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

// effectiveAnalyticsIngestURL returns the ingest URL the operator uses for a
// server. The platform derives this value; specs written by older releases may
// carry a plain-http platform URL, which is replaced by the operator's HTTPS
// default. Any other URL is returned unchanged so existing validation applies.
func effectiveAnalyticsIngestURL(server *mcpv1alpha1.MCPServer, defaultURL string) string {
	defaultURL = strings.TrimSpace(defaultURL)
	specURL := ""
	if server != nil && server.Spec.Analytics != nil {
		specURL = strings.TrimSpace(server.Spec.Analytics.IngestURL)
	}
	if specURL == "" {
		return defaultURL
	}
	if defaultURL == "" {
		return specURL
	}
	parsed, err := url.Parse(specURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" {
		return specURL
	}
	host := strings.ToLower(parsed.Hostname())
	if def, err := url.Parse(defaultURL); err == nil && host == strings.ToLower(def.Hostname()) {
		return defaultURL
	}
	if isPlatformIngestServiceHost(host) {
		return defaultURL
	}
	return specURL
}

// isPlatformIngestServiceHost matches mcp-ingest.<namespace>.svc and
// mcp-ingest.<namespace>.svc.cluster.local only.
func isPlatformIngestServiceHost(host string) bool {
	rest, ok := strings.CutPrefix(host, "mcp-ingest.")
	if !ok {
		return false
	}
	ns, suffix, ok := strings.Cut(rest, ".")
	if !ok || ns == "" {
		return false
	}
	return suffix == "svc" || suffix == "svc.cluster.local"
}
