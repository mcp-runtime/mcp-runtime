package update

import (
	"strings"
	"testing"
)

func TestMigrationGuardsSayWhatToRunNext(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		runs    []string
		pointer string
	}{
		{"audience", audienceMigrationRequired("route lacks exchange"), []string{"`mcp-runtime setup`", "`mcp-runtime update`", "setup first, then update"}, "docs/troubleshooting.md#update-refuses-token-audience-setup-migration-required"},
		{"analytics", analyticsMigrationRequired("plaintext kafka"), []string{"`mcp-runtime setup`", "`mcp-runtime update`", "encrypted CA/certificate backups"}, "docs/platform-services.md#analytics-transport-security"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if !strings.Contains(msg, "no update changes were made") {
				t.Fatalf("message must state nothing changed: %s", msg)
			}
			for _, want := range append(tt.runs, tt.pointer) {
				if !strings.Contains(msg, want) {
					t.Fatalf("message missing %q: %s", want, msg)
				}
			}
		})
	}
}
