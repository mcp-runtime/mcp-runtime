// Package errx provides structured, code-based errors for MCP runtime and CLI tooling.
//
// The package implements a code-based error system where each error has:
//   - A stable 5-digit error code (e.g., "72000" for registry errors)
//   - A category description (e.g., "Registry error")
//   - A user-facing message
//   - Optional structured context (key-value pairs)
//   - Optional cause and base error errors
//
// Error codes follow a scheme where the first two digits represent the domain:
//   - 70xxx: CLI/argument validation errors
//   - 71xxx: Cluster/provisioning errors
//   - 72xxx: Registry errors
//   - 73xxx: Operator errors
//   - 74xxx: Pipeline errors
//   - 75xxx: Build errors
//   - 76xxx: Server definition errors
//   - 77xxx: Certificate/TLS errors
//   - 78xxx: Setup/installation errors
//   - 79xxx: Configuration errors
//
// The last three digits are reserved for subcodes (future use).
//
// Example usage:
//
//	err := errx.Registry("failed to connect to registry").
//		WithContext("url", "registry.mcpruntime.com").
//		WithBase(baseErr)
//
//	if errors.Is(err, baseErr) {
//		// Handle specific error
//	}
//
//	fmt.Println(errx.UserString(err))  // User-friendly message
//	fmt.Println(errx.DebugString(err)) // Full debug details
package errx
