//go:build darwin || linux

package adapterservice

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"mcp-runtime/internal/agentadapter"
)

type Credential struct {
	Certificate tls.Certificate
	Roots       *x509.CertPool
	Expires     time.Time
}

// Issue must renew only the profile's recorded session, never create a session.
type Issue func(context.Context, Profile) (Credential, error)

type Event struct {
	At      time.Time `json:"at"`
	Profile string    `json:"profile"`
	Message string    `json:"message"`
}

type ProfileStatus struct {
	Name               string    `json:"name"`
	PlatformURL        string    `json:"platform_url"`
	Namespace          string    `json:"namespace"`
	Server             string    `json:"server"`
	Agent              string    `json:"agent"`
	Human              string    `json:"human"`
	Team               string    `json:"team"`
	Endpoint           string    `json:"endpoint"`
	LocalReady         bool      `json:"local_ready"`
	Upstream           string    `json:"upstream"`
	Authentication     string    `json:"authentication"`
	CertificateExpires time.Time `json:"certificate_expires"`
	SessionExpires     time.Time `json:"session_expires"`
	LastCheck          time.Time `json:"last_check"`
	LastRenewalError   string    `json:"last_renewal_error,omitempty"`
}

type Status struct {
	Version      int             `json:"version"`
	Running      bool            `json:"running"`
	StartAtLogin bool            `json:"start_at_login"`
	Profiles     []ProfileStatus `json:"profiles"`
	Events       []Event         `json:"events"`
}

type worker struct {
	p         Profile
	mu        sync.Mutex
	status    ProfileStatus
	cancel    context.CancelFunc
	done      chan struct{}
	transport atomic.Pointer[http.Transport]
	expiry    atomic.Int64
}

type Daemon struct {
	Store   Store
	Issue   Issue
	mu      sync.Mutex
	workers map[string]*worker
	events  []Event
	// Control requests, including reload, are serialized independently of status.
	control sync.Mutex
	ctx     context.Context
}

func (d *Daemon) event(name, message string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, Event{At: time.Now().UTC(), Profile: name, Message: message})
	if len(d.events) > 100 {
		d.events = append([]Event(nil), d.events[len(d.events)-100:]...)
	}
}

func (d *Daemon) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := Status{Version: ProtocolVersion, Running: true, StartAtLogin: d.Store.Installed(), Profiles: []ProfileStatus{}, Events: append([]Event{}, d.events...)}
	for _, w := range d.workers {
		w.mu.Lock()
		st.Profiles = append(st.Profiles, w.status)
		w.mu.Unlock()
	}
	sort.Slice(st.Profiles, func(i, j int) bool { return st.Profiles[i].Name < st.Profiles[j].Name })
	return st
}

// reload validates the entire new store before cancelling any old profile. An
// identity change always tears down the old listener and transport first.
func (d *Daemon) reload() error {
	profiles, err := d.Store.Load()
	if err != nil {
		return err
	}
	d.mu.Lock()
	old := d.workers
	d.mu.Unlock()
	for _, w := range old {
		w.cancel()
	}
	for _, w := range old {
		<-w.done
	}
	next := map[string]*worker{}
	for _, p := range profiles {
		ctx, cancel := context.WithCancel(d.ctx)
		w := &worker{p: p, cancel: cancel, done: make(chan struct{}), status: ProfileStatus{
			Name: p.Name, PlatformURL: p.PlatformURL, Namespace: p.Namespace, Server: p.Server, Agent: p.Agent, Human: p.Human, Team: p.Team,
			Endpoint: "http://" + p.Listen + "/mcp", SessionExpires: p.SessionExpires, Authentication: "enrolling", Upstream: "unknown",
		}}
		next[p.Name] = w
		go d.runProfile(ctx, w)
	}
	d.mu.Lock()
	d.workers = next
	d.mu.Unlock()
	d.event("", "profiles loaded")
	return nil
}

func (d *Daemon) Run(ctx context.Context) error {
	lock, err := d.Store.Lock("daemon")
	if err != nil {
		return err
	}
	defer lock.Close()
	if len(d.Store.Socket()) > 100 {
		return errors.New("adapter state directory is too long for a Unix socket")
	}
	d.ctx = ctx
	// The exclusive daemon lock proves any remaining socket is stale. Never
	// remove a non-socket or follow a user-controlled symlink here.
	if st, err := os.Lstat(d.Store.Socket()); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("control socket path is occupied by a non-socket")
		}
		if err := os.Remove(d.Store.Socket()); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", d.Store.Socket())
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(d.Store.Socket(), 0600); err != nil {
		return err
	}
	if err := d.reload(); err != nil {
		return err
	}
	server := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err = <-done:
	}
	d.control.Lock()
	d.mu.Lock()
	workers := d.workers
	d.mu.Unlock()
	for _, w := range workers {
		w.cancel()
	}
	for _, w := range workers {
		<-w.done
	}
	d.control.Unlock()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (d *Daemon) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No ambient cookies, browser Origins or cross-site management calls.
		if r.Header.Get("Origin") != "" || r.Host != "adapter.local" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/status" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(d.Status())
		case r.URL.Path == "/v1/reload" && r.Method == http.MethodPost:
			d.control.Lock()
			defer d.control.Unlock()
			if err := d.reload(); err != nil {
				http.Error(w, "invalid profiles; current profiles retained", http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(d.Status())
		default:
			http.Error(w, "unsupported control request or protocol version", 404)
		}
	})
}

func (d *Daemon) runProfile(ctx context.Context, w *worker) {
	defer close(w.done)
	ln, err := net.Listen("tcp4", w.p.Listen)
	if err != nil {
		w.mu.Lock()
		w.status.Authentication = "listener_conflict"
		w.mu.Unlock()
		d.event(w.p.Name, "local port is unavailable; choose another profile port")
		return
	}
	defer ln.Close()
	target, _ := url.Parse(w.p.RuntimeURL) // validated before starting workers
	transport := &observedTransport{w: w, daemon: d}
	cfg := agentadapter.ProxyConfig{RuntimeURL: target, CertificateIdentity: true, DisableXForwarded: true,
		Transport: &agentadapter.RuntimeTransport{Base: transport}, LogWriter: io.Discard}
	proxy, err := agentadapter.NewHTTPProxyHandler(cfg)
	if err != nil {
		d.event(w.p.Name, "invalid proxy configuration")
		return
	}
	handler := w.protect(proxy)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }, ErrorLog: log.New(io.Discard, "", 0)}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()
	w.mu.Lock()
	w.status.LocalReady = true
	w.mu.Unlock()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		if t := w.transport.Load(); t != nil {
			t.CloseIdleConnections()
		}
	}()
	// Renewal failures back off to at most one attempt per minute. The request
	// path never retries a tool call, including after uncertain delivery.
	renew := time.NewTimer(0)
	defer renew.Stop()
	probe := time.NewTicker(30 * time.Second)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-served:
			d.event(w.p.Name, "local listener stopped unexpectedly")
			return
		case <-renew.C:
			if !time.Now().Before(w.p.SessionExpires) {
				w.mu.Lock()
				w.status.Authentication = "sign_in_required"
				w.status.LastRenewalError = "session expired; run adapter profile authorize"
				w.mu.Unlock()
				d.event(w.p.Name, "session expired; interactive authorization required")
				continue
			}
			credential, err := d.Issue(ctx, w.p)
			if err != nil || !credential.Expires.After(time.Now()) || credential.Expires.After(w.p.SessionExpires) {
				w.mu.Lock()
				w.status.LastRenewalError = "certificate renewal failed; check login, session and network"
				w.status.Authentication = "renewal_failed"
				w.mu.Unlock()
				d.event(w.p.Name, "certificate renewal failed; check login, session and network")
				renew.Reset(time.Minute)
				continue
			}
			tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: credential.Roots, Certificates: []tls.Certificate{credential.Certificate}}
			base := agentadapter.NewHTTPTransportWithTLS(tlsConfig)
			old := w.transport.Swap(base)
			if old != nil {
				old.CloseIdleConnections()
			}
			w.expiry.Store(credential.Expires.Unix())
			w.mu.Lock()
			w.status.CertificateExpires = credential.Expires
			w.status.LastRenewalError = ""
			w.status.Authentication = "certificate_ready"
			w.mu.Unlock()
			d.event(w.p.Name, "session certificate ready; upstream authorization remains unverified")
			delay := time.Until(credential.Expires) - 5*time.Minute
			if delay < 30*time.Second {
				delay = 30 * time.Second
			}
			renew.Reset(delay)
			w.probe(ctx, target)
		case <-probe.C:
			w.probe(ctx, target)
		}
	}
}

func (w *worker) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		// Reject browser origins and DNS rebinding, including requests that try to
		// smuggle a local credential via a hop-by-hop Connection header.
		if r.Host != w.p.Listen || r.Header.Get("Origin") != "" || len(r.Header.Values(LocalCredentialHeader)) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get(LocalCredentialHeader)), []byte(w.p.LocalToken)) != 1 {
			http.Error(out, "local client authentication required", http.StatusUnauthorized)
			return
		}
		r = r.Clone(r.Context())
		r.Header.Del(LocalCredentialHeader)
		if time.Now().Unix() >= w.expiry.Load() || !time.Now().Before(w.p.SessionExpires) {
			http.Error(out, "certificate or session unavailable; inspect adapter status", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(out, r)
	})
}

type observedTransport struct {
	w      *worker
	daemon *Daemon
}

func (t *observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.w.transport.Load()
	if base == nil || time.Now().Unix() >= t.w.expiry.Load() || !time.Now().Before(t.w.p.SessionExpires) {
		return nil, errors.New("adapter session unavailable")
	}
	// ReverseProxy preserves the client's OAuth header. The local credential was
	// stripped before proxying and is never treated as an upstream bearer.
	resp, err := base.RoundTrip(r)
	t.w.mu.Lock()
	t.w.status.LastCheck = time.Now().UTC()
	if err != nil {
		t.w.status.Upstream = "unreachable"
	} else {
		t.w.status.Upstream = "reachable"
		if resp.StatusCode == 401 {
			t.w.status.Authentication = "sign_in_required"
		} else if resp.StatusCode == 403 {
			t.w.status.Authentication = "denied"
		} else if resp.StatusCode < 400 {
			t.w.status.Authentication = "request_accepted"
		}
	}
	t.w.mu.Unlock()
	if err == nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		t.daemon.event(t.w.p.Name, "upstream denied a request; inspect login and grant")
	}
	return resp, err
}

// probe measures only TLS reachability. It never sends an MCP tool request and
// does not promote a successful handshake to authenticated/authorized status.
func (w *worker) probe(ctx context.Context, target *url.URL) {
	base := w.transport.Load()
	if base == nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: base.TLSClientConfig.Clone()}
	address := target.Host
	if target.Port() == "" {
		address = net.JoinHostPort(target.Hostname(), "443")
	}
	conn, err := dialer.DialContext(probeCtx, "tcp", address)
	if conn != nil {
		_ = conn.Close()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.LastCheck = time.Now().UTC()
	if err != nil {
		w.status.Upstream = "unreachable"
	} else {
		w.status.Upstream = "tls_reachable"
	}
	if !time.Now().Before(w.p.SessionExpires) || time.Now().Unix() >= w.expiry.Load() {
		w.status.Authentication = "sign_in_required"
	}
}
