//go:build darwin || linux

package adapterservice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validProfile() Profile {
	return Profile{Name: "work", PlatformURL: "https://platform.example", RuntimeURL: "https://mcp.example/mcp", Namespace: "team-a", Server: "weather", Agent: "agent-1", Human: "user-1", Team: "team-a", Session: "session-1", TrustDomain: "mcp.example", SessionExpires: time.Now().Add(time.Hour), Listen: "127.0.0.1:8099", PlatformToken: "platform-secret", LocalToken: strings.Repeat("a", 64)}
}

func TestStoreKeepsProfilesOwnerOnlyAndValidatesBeforeSaving(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "adapter")}
	p := validProfile()
	if err := s.Save([]Profile{p}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{s.Dir: 0700, filepath.Join(s.Dir, "profiles.json"): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
	got, err := s.Load()
	if err != nil || len(got) != 1 || got[0].LocalToken != p.LocalToken {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
	p.Listen = "0.0.0.0:8099"
	if err := s.Save([]Profile{p}); err == nil {
		t.Fatal("Save accepted a non-loopback listener")
	}
}

func TestStatusAndEventsDoNotExposeCredentials(t *testing.T) {
	d := &Daemon{Store: Store{Dir: filepath.Join(t.TempDir(), "adapter")}, workers: map[string]*worker{}}
	p := validProfile()
	d.workers[p.Name] = &worker{p: p, status: ProfileStatus{Name: p.Name, Authentication: "enrolling"}}
	d.event(p.Name, "certificate renewal failed; check login, session and network")
	b, err := json.Marshal(d.Status())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{p.PlatformToken, p.LocalToken, p.Session} {
		if strings.Contains(string(b), secret) {
			t.Errorf("status leaked credential/session %q: %s", secret, b)
		}
	}
}

func TestControlHandlerRejectsOriginAndWrongHost(t *testing.T) {
	d := &Daemon{Store: Store{Dir: filepath.Join(t.TempDir(), "adapter")}, workers: map[string]*worker{}}
	h := d.Handler()
	for _, tc := range []struct{ host, origin string }{{"adapter.local", "https://evil.example"}, {"attacker.example", ""}} {
		r := httptest.NewRequest(http.MethodGet, "http://adapter.local/v1/status", nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("host=%q origin=%q status=%d, want 403", tc.host, tc.origin, w.Code)
		}
	}
}
