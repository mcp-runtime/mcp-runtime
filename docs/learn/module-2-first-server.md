# Module 2: Your first governed server

Deploy an MCP server, create a grant, connect a client, and watch live traffic
in the analytics dashboard.

**Prerequisites:**
- Module 1 completed (you understand Grants, Sessions, and the gateway)
- `mcp-runtime` CLI installed from the [latest GitHub release](https://github.com/mcp-runtime/mcp-runtime/releases/latest)
- Account on the live platform (`platform.mcpruntime.org`) or a local cluster running

## Step 1: Log in

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

This is a Go MCP server with 8 tools: `echo`, `add`, `upper`, `lower`,
`create_task`, `draft_release_note`, `slugify`, `aaa-ping`.

## Step 3: Discover tools and scaffold metadata

Run the server locally so `server init` can call its `tools/list` endpoint:

```bash
go run . &
SERVER_PID=$!

mcp-runtime server init my-server \
  --from-server http://localhost:8088
# Discovered: aaa-ping, add, create_task, draft_release_note,
#             echo, lower, slugify, upper

kill $SERVER_PID
```

Open the generated `.mcp/servers.yaml`. Every tool has a `sideEffect` and
`requiredTrust`; the gateway enforces both.

## Step 4: Validate before building

```bash
mcp-runtime server validate --metadata-dir .mcp
```

Validation catches tool name mismatches before the build.

## Step 5: Build, push, deploy

```bash
# Build (from the directory with the Dockerfile)
mcp-runtime server build image my-server --tag v1
# Prints: registry.mcpruntime.org/myteam/my-server:v1

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

Grant your cursor agent access to `echo` and `add`:

```bash
mcp-runtime access grant init my-grant \
  --server my-server \
  --namespace mcp-team-myteam \
  --agent-id cursor \
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

Start the adapter proxy. It creates the agent session:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/my-server/mcp \
  --server my-server \
  --agent cursor \
  --agent-id cursor \
  --auto-refresh \
  --listen 127.0.0.1:8099
```

Point Claude Desktop, Cursor, or any MCP client at `http://127.0.0.1:8099`.

Call the `echo` tool; it succeeds. Call `create_task`; the gateway denies it
because it is not in the grant.

## Step 9: See it in analytics

Open [platform.mcpruntime.org](https://platform.mcpruntime.org) → **Analytics → Tools**.

You should see rows like:

| Server | Tool | User | Team | Agent | Calls | Denied |
|---|---|---|---|---|---|---|
| my-server | echo | you@example.com | myteam | cursor | 3 | 0 |

Each call is recorded with its user, team, and agent. A denied call shows
`Denied: 1`.

## What just happened

- The operator created the Deployment, Service, and Ingress from the `MCPServer`.
- The gateway enforced the grant's allow list and blocked `create_task`.
- Every call is in the analytics database with user, team, agent, tool, and decision.

## Try breaking it intentionally

1. Delete the grant: `mcp-runtime access grant delete my-grant --namespace mcp-team-myteam`
2. Call `echo` again. All calls are now denied.
3. Re-apply the grant: `mcp-runtime access grant apply --file grant.yaml`
4. Calls succeed again.

You can also revoke the session:
```bash
mcp-runtime access session list --namespace mcp-team-myteam
mcp-runtime access session revoke <session-name> --namespace mcp-team-myteam
```

**Next:** [Module 3: Multi-team production setup](module-3-multi-team.md)
