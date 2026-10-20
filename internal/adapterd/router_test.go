package adapterd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterIsolatesProfilesByPath(t *testing.T) {
	r := NewRouter()
	var seen []string
	r.Set(Route{
		Profile: Profile{Slug: "cully", Server: "cully"},
		Token:   "cully-token",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, "cully "+req.URL.Path+" auth="+req.Header.Get("Authorization")+" local="+req.Header.Get(LocalTokenHeader))
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	r.Set(Route{
		Profile: Profile{Slug: "github", Server: "github"},
		Token:   "github-token",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, "github "+req.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/cully/mcp", strings.NewReader("{}"))
	req.Header.Set(LocalTokenHeader, "cully-token")
	req.Header.Set("Authorization", "Bearer oauth-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cully status %d", rec.Code)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "auth=Bearer oauth-token") || !strings.Contains(seen[0], "local=") || strings.Contains(seen[0], "cully-token") {
		t.Fatalf("forwarded request = %v", seen)
	}

	wrong := httptest.NewRequest(http.MethodPost, "/github/mcp", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, wrong)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("github status %d", rec.Code)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/missing/mcp", nil)
	unknown.Header.Set(LocalTokenHeader, "cully-token")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status %d", rec.Code)
	}
}

func TestUniqueSlugSuffix(t *testing.T) {
	taken := map[string]bool{"cully": true}
	if got := UniqueSlug("cully", func(s string) bool { return taken[s] }); got != "cully-2" {
		t.Fatalf("slug %s", got)
	}
	slug, err := SlugFromServer("Cully Server")
	if err != nil || slug != "cully-server" {
		t.Fatalf("slug %q err %v", slug, err)
	}
}

func TestHealthIsLocalReadinessOnly(t *testing.T) {
	r := NewRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusNoContent {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("health %d %s", rec.Code, body)
	}
}
