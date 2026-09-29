---
name: contributor-cluster
description: Stand up or recover the Kind+test-mode MCP Runtime contributor cluster, then use local URLs, API keys, test logins, and port-forwards. Use when preparing a live Kind target for cluster-ops or dashboard-browser-qa, verifying setup --test-mode, curling localhost:18080/18443, or debugging test-mode 401s. Owns Kind cluster lifecycle; other live QA skills assume this has run.
---

# Contributor Cluster

Kind + `setup --test-mode` lifecycle and local endpoint cheatsheet. Prefer an
existing healthy `mcp-runtime` / `test-mcp-runtime` context; never tear down
without explicit user confirmation. Never use the production kubeconfig.

## Modes

| Mode | Load |
|------|------|
| Bring-up, reuse, repair, smoke MCP traffic | [references/setup.md](references/setup.md) |
| URLs, API keys, test logins, port-forwards | [references/local-development.md](references/local-development.md) |

Default: run **setup** until the cluster is healthy, then use **local-development**
for day-to-day curls and UI login.

## Preconditions

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
export KUBECONFIG="$TEST_KUBECONFIG"
```

Isolated contributor kubeconfig only. Details and step order live in the
references above; do not invent alternate Kind or setup paths.

## Related

- Live Kind ops / security / perf / failure debug → `cluster-ops`
- Grants and MCP JSON-RPC → `access-governance`
- Browser UI QA → `dashboard-browser-qa`
