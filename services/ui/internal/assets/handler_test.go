package assets

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundledResponseContract(t *testing.T) {
	files := fstest.MapFS{"static/index.html": &fstest.MapFile{Data: []byte("INDEX")}, "static/assets/app.js": &fstest.MapFile{Data: []byte("JS")}, "static/brand.png": &fstest.MapFile{Data: []byte("IMAGE")}}
	handler := Handler{Files: files, RewriteHTML: func(b []byte) []byte { return []byte("prefix:" + string(b)) }}
	for _, tc := range []struct {
		path, body, cache, contentType string
		status                         int
	}{{"/", "prefix:INDEX", "no-cache", "text/html", 200}, {"/assets/app.js", "JS", "max-age=60", "javascript", 200}, {"/brand.png", "IMAGE", "max-age=300", "image/png", 200}, {"/missing", "", "", "", 404}, {"/../private", "", "", "", 404}} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d", w.Code)
			}
			if tc.status == 200 && (w.Body.String() != tc.body || w.Header().Get("cache-control") != tc.cache || !strings.Contains(w.Header().Get("content-type"), tc.contentType)) {
				t.Fatalf("body=%q,headers=%v", w.Body.String(), w.Header())
			}
		})
	}
}
