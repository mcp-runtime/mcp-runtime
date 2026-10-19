package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The analytics ingest client pins the platform's internal CA. OAuth issuer
// discovery and JWKS fetches target public HTTPS issuers and must keep using
// the system trust store, so they cannot share that transport.
func TestGatewayHTTPClientsKeepTrustDomainsSeparate(t *testing.T) {
	internalCA := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(internalCA.Close)
	pool := x509.NewCertPool()
	pool.AddCert(internalCA.Certificate())
	analyticsTLS := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}

	clients := newGatewayHTTPClients(analyticsTLS)

	if clients.analyticsBase.TLSClientConfig == nil || clients.analyticsBase.TLSClientConfig.RootCAs != pool {
		t.Fatal("analytics client must keep the configured internal CA")
	}
	if cfg := clients.oauthBase.TLSClientConfig; cfg != nil && (cfg.RootCAs != nil || len(cfg.Certificates) > 0) {
		t.Fatal("OAuth client must use system roots, not the analytics trust config")
	}
	if clients.oauth == clients.analytics || clients.oauthBase == clients.analyticsBase {
		t.Fatal("OAuth and analytics must not share a client or transport")
	}
	if clients.oauth.Timeout != clients.analytics.Timeout || clients.oauth.CheckRedirect == nil {
		t.Fatal("OAuth client must keep the timeout and no-redirect policy")
	}
}

func TestOAuthClientFallsBackToHTTPClient(t *testing.T) {
	base := &http.Client{}
	if got := (&gatewayServer{httpClient: base}).oauthClient(); got != base {
		t.Fatal("oauthClient must fall back to httpClient when unset")
	}
	oauth := &http.Client{}
	if got := (&gatewayServer{httpClient: base, oauthHTTPClient: oauth}).oauthClient(); got != oauth {
		t.Fatal("oauthClient must prefer oauthHTTPClient")
	}
}
