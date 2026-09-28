package agentadapter

// Identity is the adapter's session subject (human, agent, team, session).
// It is used for local concerns such as tools-cache keys. Runtime governance
// identity is the session-bound client certificate (and OAuth bearer when the
// target enables it), not request headers.
type Identity struct {
	HumanID   string
	AgentID   string
	TeamID    string
	SessionID string
}

// IdentityProvider returns the current identity. Adapters call it when a
// caller rotates session identity at runtime without restarting the process.
// When non-nil on ProxyConfig / ShimConfig it takes precedence over the
// static Identity.
type IdentityProvider func() Identity
