// Package session owns browser session data, expiry, revocation and encrypted
// shared persistence. It has no process configuration or authentication client.
package session

import "time"

const CookieName = "mcp_ui_session"
const Duration = 8 * time.Hour

type Principal struct {
	Role     string `json:"role,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Email    string `json:"email,omitempty"`
	AuthType string `json:"auth_type,omitempty"`
}

type Session struct {
	ID                 string
	ExpiresAt          time.Time
	Principal          Principal
	UpstreamAuthHeader string
	UpstreamAPIKey     string
	// CSRFToken is the session-bound synchroniser token. It is handed to
	// same-origin JavaScript and required back on every state-changing
	// request; the browser handler owns validation in the UI's csrf.go.
	CSRFToken string
}
