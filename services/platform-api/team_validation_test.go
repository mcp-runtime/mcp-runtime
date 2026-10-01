package main

import "testing"

func TestValidateTeamSlug(t *testing.T) {
	cases := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "valid", slug: "platform-core", wantErr: false},
		{name: "empty", slug: "", wantErr: true},
		{name: "invalid characters", slug: "core/team", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTeamSlug(tc.slug)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTeamSlug(%q) error = nil, want error", tc.slug)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTeamSlug(%q) error = %v, want nil", tc.slug, err)
			}
		})
	}
}

func TestValidateTeamNamespaceReserved(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		wantErr   bool
	}{
		{name: "team namespace", namespace: "mcp-team-core", wantErr: false},
		{name: "shared reserved", namespace: sharedCatalogNamespace, wantErr: true},
		{name: "kube reserved", namespace: "kube-system", wantErr: true},
		{name: "platform reserved", namespace: "mcp-platform", wantErr: true},
		{name: "observability reserved", namespace: "mcp-observability", wantErr: true},
		{name: "log collector reserved", namespace: "mcp-log-collector", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTeamNamespace(tc.namespace)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTeamNamespace(%q) error = nil, want error", tc.namespace)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTeamNamespace(%q) error = %v, want nil", tc.namespace, err)
			}
		})
	}
}
