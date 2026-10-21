package adapterd

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Route is one profile mounted at /<slug>/mcp.
type Route struct {
	Profile Profile
	Token   string
	Handler http.Handler
	// UpstreamReachable is the last connectivity probe, distinct from local readiness.
	UpstreamReachable *bool
	UpstreamCheckedAt time.Time
}

// Router serves every profile on one listener and rejects unknown paths.
type Router struct {
	mu     sync.RWMutex
	routes map[string]*Route
	now    func() time.Time
}

func NewRouter() *Router {
	return &Router{routes: map[string]*Route{}, now: time.Now}
}

func (r *Router) Set(route Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := route
	r.routes[route.Profile.Slug] = &cp
}

func (r *Router) Delete(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, slug)
}

func (r *Router) Routes() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Route, 0, len(r.routes))
	for _, route := range r.routes {
		out = append(out, *route)
	}
	return out
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/healthz", "/livez", "/readyz":
		w.WriteHeader(http.StatusNoContent)
		return
	}
	slug, rest, ok := splitProfilePath(req.URL.Path)
	if !ok {
		http.NotFound(w, req)
		return
	}
	r.mu.RLock()
	route := r.routes[slug]
	r.mu.RUnlock()
	if route == nil {
		http.NotFound(w, req)
		return
	}
	provided := req.Header.Get(LocalTokenHeader)
	if route.Token == "" || provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(route.Token)) != 1 {
		http.Error(w, "local profile credential required", http.StatusUnauthorized)
		return
	}
	if route.Handler == nil {
		http.Error(w, "profile is not enrolled", http.StatusServiceUnavailable)
		return
	}
	clone := req.Clone(req.Context())
	// Drop the local credential so it cannot be treated as an upstream credential.
	clone.Header.Del(LocalTokenHeader)
	clone.URL.Path = rest
	clone.URL.RawPath = ""
	route.Handler.ServeHTTP(w, clone)
}

func splitProfilePath(path string) (slug, rest string, ok bool) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 || parts[1] != "mcp" {
		return "", "", false
	}
	slug = parts[0]
	if slug == "" || strings.Contains(slug, "..") {
		return "", "", false
	}
	rest = "/" + strings.Join(parts[1:], "/")
	return slug, rest, true
}
