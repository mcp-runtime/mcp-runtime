# Module 2: Your first governed server

Deploy a sample MCP server on the public reference platform, create a grant,
connect a client, and inspect the result in the analytics dashboard. This
walkthrough covers user and team workflows. To try platform administration,
install MCP Runtime in your own environment; see
[Platform Installation](../self-hosting.md).

**Prerequisites:**

- Module 1 completed (you understand Grants, Sessions, and the gateway)
- `mcp-runtime` CLI installed from the [latest GitHub release](https://github.com/mcp-runtime/mcp-runtime/releases/latest)
- Account and team membership on an existing platform, or a local cluster running
- Git, Go `1.26+`, and Docker with its daemon running for the example build

The public platform lets you try deployment, grants, and client connection.
It denies calls to grant-protected tools because it does not verify the agent's
identity. For successful tool calls, use your own installation with agent
identity verification enabled. See [Client Connections](../connect-clients.md).

## Step 1: Log in

For your own installation, replace the platform API URL below, the image
registry in step 5, the MCP route in step 8, and the dashboard URL in step 9.

```bash
mcp-runtime auth login \
  --api-url https://platform.mcpruntime.org \
  --email you@example.com --password '...' \
  --profile me

mcp-runtime auth status    # confirm profile is active
```

## Step 2: Get the example server

Clone the repo to use the workspace-assistant MCP server:

```bash
git clone https://github.com/mcp-runtime/mcp-runtime
cd mcp-runtime/examples/oauth-example-go-2025-11-25
```

This is a Go MCP server with 9 tools: `echo`, `add`, `upper`, `lower`,
`create_task`, `draft_release_note`, `slugify`, `aaa-ping`, `whoami`.

## Step 3: Discover tools and scaffold metadata

Run the server locally so `server init` can call its `tools/list` endpoint:

```bash
go run . &
SERVER_PID=$!
# Wait for the listening log and confirm http://localhost:8088/health.

mcp-runtime server init my-server \
  --from-server http://localhost:8088
# Discovered: aaa-ping, add, create_task, draft_release_note,
#             echo, lower, slugify, upper, whoami

kill $SERVER_PID
```

Open the generated `.mcp/servers.yaml`. Every tool has a `sideEffect` and
`requiredTrust`; the generated allow-list policy enforces both. This example
does not configure OAuth. The public platform lets you create the grant and
connect a client, but it cannot verify the agent for a protected tool call.

## Step 4: Validate before building

```bash
mcp-runtime server validate --metadata-dir .mcp
```

Validation catches tool name mismatches before the build.

## Step 5: Build, push, deploy

```bash
# Build (from the directory with the Dockerfile)
mcp-runtime server build image my-server --tag v1
# Prints the exact image ref, for example: registry.mcpruntime.org/myteam/my-server:v1

# Push using the exact ref printed above
mcp-runtime server push \
  --image registry.mcpruntime.org/myteam/my-server:v1 \
  --scope tenant

# Deploy
mcp-runtime server deploy my-server --scope tenant --metadata-dir .mcp
```

## Step 6: Confirm the server is up

```bash
mcp-runtime server list
# NAME       NAMESPACE          READY   STATUS
# my-server  mcp-team-myteam    1/1     Ready
```

```bash
mcp-runtime server get my-server --namespace mcp-team-myteam
mcp-runtime server policy inspect my-server --namespace mcp-team-myteam
```

`policy inspect` shows the policy document the gateway enforces: every tool,
its trust level, and its side-effect class.

## Step 7: Create a grant

Grant an active managed agent access to `echo` and `add`. List the agents in
your team and use an active agent ID:

```bash
mcp-runtime agent list myteam --status active
AGENT_ID=agt_01arz3ndektsv4rrffq69g5fav # replace with an ID from the list
```

```bash
mcp-runtime access grant init my-grant \
  --server my-server \
  --namespace mcp-team-myteam \
  --agent-id "$AGENT_ID" \
  --tool echo \
  --tool add \
  --output grant.yaml

# Always validate the grant against the metadata before applying
mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml

mcp-runtime access grant apply --file grant.yaml
mcp-runtime access grant list
```

Validate before you apply. If `echo` is not in `.mcp/servers.yaml`, the gateway
returns `tool_side_effect_unknown` and denies the call.

## Step 8: Connect via the adapter

Start the adapter proxy. It creates the agent session and enrolls a certificate:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/my-server/mcp \
  --server my-server \
  --agent "$AGENT_ID" \
  --auto-refresh \
  --listen 127.0.0.1:8099
```

Point Claude Desktop, Cursor, or any MCP client at `http://127.0.0.1:8099`.

On the public platform, the client connects, but a call to `echo` or `add` is
denied because the platform cannot verify which agent made it. You will see
`401 missing_identity`. On your own installation, agent identity verification
can be enabled so granted tools can run; see
[Client Connections](../connect-clients.md).

## Step 9: See it in analytics

Open [platform.mcpruntime.org](https://platform.mcpruntime.org) → **Analytics → Tools**.

The dashboard shows the recorded tool call and its denied decision. On an
installation that verifies agent identity, it can also show successful calls
to tools covered by the grant.

## What just happened

- The operator created the Deployment, Service, and Ingress from the `MCPServer`.
- The gateway checked the caller before enforcing the grant. On the public
  platform, it denied the call because it could not verify the agent.
- The analytics dashboard shows the tool call and its decision.

## Try grant changes on your own installation

The following exercise needs an installation that verifies agent identity.

1. Delete the grant: `mcp-runtime access grant delete my-grant --namespace mcp-team-myteam`
2. Call `echo` again. All calls are now denied.
3. Re-apply the grant: `mcp-runtime access grant apply --file grant.yaml`
4. Calls to granted tools succeed again.

You can also revoke the session:
```bash
mcp-runtime access session list --namespace mcp-team-myteam
mcp-runtime access session revoke <session-name> --namespace mcp-team-myteam
```

**Next:** [Module 3: Multi-team access](03-multi-team-access.md)
