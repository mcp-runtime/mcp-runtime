// Package egresscidrs parses cluster-admin destination prefixes for MCP server
// egress. An empty list adds nothing. The default route is rejected so a
// cluster cannot open every address by accident.
package egresscidrs

import (
	"fmt"
	"net/netip"
	"strings"
)

const maxPrefixes = 16

// Block is one NetworkPolicy ipBlock: a destination prefix minus cluster ranges
// that must stay on the normal pod policy.
type Block struct {
	CIDR   string
	Except []string
}

// Parse accepts a comma-separated list of canonical prefixes. IPv4 prefixes
// must be /8 or longer, and IPv6 prefixes must be /32 or longer.
func Parse(raw string) ([]netip.Prefix, error) {
	fields := strings.Split(raw, ",")
	out := make([]netip.Prefix, 0, len(fields))
	seen := map[netip.Prefix]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("pod egress CIDR %q is not a valid prefix", field)
		}
		if !prefix.IsValid() || prefix.Addr().IsMulticast() || prefix.Addr().IsUnspecified() {
			return nil, fmt.Errorf("pod egress CIDR %q is not a usable destination", field)
		}
		minBits := 8
		if prefix.Addr().Is6() {
			minBits = 32
		}
		if prefix.Bits() < minBits {
			return nil, fmt.Errorf("pod egress CIDR %q is wider than /%d", field, minBits)
		}
		if _, ok := seen[prefix]; ok {
			return nil, fmt.Errorf("pod egress CIDR %q is listed more than once", field)
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
		if len(out) > maxPrefixes {
			return nil, fmt.Errorf("pod egress accepts at most %d CIDRs", maxPrefixes)
		}
	}
	return out, nil
}

// Blocks returns ipBlock entries for allows, subtracting except prefixes that
// fall inside each allow. A private allow with no contained exception is
// rejected so the cluster pod and service ranges are not opened by accident.
func Blocks(allows, except []netip.Prefix) ([]Block, error) {
	blocks := make([]Block, 0, len(allows))
	for _, allow := range allows {
		var inside []string
		skip := false
		for _, exception := range except {
			if exception == allow || prefixContains(exception, allow) {
				skip = true
				break
			}
			if prefixContains(allow, exception) {
				inside = append(inside, exception.String())
			}
		}
		if skip {
			continue
		}
		if isPrivate(allow) && len(inside) == 0 {
			return nil, fmt.Errorf("pod egress CIDR %s overlaps private space and needs MCP_POD_EGRESS_EXCEPT_CIDRS to exclude the cluster pod and service ranges", allow)
		}
		blocks = append(blocks, Block{CIDR: allow.String(), Except: inside})
	}
	return blocks, nil
}

func prefixContains(outer, inner netip.Prefix) bool {
	return outer.Contains(inner.Addr()) && inner.Bits() >= outer.Bits()
}

func isPrivate(prefix netip.Prefix) bool {
	addr := prefix.Addr()
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}
