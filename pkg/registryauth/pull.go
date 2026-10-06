package registryauth

import "strings"

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
