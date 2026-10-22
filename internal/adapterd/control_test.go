package adapterd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControlRejectsForeignUID(t *testing.T) {
	dir := t.TempDir()
	d := testDaemon(dir)
	c := &Control{Daemon: d, RequirePeer: true}
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), peerUIDKey{}, os.Getuid()+1))
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthRequirementPersistsAndAppearsInStatus(t *testing.T) {
	for _, value := range []bool{true, false} {
		d := testDaemon(t.TempDir())
		d.Hooks.Enroll = func(context.Context, Profile) (Material, error) {
			return Material{OAuthRequired: &value, Namespace: "resolved-team"}, nil
		}
		p := d.enroll(context.Background(), Profile{ID: "profile-1", Server: "demo", Slug: "demo"})
		if p.Namespace != "resolved-team" {
			t.Fatal("lost resolved namespace")
		}
		if err := d.Store.Put(p); err != nil {
			t.Fatal(err)
		}
		stored, err := NewStore(d.Store.path).List()
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != 1 || stored[0].OAuthRequired == nil || *stored[0].OAuthRequired != value {
			t.Fatalf("OAuth requirement lost on reload: %+v", stored)
		}
		status := d.Status().Profiles[0]
		if status.OAuthRequired == nil || *status.OAuthRequired != value {
			t.Fatal("OAuth requirement lost in status")
		}
		// Unknown metadata must remain distinct from a known false value.
		d.Hooks.Enroll = func(context.Context, Profile) (Material, error) { return Material{}, nil }
		p = d.enroll(context.Background(), p)
		if p.OAuthRequired != nil {
			t.Fatal("kept a stale OAuth requirement after an unknown lookup")
		}
	}
}

func TestAddProfileKeepsSeparatePaths(t *testing.T) {
	dir := t.TempDir()
	d := testDaemon(dir)
	ctx := context.Background()
	first, _, err := d.AddProfile(ctx, AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/cully/mcp", Agent: "agt_a"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := d.AddProfile(ctx, AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/other/mcp", Agent: "agt_b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Slug != "cully" || second.Slug != "cully-2" {
		t.Fatalf("slugs %s %s", first.Slug, second.Slug)
	}
	if err := d.DeleteProfile(first.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Secrets.Get(tokenAccount(first.ID)); err != nil {
		t.Fatalf("token removed without purge: %v", err)
	}
	if err := d.DeleteProfile(second.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Secrets.Get(tokenAccount(second.ID)); err == nil {
		t.Fatal("purge kept the local token")
	}
}

func TestConcurrentProfilesReceiveUniqueSlugs(t *testing.T) {
	d := testDaemon(t.TempDir())
	const count = 8
	profiles := make(chan Profile, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			profile, _, err := d.AddProfile(context.Background(), AddProfileRequest{
				Server: "shared", RuntimeURL: "https://mcp.example/shared/mcp",
			})
			if err != nil {
				errs <- err
				return
			}
			profiles <- profile
		}()
	}
	wg.Wait()
	close(profiles)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for profile := range profiles {
		if slugs[profile.Slug] {
			t.Fatalf("duplicate profile slug %q", profile.Slug)
		}
		slugs[profile.Slug] = true
	}
	if len(slugs) != count {
		t.Fatalf("got %d unique profile slugs, want %d", len(slugs), count)
	}
}

func TestClientConfigIsAvailableOnlyThroughManagementAPI(t *testing.T) {
	dir := t.TempDir()
	d := testDaemon(dir)
	profile, token, err := d.AddProfile(context.Background(), AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/cully/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	c := &Control{Daemon: d, RequirePeer: false}
	req := httptest.NewRequest(http.MethodGet, "/v1/profiles/"+profile.ID+"/client-config", nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("client config status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	server, ok := got.MCPServers[profile.Server]
	if !ok || server.URL != "http://"+DefaultListenAddr+"/cully/mcp" || server.Headers[LocalTokenHeader] != token {
		t.Fatalf("client config = %+v", got)
	}
	if strings.Contains(rec.Body.String(), profile.RuntimeURL) {
		t.Fatal("client config exposed the upstream URL")
	}
}

func TestRestartProfileEnrollsAgain(t *testing.T) {
	dir := t.TempDir()
	d := testDaemon(dir)
	d.Hooks.Enroll = func(context.Context, Profile) (Material, error) {
		return Material{}, io.EOF
	}
	ctx := context.Background()
	created, _, err := d.AddProfile(ctx, AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/cully/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if created.AuthState != AuthSignInRequired {
		t.Fatalf("auth %s", created.AuthState)
	}
	if len(d.LogTail(10)) == 0 {
		t.Fatal("enroll failure was not logged")
	}
	d.Hooks.Enroll = func(context.Context, Profile) (Material, error) {
		return Material{CertPEM: []byte("cert"), KeyPEM: []byte("key"), CA: []byte("ca"), Expires: time.Now().Add(time.Hour)}, nil
	}
	if err := d.RestartProfile(ctx, created.ID); err == nil {
		t.Fatal("expected the test certificate to fail proxy setup")
	}
	items, err := d.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AuthState != AuthEnrolled {
		t.Fatalf("profile %+v", items)
	}
}

func TestReconnectStartsFreshSessionAfterExpiry(t *testing.T) {
	d := testDaemon(t.TempDir())
	profile, _, err := d.AddProfile(context.Background(), AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/cully/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	profile.Session = "expired-session"
	profile.SessionExpires = time.Now().Add(-time.Minute)
	profile.TrustDomain = "old.example"
	profile.AuthState = AuthSignInRequired
	if err := d.Store.Put(profile); err != nil {
		t.Fatal(err)
	}
	var sawUnboundSession bool
	d.Hooks.Enroll = func(_ context.Context, got Profile) (Material, error) {
		sawUnboundSession = got.Session == "" && got.SessionExpires.IsZero() && got.TrustDomain == ""
		return Material{}, io.EOF
	}
	if err := d.RestartProfile(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	if !sawUnboundSession {
		t.Fatal("reconnect attempted to reuse the expired session")
	}
}

func TestBackgroundRenewalNeverCreatesANewSession(t *testing.T) {
	d := testDaemon(t.TempDir())
	profile, _, err := d.AddProfile(context.Background(), AddProfileRequest{Server: "cully", RuntimeURL: "https://mcp.example/cully/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	profile.AuthState = AuthEnrolled
	profile.CertExpires = time.Now().Add(time.Minute)
	profile.Session = "existing-session"
	profile.SessionExpires = time.Now().Add(4 * time.Minute)
	if err := d.Store.Put(profile); err != nil {
		t.Fatal(err)
	}
	called := false
	d.Hooks.Enroll = func(context.Context, Profile) (Material, error) {
		called = true
		return Material{}, nil
	}
	d.renewAll(context.Background())
	if called {
		t.Fatal("background renewal created a new session without interactive authorization")
	}
	items, err := d.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AuthState != AuthSignInRequired {
		t.Fatalf("profile status after renewal = %+v", items)
	}
}

func TestSocketStatusAllowsSameUser(t *testing.T) {
	dir, mkErr := os.MkdirTemp("/tmp", "ad")
	if mkErr != nil {
		t.Fatal(mkErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := testDaemon(dir)
	d.listenerReady = true
	d.SocketPath = filepath.Join(dir, "adapterd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Control{Daemon: d, RequirePeer: true, SocketPath: d.SocketPath}
	errCh := make(chan error, 1)
	go func() { errCh <- c.Serve(ctx) }()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", d.SocketPath)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /v1/status HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	buf, _ := io.ReadAll(conn)
	body := string(buf)
	if !strings.Contains(body, `"protocol": 1`) {
		t.Fatalf("status response %s serve %v", body, errCh)
	}
}

func TestRedactBearer(t *testing.T) {
	if got := redact("denied bearer secret-value"); strings.Contains(got, "secret-value") {
		t.Fatalf("redact kept secret: %s", got)
	}
}

func testDaemon(dir string) *Daemon {
	return &Daemon{
		ListenAddr: DefaultListenAddr,
		Store:      NewStore(filepath.Join(dir, "profiles.json")),
		Secrets:    NewFileSecretStore(filepath.Join(dir, "secrets.json")),
		Router:     NewRouter(),
		Logs:       NewLogBuffer(10),
	}
}
