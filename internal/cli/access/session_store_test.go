package access

import (
	"strings"
	"testing"
)

func TestKubernetesSessionManagementRequiresPlatform(t *testing.T) {
	mgr := &AccessManager{useKube: true}
	for name, operation := range map[string]func() error{
		"list":   func() error { return mgr.ListAccessResources(SessionResource, "team", false) },
		"get":    func() error { return mgr.GetAccessResource(SessionResource, "session", "team") },
		"delete": func() error { return mgr.DeleteAccessResource(SessionResource, "session", "team") },
		"revoke": func() error { return mgr.ToggleAccessResource(SessionResource, "session", "team", true) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil || !strings.Contains(err.Error(), "platform API") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
