package main

import (
	"net/http"
	"os"
	"strings"

	"mcp-runtime/pkg/publicroutes"
)

func configuredPublicRoutes() publicroutes.Routes {
	return publicroutes.Routes{
		Platform: os.Getenv("UI_PATH_PREFIX"), Grafana: os.Getenv("UI_GRAFANA_PATH"),
		Docs: os.Getenv("UI_DOCS_PATH"), DocsURL: os.Getenv("UI_DOCS_URL"), Registry: os.Getenv("UI_REGISTRY_PATH"),
	}.WithDefaults()
}

// mountPublicUI keeps probes and the internal Grafana forward-auth endpoint
// stable while moving all browser session, proxy, and asset routes together.
func mountPublicUI(inner *http.ServeMux, routes publicroutes.Routes) *http.ServeMux {
	outer := http.NewServeMux()
	outer.Handle("/health", inner)
	outer.Handle("/auth/admin-check", inner)
	redirect := func(route, target string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.URL.Path != route && r.URL.Path != route+"/" {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		}
	}
	for route, target := range map[string]string{routes.Docs: routes.DocsURL, routes.Registry: routes.PlatformPrefix() + "/#/servers"} {
		handler := redirect(route, target)
		outer.HandleFunc(route, handler)
		outer.HandleFunc(route+"/", handler)
	}
	prefix := routes.PlatformPrefix()
	if prefix == "" {
		outer.Handle("/", inner)
	} else {
		outer.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			target := prefix + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		})
		outer.Handle(prefix+"/", http.StripPrefix(prefix, inner))
	}
	return outer
}

func prefixAssetHTML(data []byte, prefix string) []byte {
	// Only the bundled HTML is rewritten; paths are validated at startup.
	s := string(data)
	if prefix != "" {
		s = strings.NewReplacer(`src="/`, `src="`+prefix+`/`, `href="/`, `href="`+prefix+`/`).Replace(s)
	}
	s = strings.Replace(s, "<head>", `<head><base href="`+prefix+`/">`, 1)
	return []byte(s)
}
