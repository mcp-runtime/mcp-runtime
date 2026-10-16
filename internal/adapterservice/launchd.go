//go:build darwin || linux

package adapterservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

func (s Store) Label() string {
	digest := sha256.Sum256([]byte(filepath.Clean(s.Dir)))
	return fmt.Sprintf("org.mcpruntime.adapter.%x", digest[:6])
}

func (s Store) serviceTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + s.Label() }

func escapeXML(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func (s Store) Plist(executable string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + escapeXML(s.Label()) + `</string>
<key>ProgramArguments</key><array><string>` + escapeXML(executable) + `</string><string>adapter</string><string>--state-dir</string><string>` + escapeXML(s.Dir) + `</string><string>service</string><string>run</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>30</integer>
<key>ExitTimeOut</key><integer>15</integer>
<key>Umask</key><integer>63</integer>
<key>ProcessType</key><string>Background</string>
</dict></plist>
`)
}

func (s Store) launchctl(ctx context.Context, args ...string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("adapter user service installation requires macOS")
	}
	// Arguments are structured, with no shell expansion or credentials.
	if err := exec.CommandContext(ctx, "/bin/launchctl", args...).Run(); err != nil {
		return fmt.Errorf("launchctl %s failed; run from the signed-in macOS desktop session: %w", args[0], err)
	}
	return nil
}

// Install places a stable daemon binary outside the source checkout. Replace
// that binary only after stopping the previous service. Profiles are preserved.
func (s Store) Install(ctx context.Context, executable string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("adapter user service installation requires macOS")
	}
	if err := s.Init(); err != nil {
		return err
	}
	if s.Installed() {
		if err := s.Stop(ctx); err != nil {
			return err
		}
	}
	in, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(s.Dir, ".binary-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if err := out.Chmod(0700); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	managed := filepath.Join(s.Dir, "mcp-runtime")
	if err := os.Rename(out.Name(), managed); err != nil {
		return err
	}
	path := s.LaunchAgentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := CheckPrivate(path, false); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".adapter-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(s.Plist(managed)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return s.Start(ctx)
}

func (s Store) Start(ctx context.Context) error {
	if !s.Installed() {
		return errors.New("adapter service is not installed; run adapter service install")
	}
	if _, err := s.Request(ctx, "GET", "/v1/status"); err == nil {
		return nil
	}
	if err := s.launchctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), s.LaunchAgentPath()); err != nil {
		// A crash-throttled job can still be loaded. Kick only this managed label.
		if err := s.launchctl(ctx, "kickstart", s.serviceTarget()); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := s.Request(ctx, "GET", "/v1/status"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("adapter service did not become ready; inspect profile permissions and listener conflicts")
		case <-ticker.C:
		}
	}
}

func (s Store) Stop(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return errors.New("adapter service lifecycle requires macOS")
	}
	// print distinguishes an already-unloaded job from a bootout failure.
	if exec.CommandContext(ctx, "/bin/launchctl", "print", s.serviceTarget()).Run() != nil {
		if _, err := s.Request(ctx, "GET", "/v1/status"); err == nil {
			return errors.New("a foreground daemon is running; stop it in its owning terminal")
		}
		return nil
	}
	if err := s.launchctl(ctx, "bootout", s.serviceTarget()); err != nil {
		return err
	}
	// Wait for the flock, not just socket removal: proxies must finish draining.
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := s.Lock("daemon")
		if err == nil {
			lock.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("adapter daemon has not stopped")
		case <-ticker.C:
		}
	}
}

func (s Store) Uninstall(ctx context.Context, purge bool) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	path := s.LaunchAgentPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Do not recursively delete user-controlled directories or arbitrary paths.
	names := []string{"mcp-runtime"}
	if purge {
		names = append(names, "profiles.json")
	}
	for _, name := range names {
		if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
