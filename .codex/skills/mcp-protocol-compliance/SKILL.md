---
name: mcp-protocol-compliance
description: Validate MCP Runtime against the upstream Model Context Protocol when preparing a protocol version bump, auditing transport/JSON-RPC conformance, or comparing behavior to a pinned spec revision. Prefer CI, Kind QA E2E, and Staging E2E for routine regressions; use this skill for protocol bumps and SEP/spec gaps.
---

# MCP Protocol Compliance

Thin skill for **protocol bumps and spec audits**. Routine auth, gateway, and
transport regressions belong in CI, `test/e2e/qa-e2e.sh`, Staging E2E, or
`cluster-ops` — do not run the full conformance playbook on every PR.

## When to use

- Upstream MCP protocol version bump in the runtime
- Explicit ask to audit Streamable HTTP / JSON-RPC / capability shapes vs a
  pinned spec revision
- SEP or roadmap gap analysis before adopting a new protocol feature

## When not to use

- Ordinary PR CI / Kind smoke (use CI + `cluster-ops` mode `ops`)
- Auth/grant deny paths (use `cluster-ops` mode `security`)
- Ship gate (CI + Staging E2E)

## Workflow

1. Pin the upstream revision under audit.
2. Prefer existing automated coverage first; note what CI/E2E already proved.
3. Only then load the full playbook:
   [references/full-audit.md](references/full-audit.md)
   (and any sibling files under `references/` for live conformance detail).

## Related

- Live Kind ops/security → `cluster-ops`
- Threat-model / PR security → `security-audit`
