//go:build darwin || linux

// Package adapterservice provides the per-user adapter daemon and its protected
// local management protocol. Each profile owns one fixed platform session.
package adapterservice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

const ProtocolVersion = 1
const LocalCredentialHeader = "X-MCP-Local-Token"

// Profile is stored only in the owner-protected store, never returned as status.
// PlatformToken authenticates certificate renewal, LocalToken authenticates the
// local MCP client. Neither is an upstream OAuth bearer.
type Profile struct {
	Name           string    `json:"name"`
	PlatformURL    string    `json:"platform_url"`
	RuntimeURL     string    `json:"runtime_url"`
	Namespace      string    `json:"namespace"`
	Server         string    `json:"server"`
	Agent          string    `json:"agent"`
	Human          string    `json:"human"`
	Team           string    `json:"team"`
	Session        string    `json:"session"`
	TrustDomain    string    `json:"trust_domain"`
	SessionExpires time.Time `json:"session_expires"`
	Listen         string    `json:"listen"`
	PlatformToken  string    `json:"platform_token"`
	LocalToken     string    `json:"local_token"`
}

type Store struct{ Dir string }

func DefaultStore() (Store, error) {
	home, err := os.UserHomeDir()
	return Store{Dir: filepath.Join(home, ".mcpruntime", "adapter")}, err
}

func Token() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// CheckPrivate refuses symlinks, foreign owners and group/world access rather
// than silently adopting an existing path with unsafe permissions.
func CheckPrivate(path string, dir bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || st.Mode()&os.ModeSymlink != 0 || st.IsDir() != dir || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("adapter path must be owner-only and owned by this user: %s", path)
	}
	if !dir && !st.Mode().IsRegular() {
		return errors.New("adapter store must contain regular files")
	}
	return nil
}

func (s Store) Init() error {
	if !filepath.IsAbs(s.Dir) {
		return errors.New("adapter state directory must be absolute")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	return CheckPrivate(s.Dir, true)
}

func (s Store) Socket() string { return filepath.Join(s.Dir, "control.sock") }

func (s Store) Load() ([]Profile, error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, "profiles.json")
	if err := CheckPrivate(path, false); err != nil {
		if os.IsNotExist(err) {
			return []Profile{}, nil
		}
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var profiles []Profile
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&profiles); err != nil {
		return nil, errors.New("invalid adapter profile store")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing data in adapter store")
	}
	if err := ValidateProfiles(profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (s Store) Save(profiles []Profile) error {
	if err := ValidateProfiles(profiles); err != nil {
		return err
	}
	if err := s.Init(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".profiles-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.Dir, "profiles.json"))
}

var profileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ValidateProfiles(profiles []Profile) error {
	if len(profiles) > 32 {
		return errors.New("at most 32 adapter profiles are supported")
	}
	names, listeners := map[string]bool{}, map[string]bool{}
	for _, p := range profiles {
		if !profileName.MatchString(p.Name) || names[p.Name] {
			return errors.New("invalid or duplicate adapter profile name")
		}
		names[p.Name] = true
		for _, raw := range []string{p.PlatformURL, p.RuntimeURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("profile URLs must use HTTPS without credentials, query or fragment")
			}
		}
		host, port, err := net.SplitHostPort(p.Listen)
		n, numErr := strconv.Atoi(port)
		// A fixed IPv4 loopback address makes collisions and client setup unambiguous.
		if err != nil || host != "127.0.0.1" || numErr != nil || n < 1024 || n > 65535 || listeners[p.Listen] {
			return errors.New("each profile needs a unique 127.0.0.1 port between 1024 and 65535")
		}
		listeners[p.Listen] = true
		if p.Namespace == "" || p.Server == "" || p.Agent == "" || p.Human == "" || p.Session == "" || p.TrustDomain == "" || p.SessionExpires.IsZero() || p.PlatformToken == "" || len(p.LocalToken) != 64 {
			return errors.New("profile identity, consent and credentials are required; authorize the profile interactively")
		}
		if _, err := hex.DecodeString(p.LocalToken); err != nil {
			return errors.New("invalid local client credential")
		}
	}
	return nil
}

// Lock serializes daemon starts and interactive profile changes across processes.
func (s Store) Lock(name string) (*os.File, error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, name+".lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := CheckPrivate(path, false); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("adapter operation already running")
	}
	return f, nil
}
