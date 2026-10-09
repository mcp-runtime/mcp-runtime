package main

import policypkg "mcp-runtime/pkg/policy"

// upstreamFilter is stage 5 of the gateway pipeline. It preserves the OAuth
// bearer token for validation by the upstream MCP server, strips any configured
// path prefix, and forwards the request via the reverse proxy.
//
// upstreamFilter reads Exchange.Policy, Exchange.Identity, and Exchange.OAuthToken
// (all set by earlier stages) and must not mutate them. It always returns Respond
// so the pipeline halts and stage 6 (audit) runs from the orchestrator.
func (s *gatewayServer) upstreamFilter(ex *Exchange) Result {
	// The verified SPIFFE header is an ingress-to-gateway assertion; the MCP
	// server must never see it, forged or not.
	ex.R.Header.Del(s.verifiedSPIFFEHeaderName())
	// Header mode forwards every name in auth.headers unchanged. There is no
	// scheme or vendor list. Authorization is removed only when this server
	// did not configure it and the request is not an OAuth-validated token.
	if !policypkg.PolicyUsesOAuth(ex.Policy) && !policypkg.DelegatedHeaderConfigured(ex.Policy, defaultTokenHeader) {
		ex.R.Header.Del(defaultTokenHeader)
	}
	if trimmedPath, ok := trimRequestPathPrefix(ex.R.URL.Path, s.stripPrefix); ok {
		ex.R.URL.Path = trimmedPath
		// Always clear RawPath when Path was trimmed. If RawPath trims cleanly
		// keep the percent-encoded form; otherwise clear it so Go's URL machinery
		// falls back to escaping Path — preventing an inconsistent URL where Path
		// is stripped but RawPath still carries the prefix.
		if trimmedRaw, rawOK := trimRequestPathPrefix(ex.R.URL.RawPath, s.stripPrefix); rawOK {
			ex.R.URL.RawPath = trimmedRaw
		} else {
			ex.R.URL.RawPath = ""
		}
		if ex.R.URL.Path == "" {
			ex.R.URL.Path = "/"
			if ex.R.URL.RawPath != "" {
				ex.R.URL.RawPath = "/"
			}
		}
	}

	s.proxy.ServeHTTP(ex.W, ex.R)
	return Respond
}
