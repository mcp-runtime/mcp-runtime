package platformauth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIPTrustBoundary(t *testing.T) {
	for _, tc := range []struct{ name, peer, trusted, header, want string }{
		{"default ignores spoof", "10.0.0.2:123", "", "203.0.113.1", "10.0.0.2"},
		{"untrusted peer", "10.0.0.2:123", "10.0.0.3/32", "203.0.113.1", "10.0.0.2"},
		{"trusted single", "10.0.0.2:123", "10.0.0.2/32", "203.0.113.1", "203.0.113.1"},
		{"forged left prefix", "10.0.0.2:123", "10.0.0.2/32", "198.51.100.99, 203.0.113.1", "203.0.113.1"},
		{"trusted multiple", "10.0.0.2:123", "10.0.0.0/24", "198.51.100.99, 203.0.113.1, 10.0.0.3", "203.0.113.1"},
		{"multiple header fields", "10.0.0.2:123", "10.0.0.0/24", "203.0.113.1|10.0.0.3", "203.0.113.1"},
		{"malformed suffix", "10.0.0.2:123", "10.0.0.2/32", "203.0.113.1, invalid", "10.0.0.2"},
		{"empty hop", "10.0.0.2:123", "10.0.0.2/32", "203.0.113.1, ", "10.0.0.2"},
		{"invalid trust", "10.0.0.2:123", "10.0.0.2/32,typo", "203.0.113.1", "10.0.0.2"},
		{"trust all", "10.0.0.2:123", "0.0.0.0/0", "203.0.113.1", "10.0.0.2"},
		{"ipv6", "[2001:db8::2]:123", "2001:db8::2/128", "2001:db8:1::1", "2001:db8:1::1"},
		{"mapped peer", "[::ffff:10.0.0.2]:123", "10.0.0.2/32", "::ffff:203.0.113.1", "203.0.113.1"},
		{"unknown peer", "not-an-address", "10.0.0.2/32", "203.0.113.1", UnknownRequestIP},
		{"excess hops", "10.0.0.2:123", "10.0.0.2/32", strings.Repeat("203.0.113.1,", 33) + "203.0.113.2", "10.0.0.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/login", nil)
			request.RemoteAddr = tc.peer
			for _, field := range strings.Split(tc.header, "|") {
				request.Header.Add("X-Forwarded-For", field)
			}
			if got := requestIP(request, tc.trusted); got != tc.want {
				t.Fatalf("client IP=%q want %q", got, tc.want)
			}
		})
	}
}
