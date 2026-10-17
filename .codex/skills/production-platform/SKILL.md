---
name: production-platform
description: Operate and configure public MCP Runtime installs — k3s deploy/update, registry TLS/auth, readiness, CLI/UI validation, MCP_PLATFORM_DOMAIN, Let's Encrypt, and registry/mcp/platform hostnames. Use for production or Staging E2E disposable-VM work, --with-tls/ACME, DNS, or registry forward-auth — not for local Kind test-mode (use contributor-cluster).
---

# Production Platform

Public / production Kubernetes operations and hostname/TLS configuration.
Current public deployment uses k3s. Staging E2E on the disposable VM is the
deterministic strict-prod install gate (`docs/contributor/staging-e2e.md`).

## Release deployment policy

Follow [the pre-customer release policy in AGENTS.md](../../../AGENTS.md#release-policy-before-the-first-external-customer).
Until the first external customer deployment, releases may break compatibility
and use backed-up fresh setup and verified recovery of the hosted reference
platform. Do not add legacy compatibility or general migration infrastructure
for earlier reference-platform releases. Existing production and Staging E2E
guardrails still apply.

Ordinary `mcp-runtime update` reconciles the target release's declared operator
RBAC before rolling images, with setup's SSA field manager, and explicitly
declared platform NetworkPolicies (including Kafka client isolation). Review the dry-run
object plan and use an unscoped update (`--only` skips platform objects). Unrelated
network policies are preserved. A separate RBAC
repair is not part of routine deploys. Run `cluster doctor` afterwards to verify
effective bundled permissions and forbidden informer errors. See
[the deployment runbook](../../../docs/reference-deployment.md) for optional auth,
SubjectAccessReview permissions and the intentional two-replica leader election.

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
