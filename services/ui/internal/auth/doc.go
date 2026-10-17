// Package auth owns the UI's platform authentication client and principal
// contract. Callers inject the HTTP client; session storage and browser-facing
// handlers remain separate from upstream credential verification.
package auth
