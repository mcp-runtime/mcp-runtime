# MCP server examples

The server examples are organized by **language and MCP protocol revision**.
There is one directory for each implemented language/version combination.
Examples whose MCP server directly supports OAuth use the `oauth-` prefix in
the directory, server, and README names. Adapter mTLS and Runtime gateway auth
are separate platform features and do not determine this prefix.

| Language | Protocol revision | Directory | OAuth route |
|---|---|---|---|
| Go | `2025-11-25` | [`oauth-example-go-2025-11-25`](oauth-example-go-2025-11-25/) | Gateway enabled: `oauth-example-go-2025-11-25-gateway`; standalone OAuth is exercised in E2E as `oauth-example-go-2025-11-25-standalone` |
| Python | `2025-11-25` | [`example-python-2025-11-25`](example-python-2025-11-25/) | Gateway enabled: `example-python-2025-11-25-gateway` |
| Rust | `2025-11-25` | [`example-rust-2025-11-25`](example-rust-2025-11-25/) | Gateway enabled: `example-rust-2025-11-25-gateway` |
| TypeScript | `2025-06-18` | [`oauth-example-typescript-2025-06-18`](oauth-example-typescript-2025-06-18/) | Standalone server-side OAuth: `oauth-example-typescript-2025-06-18-standalone` |

The revision is the latest protocol revision demonstrated by that server's
implementation. The legacy SDK based servers use the `initialize` handshake;
the TypeScript fixture declares `2025-06-18` directly in its JSON-RPC response.
There is no `2026-07-28` stateless server fixture in this set yet.

OAuth server examples import the published `mcp-auth` SDK directly; no separate
SDK client checkout is needed. Any server example can use Runtime governance
with its gateway enabled; a separate governed server fixture is unnecessary.

Each server's `.mcp/` directory contains CLI-generated server metadata. Gateway
examples also include grant and session metadata. Standalone examples omit
these unused gateway configurations. The `-gateway` names use Runtime gateway governance; the
`-standalone` names validate OAuth in the MCP server itself without gateway
grants, sessions, policy, or audit. Adapter mTLS remains a separate
adapter-to-Runtime identity feature. OAuth issuer and audience settings derive
from the platform configuration and public MCP URL. Check the gateway example metadata with:

```bash
./bin/mcp-runtime server validate \
  --metadata-dir examples/oauth-example-go-2025-11-25/.mcp \
  --grant-file examples/oauth-example-go-2025-11-25/.mcp/grant-example-agent.yaml \
  --session-file examples/oauth-example-go-2025-11-25/.mcp/session-example-agent.yaml
```
