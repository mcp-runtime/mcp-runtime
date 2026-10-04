# Quickstart

Deploy a governed MCP server on the live
[platform.mcpruntime.org](https://platform.mcpruntime.org) instance. You do not
need a Kubernetes cluster. It takes about 10 minutes. Login, image publish,
deploy, grants, and adapter certificate enrollment work on that instance.
A non-OAuth allow-list `tools/call` is identified only when the operator has
set `MCP_ADAPTER_CERTIFICATES=true`; the live instance leaves that off, so the
gateway denies the call with `missing_identity`.

To self-host MCP Runtime on your own cluster, see [Getting Started](self-hosting.md).

## 1. Install the CLI

=== "macOS"

    ```bash
    curl -fsSL https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.sh | MCP_RUNTIME_OS=darwin sh
    ```

    The installer detects your CPU and installs the binary to `~/.local/bin`.

=== "Linux"

    ```bash
    curl -fsSL https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.sh | MCP_RUNTIME_OS=linux sh
    ```

    The installer detects your CPU and installs the binary to `~/.local/bin`.

=== "Windows (amd64)"

    ```powershell
    irm https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.ps1 | iex
    ```

    The installer uses a user-local directory and adds it to your user `PATH`.
    Open a new terminal after installation.

Pin a specific release by setting `MCP_RUNTIME_VERSION` to its tag before
running the installer.

The macOS/Linux installer logs detection, download, and installation steps,
shows download progress, and retries failed transfers. Color is automatic in a
terminal; set `NO_COLOR=1` to disable it.

Browse all binaries on the
[latest GitHub release](https://github.com/mcp-runtime/mcp-runtime/releases/latest).
If macOS or Linux cannot find `mcp-runtime`, add `~/.local/bin` to your shell's
`PATH`, for example with `export PATH="$HOME/.local/bin:$PATH"`.

Verify:

```bash
mcp-runtime --version
```

To upgrade the CLI, run the installer again and verify `mcp-runtime --version`.
The CLI binary and the hosted platform are
separate release tracks: the CLI changes only when a new release is published;
the platform UI at `platform.mcpruntime.org` is updated by its operator and
does not need a local install.

## 2. Log in

Get credentials from the [live platform](https://platform.mcpruntime.org) or use
an existing account. You need a team and a user account. Ask your platform
admin, or [self-host MCP Runtime](self-hosting.md) to create your own.

```bash
mcp-runtime auth login \
  --api-url https://platform.mcpruntime.org \
  --email you@example.com --password '...' \
  --profile me

mcp-runtime auth status    # confirm the profile is active
```

## 3. Deploy an example server

This example needs Git, Go `1.26+`, and Docker with its daemon running.
These are example build prerequisites; installing the release CLI does not
require Go.

Clone the repo to get the example server source:

```bash
git clone https://github.com/mcp-runtime/mcp-runtime
cd mcp-runtime/examples/oauth-example-go-2025-11-25
```

Run it locally to discover its tool names, then scaffold the metadata:

```bash
go run . &
SERVER_PID=$!
# Wait for the listening log and confirm http://localhost:8088/health.

mcp-runtime server init workspace-demo \
  --from-server http://localhost:8088
# Discovered: aaa-ping, add, create_task, draft_release_note, echo, lower, slugify, upper, whoami

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
This quickstart does not configure `spec.auth`. The adapter still enrolls a
session certificate when the platform has a workload issuer. The gateway uses
that certificate as the caller identity only after the operator sets
`MCP_ADAPTER_CERTIFICATES=true`, which makes Traefik verify it and assert the
SPIFFE id. Until then a plain Ingress does not ask for a client certificate,
`initialize` succeeds, and an allow-list `tools/call` returns `401`
`missing_identity`. With the setting on, call `echo` or `add`. The allow-list
policy from `server init` checks the grant and required session on each tool
call. If you enable OAuth for the server, the client must also send its bearer
through the adapter; see [Agent adapters](connect-clients.md).

## 5. See it in the analytics

Open [platform.mcpruntime.org](https://platform.mcpruntime.org), go to
**Analytics → Tools** tab. You will see your tool calls broken down by
user, team, agent, call count, and allow/deny.

In **Server Catalog** or **My Activity**, confirm the deployed server is
visible to your account. Open its details to review the endpoint and connect
configuration. The platform UI uses the same deployment and policy state as
the CLI; it does not require a separate server install.

## What's next

- [Concepts](core-concepts.md): understand Grants, Sessions, Trust levels, and Side effects
- [Publish an MCP Server](publish-mcp-server.md): full build, push, deploy guide
- [Getting Started](self-hosting.md): self-host MCP Runtime on your own Kubernetes cluster
- [CLI reference](cli-reference.md): every command with flags and examples
