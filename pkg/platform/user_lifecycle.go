package platform

// UserDeactivationResult contains non-secret, transactionally committed cleanup counts.
type UserDeactivationResult struct {
	UserID                     string `json:"user_id"`
	Status                     string `json:"status"`
	AlreadyDeactivated         bool   `json:"already_deactivated"`
	RevokedAPIKeys             int64  `json:"revoked_api_keys"`
	RevokedRegistryCredentials int64  `json:"revoked_registry_credentials"`
	RevokedRefreshTokens       int64  `json:"revoked_refresh_tokens"`
	RevokedSessions            int64  `json:"revoked_sessions"`
	RemovedMemberships         int64  `json:"removed_memberships"`
}
