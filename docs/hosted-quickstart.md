# Explore the public reference platform

<span id="hosted-quickstart"></span>

[platform.mcpruntime.org](https://platform.mcpruntime.org) is MCP Runtime's
public reference deployment. With a user account and team access, you can
publish and deploy a sample MCP server, create grants, connect a client, and
explore the user and team views. You do not need your own Kubernetes cluster.
Allow about 10 minutes.

This platform is for getting a feel for the user and team workflows. To try
platform setup and administration, install MCP Runtime in your own cloud or
on-premises environment; see [Platform Installation](self-hosting.md).

One limit: the public platform cannot verify which agent makes the example
tool calls, so it denies them. You can still try the deployment, grant,
connection, and dashboard workflows below.

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
The CLI binary and the public reference platform are
separate release tracks: the CLI changes only when a new release is published;
the UI at `platform.mcpruntime.org` is updated by its operator and
does not need a local install.

## 2. Log in

Use an account on the [public reference platform](https://platform.mcpruntime.org).
You need team access to deploy the sample server. Ask for a user account and
team membership if you do not have them; this walkthrough does not require a
platform administrator account.

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

With the grant applied, start the adapter proxy to connect an MCP client to
your server. The grant must exist before the platform can issue the agent's
session:

```bash
mcp-runtime adapter proxy \
  --runtime-url https://mcp.mcpruntime.org/workspace-demo/mcp \
  --server workspace-demo \
  --agent "$AGENT_ID" \
  --auto-refresh \
  --listen 127.0.0.1:8099
```

Point **Claude Desktop**, **Cursor**, or any MCP client at `http://127.0.0.1:8099`.
The client can connect on the public platform. If it calls `echo` or `add`,
the platform denies the call because it cannot verify which agent made it. The
error is `401 missing_identity`. See [Client Connections](connect-clients.md)
for the identity setup needed to allow these calls on your own installation.

## 5. See it in the analytics

Open [platform.mcpruntime.org](https://platform.mcpruntime.org) and go to
**Analytics → Tools**. You can inspect recorded calls and their decisions. Calls
blocked because the platform could not verify the agent appear as denied.

In **Server Catalog** or **My Activity**, confirm the deployed server is
visible to your account. Open its details to review the endpoint and connect
configuration. The UI uses the same deployment and policy state as the CLI.

The console sidebar groups pages under **Runtime**, **Workspace**, and
**Platform**, according to your account's access. Use **Filter navigation** to
find a page and the account control at the bottom to view your account or sign
out. On compact screens, open the navigation menu in the header. The theme
button switches between dark and light mode.

## What's next

- [Concepts](core-concepts.md): understand Grants, Sessions, Trust levels, and Side effects
- [Publish an MCP Server](publish-mcp-server.md): full build, push, deploy guide
- [Platform Installation](self-hosting.md): install MCP Runtime in your own cloud or on-premises environment
- [CLI reference](cli-reference.md): every command with flags and examples
