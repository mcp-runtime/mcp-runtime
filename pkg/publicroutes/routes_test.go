package publicroutes

import "testing"

func TestRouteValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		routes  Routes
		invalid bool
	}{
		{"defaults", Routes{}, false},
		{"single host", Routes{Platform: "/platform", Grafana: "/monitoring", Docs: "/help", Registry: "/images"}, false},
		{"traversal", Routes{Platform: "/a/../platform"}, true},
		{"encoded slash", Routes{Platform: "/platform%2fauth"}, true},
		{"trailing slash", Routes{Platform: "/platform/"}, true},
		{"API collision", Routes{Platform: "/api"}, true},
		{"OCI collision", Routes{Registry: "/v2"}, true},
		{"nested routes", Routes{Platform: "/platform", Docs: "/platform/docs"}, true},
		{"session collision", Routes{Docs: "/auth/login"}, true},
		{"docs credentials", Routes{DocsURL: "https://user:secret@example.com/docs"}, true},
		{"insecure docs", Routes{DocsURL: "http://example.com/docs"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.routes.Validate()
			if (err != nil) != tc.invalid {
				t.Fatalf("Validate() = %v, invalid=%v", err, tc.invalid)
			}
		})
	}
}

func TestAuthRouteCollisions(t *testing.T) {
	for _, tc := range []struct {
		platform, issuer string
		invalid          bool
	}{
		{"/platform", "https://customer.example.com/auth", false},
		{"/", "https://customer.example.com/mcp-auth", false},
		{"/", "https://customer.example.com/auth", true},
		{"/platform", "https://customer.example.com/platform/auth", true},
		{"/platform", "https://customer.example.com/v2", true},
		{"/platform", "https://customer.example.com/registry", true},
		{"/", "https://auth.example.com/auth", false},
		{"/platform", "https://customer.example.com/auth?foo=bar", true},
	} {
		err := (Routes{Platform: tc.platform}).ValidateAuth("customer.example.com", tc.issuer)
		if (err != nil) != tc.invalid {
			t.Errorf("%s %s: %v", tc.platform, tc.issuer, err)
		}
	}
}
