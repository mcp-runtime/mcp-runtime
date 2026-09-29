package agentadapter

// Identity is the adapter's session subject (human, agent, team, session).
// It is optional local metadata. Runtime governance
// identity is the session-bound client certificate (and OAuth bearer when the
// target enables it), not request headers.
type Identity struct {
	HumanID   string
	AgentID   string
	TeamID    string
	SessionID string
}
