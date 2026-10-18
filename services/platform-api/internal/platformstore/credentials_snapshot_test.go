package platformstore

import (
	"testing"

	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/sessionstate"
)

func baseSnapshot() credentialSnapshot {
	return credentialSnapshot{
		session:  sessionstate.Session{ID: "ns/s1", ServerUID: "uid-1", GrantNamespace: "ns", GrantName: "g1"},
		bindings: []cb.Binding{{Namespace: "ns", Name: "b1", Source: "src", Server: "srv", ServerUID: "uid-1", Tools: []string{"echo"}, HumanID: "h", AgentID: "a", TeamID: "t", Header: cb.HeaderPrefix + "Token", Revision: 1}},
		sources:  []cb.Source{{Namespace: "ns", Name: "src", Provider: "kubernetes", Resource: "r", Key: "k", UserID: "h", Revision: 1}},
	}
}

func clone(s credentialSnapshot) credentialSnapshot {
	s.bindings = append([]cb.Binding(nil), s.bindings...)
	s.bindings[0].Tools = append([]string(nil), s.bindings[0].Tools...)
	s.sources = append([]cb.Source(nil), s.sources...)
	return s
}

// A delete+recreate can reuse a revision; the post-I/O check must compare
// content, not just name and revision.
func TestSameCredentialMetadataRejectsRecreatedItemsWithReusedRevision(t *testing.T) {
	if !sameCredentialMetadata(baseSnapshot(), clone(baseSnapshot())) {
		t.Fatal("identical snapshots must match")
	}
	for name, mutate := range map[string]func(*credentialSnapshot){
		"source resource":  func(s *credentialSnapshot) { s.sources[0].Resource = "other" },
		"source key":       func(s *credentialSnapshot) { s.sources[0].Key = "other" },
		"source provider":  func(s *credentialSnapshot) { s.sources[0].Provider = "vault" },
		"source version":   func(s *credentialSnapshot) { s.sources[0].Version = "2" },
		"source owner":     func(s *credentialSnapshot) { s.sources[0].UserID, s.sources[0].TeamID = "", "t2" },
		"source disabled":  func(s *credentialSnapshot) { s.sources[0].Disabled = true },
		"binding header":   func(s *credentialSnapshot) { s.bindings[0].Header = cb.HeaderPrefix + "Other" },
		"binding tools":    func(s *credentialSnapshot) { s.bindings[0].Tools = []string{"echo", "rm"} },
		"binding subjects": func(s *credentialSnapshot) { s.bindings[0].AgentID = "b" },
		"binding source":   func(s *credentialSnapshot) { s.bindings[0].Source = "src2" },
		"binding server":   func(s *credentialSnapshot) { s.bindings[0].ServerUID = "uid-2" },
		"session grant":    func(s *credentialSnapshot) { s.session.GrantName = "g2" },
		"session server":   func(s *credentialSnapshot) { s.session.ServerUID = "uid-2" },
	} {
		changed := clone(baseSnapshot())
		mutate(&changed)
		if sameCredentialMetadata(baseSnapshot(), changed) {
			t.Errorf("%s: changed content with identical name and revision was accepted", name)
		}
	}
}
