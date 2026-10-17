// Package proxy owns the UI's fixed upstream routes, browser credential
// exchange, CSRF checks and response filtering. Bootstrap injects the session
// store and HTTP client; callers cannot supply arbitrary proxy destinations.
package proxy
