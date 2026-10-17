package platformauth

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// RequestIP uses the socket peer by default. Forwarded hops are considered only
// for explicitly trusted proxy CIDRs, walking from the socket toward the first
// untrusted hop. Untrusted client-supplied prefixes never become rate-limit or
// audit identities. Invalid trust/header configuration falls back to the peer.
func RequestIP(r *http.Request) string {
	return requestIP(r, os.Getenv("MCP_TRUSTED_PROXY_CIDRS"))
}

func requestIP(r *http.Request, configured string) string {
	if r == nil {
		return UnknownRequestIP
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	peer, err := netip.ParseAddr(remote)
	if err != nil || peer.Zone() != "" {
		return UnknownRequestIP
	}
	peer = peer.Unmap()
	fallback := peer.String()
	if configured == "" || len(configured) > 4096 {
		return fallback
	}
	parts := strings.Split(configured, ",")
	if len(parts) > 32 {
		return fallback
	}
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(part))
		// Trust-all and mapped prefixes erase the boundary or introduce ambiguous
		// address-family rules. Use exact ingress addresses or their real CIDRs.
		if parseErr != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return fallback
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	trusted := func(ip netip.Addr) bool {
		for _, prefix := range prefixes {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return fallback
	}
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" || len(forwarded) > 4096 {
		return fallback
	}
	hops := strings.Split(forwarded, ",")
	if len(hops) > 32 {
		return fallback
	}
	addresses := make([]netip.Addr, len(hops))
	for i, hop := range hops {
		ip, parseErr := netip.ParseAddr(strings.TrimSpace(hop))
		if parseErr != nil || ip.Zone() != "" || ip.IsUnspecified() {
			return fallback
		}
		addresses[i] = ip.Unmap()
	}
	for i := len(addresses) - 1; i >= 0; i-- {
		peer = addresses[i]
		if !trusted(peer) {
			return peer.String()
		}
	}
	return peer.String()
}
