# Quickstart

Deploy a governed MCP server and connect an MCP client to it, using the live
[platform.mcpruntime.org](https://platform.mcpruntime.org) instance. You do not
need a Kubernetes cluster. It takes about 10 minutes.

To self-host MCP Runtime on your own cluster, see [Getting Started](getting-started.md).

## 1. Install the CLI

=== "macOS (Apple Silicon)"

    ```bash
    curl -Lo mcp-runtime https://github.com/mcp-runtime/mcp-runtime/releases/latest/download/mcp-runtime-darwin-arm64
    chmod +x mcp-runtime
    sudo mv mcp-runtime /usr/local/bin/
    ```

=== "macOS (Intel)"

    ```bash
    curl -Lo mcp-runtime https://github.com/mcp-runtime/mcp-runtime/releases/latest/download/mcp-runtime-darwin-amd64
    chmod +x mcp-runtime
    sudo mv mcp-runtime /usr/local/bin/
    ```

=== "Linux (amd64)"

    ```bash
    curl -Lo mcp-runtime https://github.com/mcp-runtime/mcp-runtime/releases/latest/download/mcp-runtime-linux-amd64
    chmod +x mcp-runtime
    sudo mv mcp-runtime /usr/local/bin/
    ```

=== "Linux (arm64)"

    ```bash
    curl -Lo mcp-runtime https://github.com/mcp-runtime/mcp-runtime/releases/latest/download/mcp-runtime-linux-arm64
    chmod +x mcp-runtime
    sudo mv mcp-runtime /usr/local/bin/
    ```

=== "Windows"

    Download [mcp-runtime-windows-amd64.exe](https://github.com/mcp-runtime/mcp-runtime/releases/latest/download/mcp-runtime-windows-amd64.exe) and add it to your `PATH`.

Verify:

```bash
mcp-runtime --version
```

To upgrade the CLI, download the matching binary again from the
[latest GitHub release](https://github.com/mcp-runtime/mcp-runtime/releases/latest)
and verify `mcp-runtime --version`. The CLI binary and the hosted platform are
separate release tracks: the CLI changes only when a new release is published;
the platform UI at `platform.mcpruntime.org` is updated by its operator and
does not need a local install.

## 2. Log in

Get credentials from the [live platform](https://platform.mcpruntime.org) or use
an existing account. You need a team and a user account. Ask your platform
admin, or [self-host MCP Runtime](getting-started.md) to create your own.

```bash
mcp-runtime auth login \
  --api-url https://platform.mcpruntime.org \
  --email you@example.com --password '...' \
  --profile me

mcp-runtime auth status    # confirm the profile is active
```

## 3. Deploy an example server

Clone the repo to get the example server source:

```bash
git clone https://github.com/mcp-runtime/mcp-runtime
cd mcp-runtime/examples/oauth-example-go-2025-11-25
```

Run it locally to discover its tool names, then scaffold the metadata:

```bash
go run . &
SERVER_PID=$!

mcp-runtime server init workspace-demo \
  --from-server http://localhost:8088
# Discovered: aaa-ping, add, create_task, draft_release_note, echo, lower, slugify, upper

kill $SERVER_PID
```

Validate the metadata, build the image, push it, and deploy:

```bash
mcp-runtime server validate --metadata-dir .mcp

mcp-runtime server build image workspace-demo --tag v1
# Prints the exact image ref, e.g.: registry.mcpruntime.org/myteam/workspace-demo:v1

mcp-runtime server push \
  --image registry.mcpruntime.org/myteam/workspace-demo:v1 \
  --scope tenant

mcp-runtime server deploy workspace-demo --scope tenant --metadata-dir .mcp
```

`server push` publishes the local image through the authenticated platform API.

Confirm it is running:

```bash
mcp-runtime server list
```

## 4. Grant access and connect

Create a grant that allows an agent to call `echo` and `add`. Replace `myteam`
with your actual team slug. First choose an active agent ID from your team's
directory:

```bash
mcp-runtime agent list myteam --status active
# If your team has no suitable agent, ask a team owner to create one.
AGENT_ID=agt_01arz3ndektsv4rrffq69g5fav # replace with the ID from the list
```

Agent display names such as `Cursor` are not IDs. Use the immutable ID returned
by `mcp-runtime agent create` or shown by `mcp-runtime agent list`. Members see
only agents covered by an applicable grant or their own active session; ask a
team owner for an ID if the list is empty.

```bash
mcp-runtime access grant init workspace-cursor \
  --server workspace-demo \
  --namespace mcp-team-myteam \
  --agent-id "$AGENT_ID" \
  --tool echo \
  --tool add \
  --output grant.yaml

mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml
mcp-runtime access grant apply --file grant.yaml
```

With that grant applied, start the adapter proxy. It enrolls a session-bound
client certificate and refreshes it before expiry. The platform issues the
session only when an enabled grant matches the server, the signed-in user, and
the agent, so the grant has to exist first:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/workspace-demo/mcp \
  --server workspace-demo \
  --agent "$AGENT_ID" \
  --auto-refresh \
  --listen 127.0.0.1:8099
```

Point **Claude Desktop**, **Cursor**, or any MCP client at `http://127.0.0.1:8099`.
This quickstart does not configure `spec.auth`, so the route uses the adapter
certificate without an OAuth bearer. Call the `echo` or `add` tool. The
allow-list policy created by `server init` checks the grant and required session
on each tool call. If you enable OAuth for the server, the client must send its
bearer through the adapter; see [Agent adapters](agent-adapters.md).

## 5. See it in the analytics

Open [platform.mcpruntime.org](https://platform.mcpruntime.org), go to
**Analytics → Tools** tab. You will see your tool calls broken down by
user, team, agent, call count, and allow/deny.

In **Server Catalog** or **My Activity**, confirm the deployed server is
visible to your account. Open its details to review the endpoint and connect
configuration. The platform UI uses the same deployment and policy state as
the CLI; it does not require a separate server install.

## What's next

- [Concepts](concepts.md): understand Grants, Sessions, Trust levels, and Side effects
- [Publish an MCP Server](publish-mcp-server.md): full build, push, deploy guide
- [Getting Started](getting-started.md): self-host MCP Runtime on your own Kubernetes cluster
- [CLI reference](cli.md): every command with flags and examples
