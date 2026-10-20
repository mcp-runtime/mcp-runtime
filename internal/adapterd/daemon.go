package adapterd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mcp-runtime/internal/agentadapter"
)

// Material is certificate PEM stored for one profile.
type Material struct {
	CertPEM []byte
	KeyPEM  []byte
	CA      []byte
	Expires time.Time
}

// AddProfileRequest creates one profile. LocalToken is returned once.
type AddProfileRequest struct {
	Server      string `json:"server"`
	Namespace   string `json:"namespace,omitempty"`
	Team        string `json:"team,omitempty"`
	Agent       string `json:"agent,omitempty"`
	PlatformURL string `json:"platformUrl,omitempty"`
	RuntimeURL  string `json:"runtimeUrl"`
}

// Hooks let the CLI enroll and renew without the daemon importing the CLI.
type Hooks struct {
	Enroll func(ctx context.Context, p Profile) (Material, error)
}

// Status is the machine-readable daemon snapshot.
type Status struct {
	Protocol       int             `json:"protocol"`
	ProcessRunning bool            `json:"processRunning"`
	ListenAddr     string          `json:"listenAddr"`
	ListenerReady  bool            `json:"listenerReady"`
	CheckedAt      time.Time       `json:"checkedAt"`
	StartupAtLogin bool            `json:"startupAtLogin"`
	Profiles       []ProfileStatus `json:"profiles"`
}

// ProfileStatus separates local readiness from upstream and enrollment state.
type ProfileStatus struct {
	ID                string     `json:"id"`
	Slug              string     `json:"slug"`
	Server            string     `json:"server"`
	Team              string     `json:"team,omitempty"`
	Agent             string     `json:"agent,omitempty"`
	PlatformURL       string     `json:"platformUrl,omitempty"`
	RuntimeURL        string     `json:"runtimeUrl,omitempty"`
	Namespace         string     `json:"namespace,omitempty"`
	LocalURL          string     `json:"localUrl"`
	UpstreamReachable *bool      `json:"upstreamReachable"`
	UpstreamCheckedAt time.Time  `json:"upstreamCheckedAt,omitempty"`
	AuthState         string     `json:"authState"`
	CertExpires       *time.Time `json:"certExpires,omitempty"`
	CertNotBefore     *time.Time `json:"certNotBefore,omitempty"`
	CertSPIFFE        string     `json:"certSpiffe,omitempty"`
	RenewalError      string     `json:"renewalError,omitempty"`
	LastError         string     `json:"lastError,omitempty"`
}

// Daemon supervises one loopback listener and many profile paths.
type Daemon struct {
	ListenAddr string
	StateDir   string
	SocketPath string
	Store      *Store
	Secrets    SecretStore
	Hooks      Hooks
	Router     *Router
	Logs       *LogBuffer

	mu             sync.Mutex
	cancel         context.CancelFunc
	listenerReady  bool
	startupAtLogin bool
}

func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	defer cancel()
	if d.Router == nil {
		d.Router = NewRouter()
	}
	if d.Logs == nil {
		d.Logs = NewLogBuffer(200)
	}
	if d.ListenAddr == "" {
		d.ListenAddr = DefaultListenAddr
	}
	if err := d.reload(ctx); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", d.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.ListenAddr, err)
	}
	d.mu.Lock()
	d.listenerReady = true
	d.mu.Unlock()
	srv := &http.Server{Handler: d.Router, ReadHeaderTimeout: readHeaderTimeout}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	ctrl := &Control{Daemon: d, RequirePeer: true, SocketPath: d.SocketPath}
	go func() {
		if err := ctrl.Serve(ctx); err != nil && ctx.Err() == nil {
			d.Logs.Add("control socket: " + err.Error())
		}
	}()
	go d.probeLoop(ctx)
	go d.renewLoop(ctx)
	select {
	case <-ctx.Done():
		shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShut()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (d *Daemon) Status() Status {
	d.mu.Lock()
	ready := d.listenerReady
	login := d.startupAtLogin
	d.mu.Unlock()
	now := time.Now().UTC()
	items, _ := d.Store.List()
	routes := map[string]Route{}
	if d.Router != nil {
		for _, route := range d.Router.Routes() {
			routes[route.Profile.Slug] = route
		}
	}
	out := make([]ProfileStatus, 0, len(items))
	for _, p := range items {
		st := ProfileStatus{
			ID: p.ID, Slug: p.Slug, Server: p.Server, Namespace: p.Namespace, Team: p.Team, Agent: p.Agent,
			PlatformURL: p.PlatformURL, RuntimeURL: p.RuntimeURL, LocalURL: p.LocalURL(d.ListenAddr),
			AuthState: p.AuthState, RenewalError: p.RenewalError, LastError: p.LastError,
		}
		if !p.CertExpires.IsZero() {
			exp := p.CertExpires
			st.CertExpires = &exp
		}
		if d.Secrets != nil {
			if pemText, err := d.Secrets.Get(certAccount(p.ID)); err == nil {
				if notBefore, notAfter, spiffe := certSummary(pemText); spiffe != "" || !notAfter.IsZero() {
					st.CertSPIFFE = spiffe
					if !notBefore.IsZero() {
						st.CertNotBefore = &notBefore
					}
					if !notAfter.IsZero() {
						st.CertExpires = &notAfter
					}
				}
			}
		}
		if route, ok := routes[p.Slug]; ok {
			st.UpstreamReachable = route.UpstreamReachable
			st.UpstreamCheckedAt = route.UpstreamCheckedAt
		}
		out = append(out, st)
	}
	return Status{
		Protocol: ProtocolVersion, ProcessRunning: true, ListenAddr: d.ListenAddr,
		ListenerReady: ready, CheckedAt: now, StartupAtLogin: login, Profiles: out,
	}
}

func (d *Daemon) AddProfile(ctx context.Context, req AddProfileRequest) (Profile, string, error) {
	if strings.TrimSpace(req.Server) == "" || strings.TrimSpace(req.RuntimeURL) == "" {
		return Profile{}, "", fmt.Errorf("server and runtimeUrl are required")
	}
	base, err := SlugFromServer(req.Server)
	if err != nil {
		return Profile{}, "", err
	}
	items, err := d.Store.List()
	if err != nil {
		return Profile{}, "", err
	}
	slug := UniqueSlug(base, func(s string) bool {
		for _, item := range items {
			if item.Slug == s {
				return true
			}
		}
		return false
	})
	id := newID()
	token := newID()
	p := Profile{
		ID: id, Slug: slug, Server: req.Server, Namespace: req.Namespace, Team: req.Team,
		Agent: req.Agent, PlatformURL: req.PlatformURL, RuntimeURL: req.RuntimeURL,
		CreatedAt: time.Now().UTC(), AuthState: AuthNotEnrolled,
	}
	if err := d.Secrets.Put(tokenAccount(id), token); err != nil {
		return Profile{}, "", err
	}
	p = d.enroll(ctx, p)
	if err := d.Store.Put(p); err != nil {
		return Profile{}, "", err
	}
	if err := d.mount(ctx, p); err != nil {
		d.Logs.Add("mount " + p.Slug + ": " + redact(err.Error()))
	}
	return p, token, nil
}

func (d *Daemon) DeleteProfile(id string, purge bool) error {
	items, err := d.Store.List()
	if err != nil {
		return err
	}
	var slug string
	for _, item := range items {
		if item.ID == id {
			slug = item.Slug
		}
	}
	if slug == "" {
		return fmt.Errorf("profile %s not found", id)
	}
	if d.Router != nil {
		d.Router.Delete(slug)
	}
	if err := d.Store.Delete(id); err != nil {
		return err
	}
	if purge {
		for _, account := range []string{tokenAccount(id), certAccount(id), keyAccount(id), caAccount(id)} {
			_ = d.Secrets.Delete(account)
		}
	}
	return nil
}

func (d *Daemon) RestartProfile(ctx context.Context, id string) error {
	items, err := d.Store.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == id {
			item = d.enroll(ctx, item)
			if err := d.Store.Put(item); err != nil {
				return err
			}
			return d.mount(ctx, item)
		}
	}
	return fmt.Errorf("profile %s not found", id)
}

func (d *Daemon) enroll(ctx context.Context, p Profile) Profile {
	if d.Hooks.Enroll == nil {
		return p
	}
	material, err := d.Hooks.Enroll(ctx, p)
	if err != nil {
		p.AuthState = AuthSignInRequired
		p.LastError = redact(err.Error())
		if p.CertExpires.After(time.Now()) {
			p.AuthState = AuthRenewalFailed
			p.RenewalError = p.LastError
		}
		d.Logs.Add("enroll " + p.Slug + ": " + p.LastError)
		return p
	}
	if err := d.storeMaterial(p.ID, material); err != nil {
		p.AuthState = AuthRenewalFailed
		p.RenewalError = redact(err.Error())
		d.Logs.Add("enroll " + p.Slug + ": " + p.RenewalError)
		return p
	}
	p.AuthState = AuthEnrolled
	p.CertExpires = material.Expires
	p.LastError = ""
	p.RenewalError = ""
	d.Logs.Add("enroll " + p.Slug + ": certificate stored")
	return p
}

func (d *Daemon) LogTail(n int) []string { return d.Logs.Tail(n) }

func certSummary(certPEM string) (notBefore, notAfter time.Time, spiffe string) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}, time.Time{}, ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, time.Time{}, ""
	}
	for _, uri := range cert.URIs {
		if uri != nil && uri.Scheme == "spiffe" {
			spiffe = uri.String()
			break
		}
	}
	return cert.NotBefore.UTC(), cert.NotAfter.UTC(), spiffe
}

func (d *Daemon) reload(ctx context.Context) error {
	items, err := d.Store.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := d.mount(ctx, item); err != nil {
			d.Logs.Add("mount " + item.Slug + ": " + redact(err.Error()))
		}
	}
	return nil
}

func (d *Daemon) mount(ctx context.Context, p Profile) error {
	token, err := d.Secrets.Get(tokenAccount(p.ID))
	if err != nil {
		return err
	}
	var handler http.Handler
	certPEM, certErr := d.Secrets.Get(certAccount(p.ID))
	keyPEM, keyErr := d.Secrets.Get(keyAccount(p.ID))
	caPEM, _ := d.Secrets.Get(caAccount(p.ID))
	if certErr == nil && keyErr == nil {
		h, err := proxyHandler(p.RuntimeURL, []byte(certPEM), []byte(keyPEM), []byte(caPEM))
		if err != nil {
			return err
		}
		handler = h
	}
	if d.Router == nil {
		d.Router = NewRouter()
	}
	d.Router.Set(Route{Profile: p, Token: token, Handler: handler})
	_ = ctx
	return nil
}

func (d *Daemon) storeMaterial(id string, m Material) error {
	if err := d.Secrets.Put(certAccount(id), string(m.CertPEM)); err != nil {
		return err
	}
	if err := d.Secrets.Put(keyAccount(id), string(m.KeyPEM)); err != nil {
		return err
	}
	return d.Secrets.Put(caAccount(id), string(m.CA))
}

func (d *Daemon) probeLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	d.probe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.probe()
		}
	}
}

func (d *Daemon) probe() {
	if d.Router == nil {
		return
	}
	for _, route := range d.Router.Routes() {
		ok := dialUpstream(route.Profile.RuntimeURL)
		reachable := ok
		route.UpstreamReachable = &reachable
		route.UpstreamCheckedAt = time.Now().UTC()
		d.Router.Set(route)
	}
}

func (d *Daemon) renewLoop(ctx context.Context) {
	if d.Hooks.Enroll == nil {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.renewAll(ctx)
		}
	}
}

func (d *Daemon) renewAll(ctx context.Context) {
	items, err := d.Store.List()
	if err != nil {
		return
	}
	for _, p := range items {
		if p.CertExpires.IsZero() || time.Until(p.CertExpires) > 5*time.Minute {
			continue
		}
		material, err := d.Hooks.Enroll(ctx, p)
		if err != nil {
			p.AuthState = AuthRenewalFailed
			p.RenewalError = redact(err.Error())
			if strings.Contains(strings.ToLower(err.Error()), "sign") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") || strings.Contains(strings.ToLower(err.Error()), "revok") {
				p.AuthState = AuthSignInRequired
			}
			_ = d.Store.Put(p)
			d.Logs.Add("renew " + p.Slug + ": " + p.RenewalError)
			continue
		}
		if err := d.storeMaterial(p.ID, material); err != nil {
			p.RenewalError = redact(err.Error())
			_ = d.Store.Put(p)
			continue
		}
		p.AuthState = AuthEnrolled
		p.CertExpires = material.Expires
		p.RenewalError = ""
		_ = d.Store.Put(p)
		_ = d.mount(ctx, p)
	}
}

func proxyHandler(runtimeURL string, certPEM, keyPEM, caPEM []byte) (http.Handler, error) {
	parsed, err := url.Parse(runtimeURL)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if len(caPEM) > 0 {
		_ = caPEM
	}
	transport := &agentadapter.RuntimeTransport{Base: agentadapter.NewHTTPTransportWithTLS(tlsCfg)}
	return agentadapter.NewHTTPProxyHandler(agentadapter.ProxyConfig{
		RuntimeURL:          parsed,
		Transport:           transport,
		CertificateIdentity: true,
		ListenAddr:          DefaultListenAddr,
	})
}

func dialUpstream(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func redact(msg string) string {
	msg = bearerRE(msg)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func bearerRE(msg string) string {
	lower := strings.ToLower(msg)
	if i := strings.Index(lower, "bearer "); i >= 0 {
		return msg[:i] + "bearer [redacted]"
	}
	return msg
}

// LogBuffer keeps a bounded list of redacted lines.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewLogBuffer(max int) *LogBuffer { return &LogBuffer{max: max} }

func (l *LogBuffer) Add(line string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, time.Now().UTC().Format(time.RFC3339)+" "+redact(line))
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
}

func (l *LogBuffer) Tail(n int) []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.lines) {
		n = len(l.lines)
	}
	out := make([]string, n)
	copy(out, l.lines[len(l.lines)-n:])
	return out
}

func (d *Daemon) SetStartupAtLogin(v bool) {
	d.mu.Lock()
	d.startupAtLogin = v
	d.mu.Unlock()
}
