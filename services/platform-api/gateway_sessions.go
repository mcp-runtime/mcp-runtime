package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"mcp-platform-api/internal/sessionapi"
)

// startGatewaySessions binds the separate tenant-facing read audience before
// publishing readiness. None of these routes are mounted on the main listener.
func (s *apiServer) startGatewaySessions() (*http.Server, error) {
	identity, err := sessionapi.NewKubernetesIdentity()
	if err != nil {
		return nil, fmt.Errorf("gateway identity: %w", err)
	}
	listener, err := net.Listen("tcp", ":"+envOr("GATEWAY_SYNC_PORT", "8085"))
	if err != nil {
		return nil, err
	}
	var sessions sessionapi.Store
	if s.platform != nil {
		sessions = s.platform
	}
	server := &http.Server{Handler: sessionapi.Handler{Store: sessions, Authenticate: identity.Authenticate}.Gateway(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway session listener stopped: %v", err)
		}
	}()
	return server, nil
}

func shutdownGatewaySessions(ctx context.Context, server *http.Server) error {
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}
