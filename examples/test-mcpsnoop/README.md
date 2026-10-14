# Inspect real tool calls with MCPSnoop

This example is for developers inspecting MCP request and response bodies.
It exposes exactly two read-only tools, with no login, credentials, or gateway
session required. It uses the Python MCP SDK's Streamable HTTP transport and
the `2025-11-25` initialize handshake. Read the deployment steps first if you
need your own deployment; otherwise start with the MCPSnoop commands.

| Tool | Arguments | Result |
|---|---|---|
| `echo` | `message`: string | The same string, including Unicode |
| `todays_info` | `timezone_name`: IANA timezone, default `UTC` | Date, weekday, timezone, and timestamp with UTC offset |

The server is stateless and returns JSON responses. The metadata deliberately
sets `gateway.enabled: false` and omits `auth`, policy, grants, and sessions.
This publishes the read-only demo without Runtime gateway authentication,
governance, or gateway analytics. Requests remain visible in MCPSnoop.

## Build, publish, and deploy

Prerequisites: Docker, the built `./bin/mcp-runtime` CLI, a saved platform login
with public publishing/deployment permissions, a public-mode platform, and an available server name.
Run from the repository root. These commands publish an image and create one
MCP server on the platform selected by your saved login.
The checked-in image publishes into the public catalog. Organization and team
publishing use `--scope org` and `--scope tenant`, respectively; choose the
matching metadata scope and repository prefix before building.

```bash
./bin/mcp-runtime auth status
./bin/mcp-runtime server validate --metadata-dir examples/test-mcpsnoop/.mcp

SNOOP_TAG="snoop-$(date -u +%Y%m%d%H%M%S)"
./bin/mcp-runtime server build image test-mcpsnoop \
  --context examples/test-mcpsnoop \
  --dockerfile examples/test-mcpsnoop/Dockerfile \
  --metadata-dir examples/test-mcpsnoop/.mcp \
  --platform linux/amd64 \
  --tag "$SNOOP_TAG"

# Use the exact image repository printed by build.
./bin/mcp-runtime server push \
  --image "registry.mcpruntime.org/public/test-mcpsnoop:$SNOOP_TAG" \
  --scope public

./bin/mcp-runtime server deploy test-mcpsnoop \
  --metadata-dir examples/test-mcpsnoop/.mcp \
  --scope public

./bin/mcp-runtime server get test-mcpsnoop --namespace mcp-servers-public
./bin/mcp-runtime server connect-config test-mcpsnoop \
  --namespace mcp-servers-public --client json
```

`server init` originally generated the checked-in metadata; its default enables
the gateway, so keep the example's explicit `gateway.enabled: false` when
regenerating it. For another platform, change the metadata image/ingress host
and use the registry and endpoint returned by that platform. To replace your
existing demo deployment after rebuilding and pushing, add `--update` to
`server deploy`. Delete only this demo when finished:

```bash
./bin/mcp-runtime server delete test-mcpsnoop --namespace mcp-servers-public
```

## Watch requests and responses

Install [MCPSnoop](https://github.com/kerlenton/mcpsnoop); these commands were
verified with v0.26.1. In terminal 1, leave the HTTP proxy running:

```bash
npm install -g mcpsnoop@0.26.1
mcpsnoop http \
  --target https://mcp.mcpruntime.org/test-mcpsnoop/mcp \
  --listen 127.0.0.1:7000 \
  --label test-mcpsnoop
```

In terminal 2, open the inspector:

```bash
mcpsnoop
```

Configure your local MCP client to use the proxy, then restart its session:

```json
{
  "mcpServers": {
    "test-mcpsnoop": {
      "type": "http",
      "url": "http://127.0.0.1:7000/mcp"
    }
  }
}
```

Ask the client:

```text
Use test-mcpsnoop to echo "hello from mcpsnoop", then call todays_info with
timezone_name "Asia/Kolkata".
```

In MCPSnoop, select the `test-mcpsnoop` session and press Enter on a frame to
inspect its complete payload. Press `/` and filter by `method:tools/call`,
`tool:echo`, `tool:todays_info`, `dir:c2s` (request), or `dir:s2c` (response).
Use `id:<request-id>` to inspect both directions of one call.

## Generate traffic with curl

The following sends a handshake and two real tool calls through the local
proxy, so you can inspect traffic before connecting an AI client. This
stateless server does not require an `Mcp-Session-Id`.

```bash
curl -sS http://127.0.0.1:7000/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl-mcpsnoop","version":"1.0"}}}'

curl -sS http://127.0.0.1:7000/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -sS http://127.0.0.1:7000/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello from mcpsnoop"}}}'

curl -sS http://127.0.0.1:7000/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"todays_info","arguments":{"timezone_name":"Asia/Kolkata"}}}'
```

Export the latest captured session after making the calls:

```bash
mcpsnoop export -T json -o test-mcpsnoop-capture.json
mcpsnoop export -T html -o test-mcpsnoop-capture.html
```

Press Ctrl+C in terminal 1 to stop the proxy. Captures include the messages you
send and the tool results; use sample text when demonstrating this server.
