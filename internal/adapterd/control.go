package adapterd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const readHeaderTimeout = 5 * time.Second

type peerUIDKey struct{}

// Control serves the owner-only management API.
type Control struct {
	Daemon      *Daemon
	RequirePeer bool
	SocketPath  string
}

func (c *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", c.status)
	mux.HandleFunc("GET /v1/logs", c.logs)
	mux.HandleFunc("POST /v1/profiles", c.addProfile)
	mux.HandleFunc("GET /v1/profiles/{id}/client-config", c.clientConfig)
	mux.HandleFunc("DELETE /v1/profiles/{id}", c.deleteProfile)
	mux.HandleFunc("POST /v1/profiles/{id}/restart", c.restartProfile)
	mux.HandleFunc("POST /v1/shutdown", c.shutdown)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.authorize(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *Control) authorize(ctx context.Context) error {
	if !c.RequirePeer {
		return nil
	}
	uid, ok := ctx.Value(peerUIDKey{}).(int)
	if !ok || uid != os.Getuid() {
		return errors.New("management client is not the socket owner")
	}
	return nil
}

func (c *Control) Serve(ctx context.Context) error {
	if err := os.MkdirAll(dirOf(c.SocketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(c.SocketPath)
	ln, err := net.Listen("unix", c.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(c.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           c.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if uid, ok := uidFromConn(conn); ok {
				return context.WithValue(ctx, peerUIDKey{}, uid)
			}
			return ctx
		},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (c *Control) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, c.Daemon.Status())
}

func (c *Control) logs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lines": c.Daemon.LogTail(80)})
}

func (c *Control) addProfile(w http.ResponseWriter, r *http.Request) {
	var req AddProfileRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid profile", http.StatusBadRequest)
		return
	}
	created, token, err := c.Daemon.AddProfile(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"profile":    created,
		"localUrl":   created.LocalURL(c.Daemon.ListenAddr),
		"localToken": token,
	})
}

func (c *Control) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := c.Daemon.DeleteProfile(r.PathValue("id"), r.URL.Query().Get("purge") == "true"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Control) clientConfig(w http.ResponseWriter, r *http.Request) {
	profile, token, err := c.Daemon.ClientConfig(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mcpServers": map[string]any{
			profile.Server: map[string]any{
				"url":     profile.LocalURL(c.Daemon.ListenAddr),
				"headers": map[string]string{LocalTokenHeader: token},
			},
		},
	})
}

func (c *Control) restartProfile(w http.ResponseWriter, r *http.Request) {
	if err := c.Daemon.RestartProfile(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, c.Daemon.Status())
}

func (c *Control) shutdown(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if c.Daemon.cancel != nil {
		go c.Daemon.cancel()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
