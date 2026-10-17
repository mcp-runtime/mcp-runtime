//go:build darwin || linux

package adapterservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Request uses only the protected per-user Unix socket, never TCP or a proxy.
func (s Store) Request(ctx context.Context, method, path string) (Status, error) {
	if err := CheckPrivate(s.Dir, true); err != nil {
		return Status{}, err
	}
	st, err := os.Lstat(s.Socket())
	if err != nil {
		return Status{}, errors.New("adapter daemon is stopped; run adapter service start")
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0077 != 0 {
		return Status{}, errors.New("unsafe adapter control socket")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.Socket())
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	r, err := http.NewRequestWithContext(ctx, method, "http://adapter.local"+path, nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := client.Do(r)
	if err != nil {
		return Status{}, errors.New("adapter control connection unavailable; inspect service status")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Status{}, fmt.Errorf("adapter control request failed (%d); check profiles and protocol version", resp.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err != nil {
		return Status{}, errors.New("invalid adapter control response")
	}
	if status.Version != ProtocolVersion {
		return Status{}, errors.New("app/daemon protocol mismatch; update both app and CLI")
	}
	return status, nil
}

func (s Store) LaunchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", s.Label()+".plist")
}

func (s Store) Installed() bool {
	st, err := os.Lstat(s.LaunchAgentPath())
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0077 == 0
}
