package platforminventory

// CredentialSet defines the exact keys delivered to its consumers. Shared keys
// have one authoritative set; setup synchronizes only explicitly listed copies.
// This metadata describes consumption, not authorization to read Secrets.
type CredentialSet struct {
	Name      string
	Consumers []string
	Keys      []string
}

var credentialSets = []CredentialSet{
	{Name: "mcp-platform-api-credentials", Consumers: []string{"platform-api", "admin-bootstrap"}, Keys: []string{"API_KEYS", "ADMIN_API_KEYS", "POSTGRES_DSN", "JWT_SECRET", "INTERNAL_AUTH_TOKEN", "ADMIN_USERS", "PLATFORM_ADMIN_EMAIL", "PLATFORM_ADMIN_PASSWORD", "PLATFORM_DEV_LOGIN_ENABLED", "PLATFORM_DEV_USER_EMAIL", "PLATFORM_DEV_USER_PASSWORD", "PLATFORM_DEV_ADMIN_EMAIL", "PLATFORM_DEV_ADMIN_PASSWORD"}},
	{Name: "mcp-runtime-api-credentials", Consumers: []string{"runtime-api"}, Keys: []string{"API_KEYS", "ADMIN_API_KEYS", "JWT_SECRET", "INTERNAL_AUTH_TOKEN"}},
	{Name: "mcp-analytics-api-credentials", Consumers: []string{"analytics-api"}, Keys: []string{"API_KEYS", "ADMIN_API_KEYS", "JWT_SECRET", "INTERNAL_AUTH_TOKEN"}},
	{Name: "mcp-ui-credentials", Consumers: []string{"ui"}, Keys: []string{"UI_API_KEY", "ADMIN_API_KEYS", "UI_SESSION_DATABASE_URL", "UI_SESSION_ENCRYPTION_KEY"}},
	{Name: "mcp-ingest-credentials", Consumers: []string{"ingest", "gateway-example"}, Keys: []string{"INGEST_API_KEYS"}},
	{Name: "mcp-runtime-ingest-credentials", Consumers: []string{"runtime-api"}, Keys: []string{"INGEST_API_KEYS"}},
	{Name: "mcp-grafana-credentials", Consumers: []string{"grafana"}, Keys: []string{"GRAFANA_ADMIN_USER", "GRAFANA_ADMIN_PASSWORD"}},
	{Name: "mcp-postgres-credentials", Consumers: []string{"postgres"}, Keys: []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"}},
	{Name: "mcp-platform-signing-credentials", Keys: []string{"OAUTH_PRIVATE_KEY"}},
}

// CredentialSets returns copies safe for callers to modify.
func CredentialSets() []CredentialSet {
	out := make([]CredentialSet, len(credentialSets))
	for i, c := range credentialSets {
		c.Keys = append([]string(nil), c.Keys...)
		c.Consumers = append([]string(nil), c.Consumers...)
		out[i] = c
	}
	return out
}

// CredentialOwner returns the authoritative Secret for a key. For shared API
// auth keys platform-api is the owner; the remaining API/UI copies are derived.
func CredentialOwner(key string) (string, bool) {
	for _, c := range credentialSets {
		for _, k := range c.Keys {
			if k == key {
				return c.Name, true
			}
		}
	}
	return "", false
}
