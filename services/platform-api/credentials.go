package main

import (
	"context"
	"net/http"

	"mcp-platform-api/internal/credentialapi"
	"mcp-platform-api/internal/sessionapi"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/sessionstate"
)

func (s *apiServer) registerCredentials(mux *http.ServeMux) {
	h := credentialapi.Handler{}
	if s.platform != nil {
		h.Store = s.platform
	}
	if identity, err := sessionapi.NewKubernetesIdentity(); err == nil {
		h.Kube = identity.Client
		h.Authenticate = func(ctx context.Context, token string) (sessionstate.Server, error) {
			return identity.AuthenticateAudience(ctx, token, cb.Audience)
		}
		h.Providers = credentialapi.Providers(identity.Client)
	}
	mux.Handle(cb.ManagementPath+"/", s.authentic.Middleware(http.HandlerFunc(h.Management)))
	mux.HandleFunc(cb.ResolvePath, h.Resolve)
}
