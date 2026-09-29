---
name: production-platform
description: Operate and configure public MCP Runtime installs — k3s deploy/update, registry TLS/auth, readiness, CLI/UI validation, MCP_PLATFORM_DOMAIN, Let's Encrypt, and registry/mcp/platform hostnames. Use for production or Staging E2E disposable-VM work, --with-tls/ACME, DNS, or registry forward-auth — not for local Kind test-mode (use contributor-cluster).
---

# Production Platform

Public / production Kubernetes operations and hostname/TLS configuration.
Current public deployment uses k3s. Staging E2E on the disposable VM is the
deterministic strict-prod install gate (`docs/contributor/staging-e2e.md`).

## Modes

| Mode | Load |
|------|------|
| Deploy, rollout, registry debug, Staging E2E, multitenancy | [references/operations.md](references/operations.md) |
| Domain, ACME/TLS, hostname model, OIDC/Google | [references/public-configuration.md](references/public-configuration.md) |

## Non-negotiables (short)

- Never leave a production context as the current kube context; use an explicit
  per-command `KUBECONFIG=<prod file>`.
- Staging E2E never targets production; disposable-VM guards must pass.
- Prefer CLI/UI product paths over hand `kubectl` mutations.

## Related

- Kind contributor cluster → `contributor-cluster`
- Failure playbooks on Kind or k3s → `cluster-ops` mode `troubleshoot`
- Security review of prod-facing changes → `security-audit`
