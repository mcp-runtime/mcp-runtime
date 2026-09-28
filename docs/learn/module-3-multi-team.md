# Module 3: Multi-team production setup

Set up two teams in separate namespaces, share one server between them, and
control cross-team access with explicit grants. Production deployments use
the same isolation model.

**Prerequisites:**
- Module 2 completed (you have deployed a server and understand grants)
- Admin credentials on the platform

MCP Runtime is alpha software. This walkthrough covers team namespaces,
cross-team grants, and scoped sessions, but it does not certify a deployment
for production. Run the setup through your own security review before you use
it for real workloads.

## What we are building

```
Team Acme owns:   payments server  (namespace: mcp-team-acme)
Team Globex owns: workspace server (namespace: mcp-team-globex)

Acme grants Globex's cursor agent access to payments/list_invoices
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
```

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
go run . &; SERVER_PID=$!
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

## Step 3: Get Globex's team UUID

Cross-team grants need the team UUID; the slug does not work. Get it from the admin API:

```bash
# List teams as admin to find the UUID
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team list
# Shows: globex  Globex Corp  mcp-team-globex

# The UUID comes from server get or is shown in access grant list output
# For now note it from: platform dashboard → Teams → Globex → copy UUID
# Or: mcp-runtime access session list will show teamID in output
```

## Step 4: Alice grants Globex access to payments

```bash
mcp-runtime auth use alice

mcp-runtime access grant init payments-to-globex \
  --server payments \
  --namespace mcp-team-acme \
  --team-id <globex-team-uuid> \
  --agent-id cursor \
  --tool echo \
  --tool add \
  --output grant-cross.yaml

# Validate the grant matches the server metadata
mcp-runtime server validate --metadata-dir .mcp --grant-file grant-cross.yaml

mcp-runtime access grant apply --file grant-cross.yaml
mcp-runtime access grant list --namespace mcp-team-acme
```

The grant lives in Acme's namespace, next to the server, and is scoped to
Globex's team ID. Only Globex's agents can use it.

## Step 5: Admin creates a session for Globex's agent

Session apply requires admin role:

```bash
mcp-runtime auth use admin

mcp-runtime access session init globex-payments-session \
  --server payments \
  --namespace mcp-team-acme \
  --team-id <globex-team-uuid> \
  --agent-id cursor \
  --trust low \
  --expires-in 4h \
  --output session-cross.yaml

mcp-runtime access session apply --file session-cross.yaml
mcp-runtime access session list
```

## Step 6: Bob connects via the adapter (Globex)

```bash
mcp-runtime auth use bob   # bob@globex.com

mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/payments/mcp \
  --server payments \
  --agent cursor \
  --auto-refresh \
  --listen 127.0.0.1:8099 &
```

Connect Claude Desktop or any MCP client to `http://127.0.0.1:8099`.
Bob can call `echo` and `add` on Acme's payments server; the gateway
enforces the cross-team grant.

## Step 7: Verify isolation

Alice's payments server denies callers without a grant. To confirm:

1. Have Bob try to call a tool without the grant:
   ```bash
   mcp-runtime access grant delete payments-to-globex --namespace mcp-team-acme
   ```
2. Bob's next tool call is denied.
3. Re-apply the grant to restore access.

Namespace isolation gives Bob no Kubernetes RBAC access to Acme's namespace.
The gateway enforces the grant; network policy alone does not.

## Step 8: See cross-team traffic in analytics

Open the platform dashboard → **Analytics → Tools**.

The rows show:
- User: `bob@globex.com`
- Team: `globex`
- Agent: `cursor`
- Server: `payments` (Acme's server)

Every call shows which team made it, which server it hit, and whether it was
allowed.

## What you have built

- Two isolated team namespaces with RBAC and NetworkPolicy
- A server owned by one team, accessed by another via an explicit grant
- A revocable, time-limited session carrying the consuming team's identity
- A full audit trail of cross-team tool calls

## Production checklist before going live

- [ ] TLS enabled (`--with-tls --tls-cluster-issuer ...`)
- [ ] Registry mode `bundled-https` or external registry
- [ ] Sessions have reasonable `--expires-in` (4h–24h for day-to-day work)
- [ ] Admin credentials rotated from default
- [ ] `cluster diagnostics` passes all post-setup checks
- [ ] Troubleshooting page bookmarked: [Troubleshooting](../troubleshooting.md)

**You have completed the MCP Runtime learning path.**

- [CLI reference](../cli.md): every command
- [API reference](../api.md): CRD fields
- [Troubleshooting](../troubleshooting.md): common errors
- [Contribute](../contributor/README.md): help build MCP Runtime
