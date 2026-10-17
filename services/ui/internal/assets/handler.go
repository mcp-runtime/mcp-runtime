// Package assets owns bundled console file responses and their cache contract.
package assets

import (
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// Handler serves the embedded tree. Public-route HTML rewriting remains supplied
// by the route owner, so file serving does not depend on sessions or API clients.
type Handler struct {
	Files       fs.ReadFileFS
	RewriteHTML func([]byte) []byte
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "static/index.html"
	} else {
		path = filepath.ToSlash(filepath.Join("static", path))
	}
	data, err := h.Files.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := filepath.Ext(path)
	if ext != "" {
		if contentType := mime.TypeByExtension(ext); contentType != "" {
			w.Header().Set("content-type", contentType)
		}
	}
	switch ext {
	case ".html":
		w.Header().Set("cache-control", "no-cache")
	case ".js", ".css":
		w.Header().Set("cache-control", "max-age=60")
	default:
		w.Header().Set("cache-control", "max-age=300")
	}
	if ext == ".html" {
		data = h.RewriteHTML(data)
	}
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- assets are bundled from repository static/ at build time.
	_, _ = w.Write(data)
}
