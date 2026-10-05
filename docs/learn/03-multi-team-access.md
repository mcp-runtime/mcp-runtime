# Module 3: Multi-team access

<span id="module-3-multi-team-production-setup"></span>

Set up two teams in separate namespaces, share one server between them, and
control cross-team access with explicit grants. Production deployments use
the same isolation model. This module requires MCP Runtime in your own
environment; the public reference platform is for user and team workflows,
not platform administration. Start with [Platform Installation](../self-hosting.md)
if you have not installed it yet.

**Prerequisites:**

- Module 2 completed (you have deployed a server and understand grants)
- Platform admin credentials for your own installation
- Git, Go `1.26+`, and Docker for the example build
- Adapter-certificate identity enabled on the platform for successful tool calls

MCP Runtime is alpha software. This walkthrough covers team namespaces,
cross-team grants, and scoped sessions, but it does not certify a deployment
for production. Run the setup through your own security review before you use
it for real workloads.

## What we are building

```
Team Acme owns:   payments server  (namespace: mcp-team-acme)
Team Globex owns: a managed agent  (team namespace: mcp-team-globex)

Acme grants a managed agent in Globex access to the payments server's `echo`
and `add` tools.
```

Servers live in team namespaces. Grants carry the team ID, which tells the
gateway which team the agent is acting for.

## Step 1: Create two teams (admin)

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email admin@example.com --password '...' \
  --profile admin

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team create acme --name "Acme Corp"
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user create acme \
  --username alice@acme.com --password 'alice123' --role owner

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team create globex --name "Globex Corp"
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user create globex \
  --username bob@globex.com --password 'bob456' --role member

MCP_PLATFORM_API_PROFILE=admin mcp-runtime agent create globex \
  --name "Cursor assistant"
```

Record the `agt_...` ID printed for the new Globex agent. This walkthrough uses
that managed agent ID in the grant and adapter commands. Agent display names
are not IDs.

Verify:

```bash
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team list
```

## Step 2: Alice deploys payments server (Acme)

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email alice@acme.com --password 'alice123' \
  --profile alice
mcp-runtime auth use alice
```

Scaffold metadata from the running server, validate, build, push, deploy:

```bash
cd examples/oauth-example-go-2025-11-25
go run . &
SERVER_PID=$!
# Wait for the listening log and confirm http://localhost:8088/health.
mcp-runtime server init payments --from-server http://localhost:8088
kill $SERVER_PID

mcp-runtime server validate --metadata-dir .mcp
mcp-runtime server build image payments --tag v1
mcp-runtime server push --image registry.example.com/acme/payments:v1 --scope tenant
mcp-runtime server deploy payments --scope tenant --metadata-dir .mcp
```

Confirm:

```bash
mcp-runtime server list
# NAME      NAMESPACE      READY   STATUS
# payments  mcp-team-acme  1/1     Ready
```

## Step 3: Get Globex's team ID and agent ID

Cross-team grants need Globex's stable team ID; the slug does not work. Copy
the ID from **Teams → Globex** in the platform dashboard. `mcp-runtime team
list` shows the slug, display name, and namespace, but not the team ID. The
agent ID is the one printed by `agent create` above; you can list active agents
with:

```bash
MCP_PLATFORM_API_PROFILE=admin mcp-runtime agent list globex --status active
```

Set the following values to the IDs returned by the platform:

```bash
GLOBEX_TEAM_ID=replace-with-globex-team-id
AGENT_ID=agt_01arz3ndektsv4rrffq69g5fav # replace with the Globex agent ID
```

## Step 4: Alice grants Globex access to payments

```bash
mcp-runtime auth use alice

mcp-runtime access grant init payments-to-globex \
  --server payments \
  --namespace mcp-team-acme \
  --team-id "$GLOBEX_TEAM_ID" \
  --agent-id "$AGENT_ID" \
  --expires-in 4h \
  --tool echo \
  --tool add \
  --output grant-cross.yaml

# Validate the grant matches the server metadata
mcp-runtime server validate --metadata-dir .mcp --grant-file grant-cross.yaml

mcp-runtime access grant apply --file grant-cross.yaml
mcp-runtime access grant list --namespace mcp-team-acme
```

The grant lives in Acme's namespace, next to the server, and is scoped to
Globex's team ID. It matches the selected Globex agent and team; other Globex agents do not
match this grant.

## Step 5: Bob connects through the adapter (Globex)

The adapter asks the platform to create or reuse Bob's session for the granted
agent. You do not need a separate session manifest for this flow.

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email bob@globex.com --password 'bob456' \
  --profile bob
mcp-runtime auth use bob

mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/payments/mcp \
  --server payments \
  --agent "$AGENT_ID" \
  --auto-refresh \
  --listen 127.0.0.1:8099 &
```

Connect Claude Desktop or any MCP client to `http://127.0.0.1:8099`.
Bob can call `echo` and `add` on Acme's payments server; the gateway
enforces the cross-team grant.

## Step 6: Verify isolation

Alice's payments server denies callers without a grant. To confirm:

1. Alice disables the grant (Bob cannot administer Acme’s grant):

   ```bash
   MCP_PLATFORM_API_PROFILE=alice mcp-runtime access grant disable payments-to-globex \
     --namespace mcp-team-acme
   ```
2. Wait about 10 seconds for the gateway to load the updated policy, then Bob's
   next tool call is denied.
3. Alice restores access with `MCP_PLATFORM_API_PROFILE=alice mcp-runtime
   access grant enable payments-to-globex --namespace mcp-team-acme`.

Namespace isolation gives Bob no Kubernetes RBAC access to Acme's namespace.
The gateway enforces the grant; network policy alone does not.

## Step 7: See cross-team traffic in analytics

Open the platform dashboard → **Analytics → Tools**.

The event records distinguish server ownership from caller identity:

- User: `bob@globex.com`
- Server/resource team (`team_id`): Acme
- Caller team (`subject_team_id`): Globex
- Agent: `$AGENT_ID` (the directory also shows its display name)
- Server: `payments` (Acme's server)

Every call shows which team made it, which server it hit, and whether it was
allowed.

## What you have built

- Two isolated team namespaces with RBAC and NetworkPolicy
- A server owned by one team, accessed by another via an explicit grant
- A revocable, time-limited session that records the consuming team's identity
- A full audit trail of cross-team tool calls

## Production checklist before going live

- [ ] TLS enabled (`--with-tls --tls-cluster-issuer ...`)
- [ ] Registry mode `bundled-https` or external registry
- [ ] Sessions have reasonable `--expires-in` (4h–24h for day-to-day work)
- [ ] Admin credentials rotated from default
- [ ] `cluster diagnostics` passes all post-setup checks
- [ ] Troubleshooting page bookmarked: [Troubleshooting](../troubleshooting.md)

**You have completed the MCP Runtime learning path.**

- [CLI reference](../cli-reference.md): every command
- [API reference](../api-reference.md): CRD fields
- [Troubleshooting](../troubleshooting.md): common errors
- [Contribute](../contributor/README.md): help build MCP Runtime
