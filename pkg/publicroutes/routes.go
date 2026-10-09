// Package publicroutes validates customer-facing paths shared by setup and the UI.
package publicroutes

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const DefaultDocsURL = "https://mcpruntime.org/docs/"

type Routes struct {
	Platform string
	Grafana  string
	Docs     string
	DocsURL  string
	Registry string
}

func (r Routes) WithDefaults() Routes {
	if r.Platform == "" {
		r.Platform = "/"
	}
	if r.Grafana == "" {
		r.Grafana = "/grafana"
	}
	if r.Docs == "" {
		r.Docs = "/docs"
	}
	if r.DocsURL == "" {
		r.DocsURL = DefaultDocsURL
	}
	if r.Registry == "" {
		r.Registry = "/registry"
	}
	return r
}

var pathPattern = regexp.MustCompile(`^/[A-Za-z0-9/_.~-]*$`)

// ValidatePath rejects escaped separators, traversal, URL components, and
// characters that could change a generated ingress or middleware definition.
func ValidatePath(value string, allowRoot bool) error {
	if !pathPattern.MatchString(value) || path.Clean(value) != value || (!allowRoot && value == "/") {
		return fmt.Errorf("%q must be a clean absolute path without a trailing slash, escapes, or URL components", value)
	}
	return nil
}

func Overlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (r Routes) Validate() error {
	r = r.WithDefaults()
	paths := []struct{ name, value string }{{"platform", r.Platform}, {"grafana", r.Grafana}, {"docs", r.Docs}, {"registry", r.Registry}}
	for _, entry := range paths {
		name, value := entry.name, entry.value
		if err := ValidatePath(value, name == "platform"); err != nil {
			return fmt.Errorf("%s path: %w", name, err)
		}
		for _, reserved := range []string{"/api", "/v2", "/.well-known", "/health"} {
			if Overlap(value, reserved) {
				return fmt.Errorf("%s path %q overlaps reserved path %q", name, value, reserved)
			}
		}
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if a.value != "/" && Overlap(a.value, b.value) {
				return fmt.Errorf("%s path overlaps %s path", a.name, b.name)
			}
		}
		if r.Platform == "/" && a.name != "platform" {
			for _, reserved := range []string{"/auth", "/assets", "/brand", "/config.js", "/favicon.png"} {
				if Overlap(a.value, reserved) {
					return fmt.Errorf("%s path overlaps dashboard route %q", a.name, reserved)
				}
			}
		}
	}
	u, err := url.Parse(r.DocsURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("docs URL must be an absolute HTTPS URL without credentials")
	}
	return nil
}

// ValidateAuth checks collisions when the issuer shares the platform hostname.
func (r Routes) ValidateAuth(platformHost, issuer string) error {
	if issuer == "" {
		return nil
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("MCP Auth issuer must be an absolute URL without credentials, query, or fragment")
	}
	if err := ValidatePath(u.Path, false); err != nil {
		return fmt.Errorf("MCP Auth issuer path: %w", err)
	}
	r = r.WithDefaults()
	if strings.EqualFold(u.Host, platformHost) {
		for _, route := range []string{r.Grafana, r.Docs, r.Registry, "/api", "/v2", "/.well-known", "/health"} {
			if Overlap(u.Path, route) {
				return fmt.Errorf("MCP Auth issuer path %q overlaps route %q", u.Path, route)
			}
		}
		if (r.Platform != "/" && Overlap(u.Path, r.Platform)) || (r.Platform == "/" && Overlap(u.Path, "/auth")) {
			return fmt.Errorf("MCP Auth issuer path overlaps dashboard routes; choose a separate --platform-path-prefix")
		}
		if r.Platform == "/" {
			for _, reserved := range []string{"/assets", "/brand", "/config.js", "/favicon.png"} {
				if Overlap(u.Path, reserved) {
					return fmt.Errorf("MCP Auth issuer path overlaps dashboard asset route %q", reserved)
				}
			}
		}
	}
	return nil
}

func (r Routes) PlatformPrefix() string { return strings.TrimSuffix(r.WithDefaults().Platform, "/") }
