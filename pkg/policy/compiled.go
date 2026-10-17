package policy

import (
	"encoding/json"
	"sort"
	"strings"
)

type subjectKey struct {
	human HumanID
	agent AgentID
	team  TeamID
}

type compiledIndex struct {
	hasMonitor      bool
	tools           map[ToolName]int
	sessions        map[SessionID][]int
	sessionSubjects map[subjectKey][]int
	grants          map[subjectKey][]int
}

// Compile copies a policy into an immutable authorization snapshot. Callers
// validate file integrity before applying runtime defaults and compiling. Never
// mutate the returned document; publish a new snapshot when policy changes.
// The private indexes do not change JSON, revision digests or schema versions.
func Compile(doc *Document) (*Document, error) {
	if doc == nil {
		return nil, nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var snapshot Document
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	index := &compiledIndex{
		tools:           make(map[ToolName]int, len(snapshot.Tools)),
		sessions:        make(map[SessionID][]int, len(snapshot.Sessions)),
		sessionSubjects: make(map[subjectKey][]int),
		grants:          make(map[subjectKey][]int),
	}
	for i, tool := range snapshot.Tools {
		if _, exists := index.tools[tool.Name]; !exists {
			index.tools[tool.Name] = i
		}
	}
	for i, session := range snapshot.Sessions {
		index.sessions[session.Name] = append(index.sessions[session.Name], i)
		key := subjectKey{session.HumanID, session.AgentID, session.TeamID}
		index.sessionSubjects[key] = append(index.sessionSubjects[key], i)
	}
	for i, grant := range snapshot.Grants {
		index.hasMonitor = index.hasMonitor || strings.EqualFold(strings.TrimSpace(grant.Mode), "monitor")
		key := subjectKey{grant.HumanID, grant.AgentID, grant.TeamID}
		index.grants[key] = append(index.grants[key], i)
	}
	snapshot.compiled = index
	return &snapshot, nil
}

// Empty subject fields are wildcards. A matching subject must still name at
// least one principal, so at most seven disjoint subject buckets can match.
func matchingSubjectKeys(identity Identity) ([7]subjectKey, int) {
	var keys [7]subjectKey
	var count int
	for mask := 1; mask < 8; mask++ {
		var key subjectKey
		if mask&1 != 0 {
			if identity.HumanID == "" {
				continue
			}
			key.human = identity.HumanID
		}
		if mask&2 != 0 {
			if identity.AgentID == "" {
				continue
			}
			key.agent = identity.AgentID
		}
		if mask&4 != 0 {
			if identity.TeamID == "" {
				continue
			}
			key.team = identity.TeamID
		}
		keys[count] = key
		count++
	}
	return keys, count
}

func matchingPolicyGrants(doc *Document, identity Identity) []Grant {
	if doc == nil {
		return nil
	}
	if doc.compiled == nil || doc.compiled.grants == nil {
		return matchingGrants(doc.Grants, identity)
	}
	var indices []int
	keys, count := matchingSubjectKeys(identity)
	for _, key := range keys[:count] {
		indices = append(indices, doc.compiled.grants[key]...)
	}
	// Preserve source order, including equal-name grants whose attribution was
	// historically determined by that order. Cost depends on matching grants.
	sort.Ints(indices)
	grants := make([]Grant, 0, len(indices))
	for _, index := range indices {
		grants = append(grants, doc.Grants[index])
	}
	return grants
}

func policyWithGrants(doc *Document, grants []Grant) *Document {
	copy := *doc
	copy.Grants = grants
	if doc.compiled != nil {
		index := *doc.compiled
		index.grants = nil
		copy.compiled = &index
	}
	return &copy
}

func findPolicySession(doc *Document, request Request) (Binding, bool) {
	if request.Session != nil {
		return findSession([]Binding{*request.Session}, request.Identity)
	}
	if doc == nil || doc.Session != nil && doc.Session.Store == "platform" {
		return Binding{}, false
	}
	if doc.compiled == nil {
		return findSession(doc.Sessions, request.Identity)
	}
	if request.Identity.SessionID != "" {
		for _, index := range doc.compiled.sessions[request.Identity.SessionID] {
			session := doc.Sessions[index]
			if subjectMatchesTeam(session.HumanID, session.AgentID, session.TeamID, request.Identity) {
				return session, true
			}
		}
		return Binding{}, false
	}
	first := len(doc.Sessions)
	keys, count := matchingSubjectKeys(request.Identity)
	for _, key := range keys[:count] {
		if indices := doc.compiled.sessionSubjects[key]; len(indices) > 0 && indices[0] < first {
			first = indices[0]
		}
	}
	if first == len(doc.Sessions) {
		return Binding{}, false
	}
	return doc.Sessions[first], true
}

func resolvePolicyToolMetadata(doc *Document, name ToolName) (string, string, string) {
	if doc == nil {
		return resolveToolMetadata(nil, name)
	}
	if doc.compiled == nil {
		return resolveToolMetadata(doc.Tools, name)
	}
	if index, ok := doc.compiled.tools[name]; ok {
		return resolveToolMetadata(doc.Tools[index:index+1], name)
	}
	return resolveToolMetadata(nil, name)
}
