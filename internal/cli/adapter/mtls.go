package adapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcp-runtime/internal/agentadapter"
	"mcp-runtime/internal/cli/platformapi"
)

// mtlsRefresher owns a session-bound client certificate and rotates it before
// the issuing session expires. The live *tls.Config reads the current cert via
// GetClientCertificate from an atomic holder, so rotation needs no transport
// rebuild: after storing a fresh cert the refresher drains idle connections so
// subsequent requests renegotiate with it. The previous certificate stays
// valid until its own expiry, which is always after the rotation fires
// (adapterRefreshLead ahead), so in-flight connections are never orphaned.
type mtlsRefresher struct {
	client      *platformapi.PlatformClient
	flags       platformSessionFlags
	trustDomain string
	sink        io.Writer

	cert      atomic.Pointer[tls.Certificate]
	transport *agentadapter.RuntimeTransport

	mu     sync.Mutex
	expiry time.Time
	cancel context.CancelFunc
	done   chan struct{}
}

// setupMTLS performs the first enrollment, builds the mTLS transport, and (when
// autoRefresh is set) starts the rotation loop. The returned transport carries
// the GetClientCertificate-backed TLS config; base supplies any Timeout or
// AuthHeader the caller already resolved from flags. The returned stop func is
// always safe to call, even when autoRefresh is false.
func setupMTLS(ctx context.Context, client *platformapi.PlatformClient, flags platformSessionFlags, trustDomain string, base *agentadapter.RuntimeTransport, autoRefresh bool, insecureSkipVerify bool, sink io.Writer) (*agentadapter.RuntimeTransport, func(), error) {
	cred, err := issueAdapterCredential(ctx, client, flags, trustDomain)
	if err != nil {
		return nil, nil, err
	}
	cert, err := tls.X509KeyPair(cred.CertPEM, cred.KeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("assemble client certificate: %w", err)
	}
	// RootCAs is fixed from the first enrollment: the bundle is the cluster's
	// stable CA, not the rotating leaf. A CA rotation would require a restart.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cred.CABundle) {
		return nil, nil, fmt.Errorf("issued CA bundle contains no valid certificates")
	}

	r := &mtlsRefresher{
		client:      client,
		flags:       flags,
		trustDomain: trustDomain,
		sink:        sink,
		expiry:      cred.ExpiresAt,
	}
	r.cert.Store(&cert)

	tlsCfg := &tls.Config{
		RootCAs: pool,
		// Kind Traefik port-forwards terminate with a local default cert whose
		// SAN is not localhost; opt-in only via --tls-insecure-skip-verify.
		InsecureSkipVerify: insecureSkipVerify, // #nosec G402 -- explicit Kind/local Traefik default-cert skip; client certs still presented
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return r.cert.Load(), nil
		},
	}
	transport := base
	if transport == nil {
		transport = &agentadapter.RuntimeTransport{}
	}
	transport.Base = agentadapter.NewHTTPTransportWithTLS(tlsCfg)
	r.transport = transport

	if sink != nil {
		fmt.Fprintf(sink, "mcp-runtime adapter: issued %s (expires %s)\n",
			cred.SPIFFEID, cred.ExpiresAt.Format(time.RFC3339))
	}
	if autoRefresh {
		r.start(ctx)
	}
	return transport, r.Stop, nil
}

func (r *mtlsRefresher) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.cancel = cancel
	r.done = make(chan struct{})
	r.mu.Unlock()
	go r.loop(ctx)
}

// Stop cancels the rotation loop and waits for it to return. Safe to call
// multiple times and on a refresher whose loop never started.
func (r *mtlsRefresher) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (r *mtlsRefresher) loop(ctx context.Context) {
	defer close(r.done)
	for {
		r.mu.Lock()
		expiry := r.expiry
		r.mu.Unlock()
		wait := time.Until(expiry) - adapterRefreshLead
		if wait < adapterRefreshFloor {
			wait = adapterRefreshFloor
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := r.rotate(ctx); err != nil {
			// Keep the current cert; it is valid until the real expiry. Retry on
			// the next tick (which floors to adapterRefreshFloor once near expiry).
			if r.sink != nil {
				fmt.Fprintf(r.sink, "mcp-runtime adapter: certificate refresh failed: %v\n", err)
			}
			continue
		}
	}
}

// rotate re-enrolls a fresh session-bound certificate, swaps it into the atomic
// holder, and drains idle connections so the next request renegotiates with it.
// On error the previous certificate is left in place untouched.
func (r *mtlsRefresher) rotate(ctx context.Context) error {
	cred, err := issueAdapterCredential(ctx, r.client, r.flags, r.trustDomain)
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(cred.CertPEM, cred.KeyPEM)
	if err != nil {
		return fmt.Errorf("refresh produced an invalid keypair: %w", err)
	}
	r.cert.Store(&cert)
	r.mu.Lock()
	r.expiry = cred.ExpiresAt
	r.mu.Unlock()
	r.transport.CloseIdleConnections()
	return nil
}

// resolveAuth configures the adapter's additional certificate identity. OAuth
// remains required on each MCP request and passes through from the local client
// or AuthHeader. Explicit
// client certificate files bypass enrollment; otherwise the adapter enrolls
// in memory and optionally renews the certificate before expiry. The gateway
// derives identity from the verified certificate.
func resolveAuth(
	ctx context.Context,
	idFlags identityFlags,
	sessionFlags *platformSessionFlags,
	baseTransport *agentadapter.RuntimeTransport,
	sink io.Writer,
) (*agentadapter.RuntimeTransport, func(), error) {
	noop := func() {}
	// Explicit certificate files are useful for externally managed credentials
	// and do not require a platform API session at adapter startup.
	if strings.TrimSpace(idFlags.tlsClientCert) != "" {
		if sessionFlags != nil && sessionFlags.autoRefresh {
			return nil, noop, fmt.Errorf("--auto-refresh applies only to certificates enrolled in memory; remove the TLS client certificate flags")
		}
		return baseTransport, noop, nil
	}
	if sessionFlags == nil || !sessionFlags.enabled() {
		return nil, noop, fmt.Errorf("--server (or $%s) is required to enroll a certificate", EnvAdapterServer)
	}
	if strings.TrimSpace(sessionFlags.agent) == "" {
		return nil, noop, fmt.Errorf("--agent (or $%s) is required to enroll a certificate", EnvAdapterAgent)
	}
	trustDomain := strings.TrimSpace(idFlags.trustDomain)

	if u := strings.TrimSpace(sessionFlags.platformURL); u != "" {
		if err := os.Setenv(EnvPlatformURL, u); err != nil {
			return nil, noop, fmt.Errorf("set %s: %w", EnvPlatformURL, err)
		}
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return nil, noop, fmt.Errorf("platform client: %w", err)
	}
	transport, stop, err := setupMTLS(ctx, client, *sessionFlags, trustDomain, baseTransport, sessionFlags.autoRefresh, idFlags.tlsInsecureSkipVerify, sink)
	if err != nil {
		return nil, noop, err
	}
	return transport, stop, nil
}
