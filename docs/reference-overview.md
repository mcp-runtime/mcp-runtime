# CLI and API Reference

Use this section to look up commands, resource fields, HTTP endpoints, and
authentication requirements. For a guided workflow, start with
[Server and Client Guides](usage-overview.md) or [Deployment and Operations](hosting-overview.md).

## Choose the reference you need

| Question | Reference |
|---|---|
| Which command, flag, or profile should I use? | [CLI Commands](cli-reference.md) |
| What fields do servers, grants, and sessions accept, or what does an endpoint return? | [API and Resources](api-reference.md) |
| Which credentials and permissions does an endpoint require? | [Authentication and Authorization](security/authz-matrix.md) |
| Where is a Go symbol defined and how is it implemented? | [Go Packages](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime) |

For API integrations, read the resource and endpoint contract together with
the authentication and authorization matrix. A valid payload alone does not
give the caller permission to manage the resource.

## Related guides

[Concepts and Architecture](concepts-overview.md) explains why the contracts are
shaped this way. [Implementation Details](internals/README.md) maps them to
source packages and tests. [Troubleshooting](troubleshooting.md) covers common
failures encountered while using the CLI or platform APIs.
