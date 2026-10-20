package adapterd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
