package registryauth

import "strings"

// SharedPullRepositories are the platform-owned repositories every namespace's
// node pull credential may read, in addition to the repositories the namespace
// already uses: the gateway sidecar image, and the doctor smoke image that
// `cluster doctor` and `cluster diagnostics` run inside tenant namespaces.
// Without the smoke image those checks are forbidden by the registry broker on
// installs that enforce scoped pull credentials.
func SharedPullRepositories() []string {
	return []string{"mcp-gateway", "mcp-runtime-doctor-smoke"}
}

// PullScope limits a node credential independently of platform API privileges.
type PullScope struct {
	Namespace    string   `json:"namespace"`
	Repositories []string `json:"repositories"`
	Prefixes     []string `json:"prefixes"`
}

func (s PullScope) Allows(repo string) bool {
	for _, r := range s.Repositories {
		if r == repo {
			return true
		}
	}
	for _, p := range s.Prefixes {
		if strings.HasPrefix(repo, p+"/") {
			return true
		}
	}
	return false
}
