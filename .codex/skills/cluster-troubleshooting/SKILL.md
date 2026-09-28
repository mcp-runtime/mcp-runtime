---
name: cluster-troubleshooting
description: Debug MCP Runtime cluster, ingress, registry, Sentinel auth, MCPServer pods, and platform UI failures on Kind or k3s. Use when setup, doctor, e2e, or live traffic fails; when investigating 401/404/ImagePullBackOff/ACME/TLS/registry push errors; when a real MCP client (Cursor, Claude Desktop) cannot connect and you need its own logs; or when AGENTS.md points here for the full failure-mode checklist.
---

# Cluster Troubleshooting

## When to use

- Live cluster misbehaves after `setup`, upgrade, or config change
- Operator, gateway, registry, or Sentinel symptoms (not unit-test failures)
- Before re-running full `setup`, scan the checklist for a targeted fix

## First steps

Use the CLI and platform UI for supported management operations. A failure is
a product defect to reproduce, cover with regression tests, fix, and submit in
a focused PR. Do not bypass it with manual namespace/RBAC/Secret creation,
database edits, `kubectl` mutations, or direct API writes. Read-only diagnostics
remain available. Mutation recipes in the reference are for an explicitly
requested Kubernetes recovery/test path; they are not fallback permission for
failed CLI/UI flows. Re-run the original CLI/UI operation to validate the fix.

```bash
kubectl config current-context
./bin/mcp-runtime status
./bin/mcp-runtime cluster doctor
kubectl get ingress -A
kubectl get pods -A | grep -E 'mcp-|registry|traefik|cert-manager'
```

For Kind contributor clusters, keep the default context on `test-mcp-runtime`
and prefer reusing the `mcp-runtime` cluster — see
`.codex/skills/contributor-cluster-setup/SKILL.md`. Production uses the separate
`~/.kube/prod-mcp-runtime-config` file and `prod-mcp-runtime` context; pass it
explicitly for production commands, never as the default kubeconfig.

For public k3s / `mcpruntime.org` deploys, also read `.codex/skills/production-operations/SKILL.md` and `docs/cluster-readiness.md`.

For production incidents, inspect metrics, aggregated logs, and traces at
`https://platform.mcpruntime.org/grafana` for a shared incident window.
Read private credentials from `~/.mcpruntime/infra.env` without displaying
them. Check collection coverage and correlate request/trace IDs with client
logs. Follow the [production observability workflow](../../../docs/k3s-deployment-runbook.md#production-observability-and-debugging).
Find or create an actionable repository ticket for each concrete
maintainability or debugging gap and attach it to
[Maintainability and Debuggability Improvement](https://github.com/orgs/mcp-runtime/projects/1).

## Full checklist

Read **[reference.md](reference.md) end-to-end** before diagnosing (ingress, registry, cert-manager, ImagePullBackOff, UI redirect loops, registry push timeouts, k3s NetworkPolicy, duplicate Traefik, and more). Public TLS/DNS detail: `public-platform-configuration` skill.

## Client can't connect (Cursor, Claude Desktop)

When `curl` against the server succeeds but a real client fails, the client's own log is the
only place the real error appears — it does discovery, metadata schema validation, dynamic
client registration, and PKCE that curl does not.

```bash
D=$(ls -td ~/Library/Application\ Support/Cursor/logs/*/ | head -1)   # newest launch
tail -n 100 "$D"/mcp-server-user-*.log                                # Cursor: the live log
tail -n 100 ~/Library/Logs/Claude/mcp*.log                            # Claude Desktop
```

`mcp-server-user-<name>.log` **at the log-directory root** is the file that matters. The
`exthost/anysphere.cursor-mcp/MCP user-<name>.log` path is a legacy location that can sit
frozen for hours while the real log is live — trust mtime, not the filename.

Full recipe, the OAuth state machine, and symptom→cause table (SSE 404 red herring,
`subject_types_supported` schema rejection, consent-page 404, RFC 7591 echo, RFC 8707
`resource is not recognized`): **[reference.md](reference.md) → MCP client-side debugging**.

## MCPServer pod / gateway sidecar

When a server is deployed but grants, policy, or analytics look wrong:

```bash
SERVER=oauth-example-go-2025-11-25-gateway
CONTAINER=oauth-example-go-2025-11-25-gateway
NS=mcp-servers

kubectl get mcpservers -n "$NS"
POD="$(kubectl get pods -n "$NS" -l app="$SERVER" -o jsonpath='{.items[0].metadata.name}')"
kubectl describe pod -n "$NS" "$POD"
kubectl logs -n "$NS" "$POD" -c "$CONTAINER"
kubectl logs -n "$NS" "$POD" -c mcp-gateway
./bin/mcp-runtime server policy inspect "$SERVER" --namespace "$NS"
kubectl get mcpaccessgrant,mcpagentsession -n "$NS" -o wide
```

Sidecar container name is `mcp-gateway`. Many images are distroless — use logs/describe or:

```bash
kubectl debug -it -n "$NS" "pod/$POD" --target="$CONTAINER" --image=busybox:1.36 -- sh
```

Policy reload: the gateway sidecar polls its mounted policy file every 5-7s, and the operator stamps `mcpruntime.org/gateway-policy-revision` on server pods so the kubelet re-projects the ConfigMap immediately. Expect grants/sessions to apply within ~10s; a persistent `session_not_found` with a correct ConfigMap usually means the pod stamp failed (see `reference.md`).

## Clean start (keep cluster, wipe workloads)

Do not use a generic API-wide cleanup command here. It can delete every
namespaced and cluster-scoped resource in the selected cluster, including
production workloads, CRDs, and user data. Use a symptom-specific repair from
[reference.md](reference.md). If a fresh contributor cluster is required,
follow `contributor-cluster-setup`; any cluster deletion must target the explicit Kind
cluster and isolated test kubeconfig, and requires the user's approval.
