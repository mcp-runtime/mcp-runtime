---
name: security-audit
description: Audit a single MCP Runtime change for security risks and choose the relevant local or CI security checks. Use when Codex reviews or changes authentication, authorization, secrets, TLS, ingress, registry/image handling, Kubernetes RBAC or pod security, gateway policy, audit logging, dependencies, GitHub Actions, container builds, or any code path that accepts untrusted input. For a deep platform-wide audit prefer security-audit-platform; for SBOM/image/dependency-only audits prefer supply-chain-audit; for cluster RBAC/PSS/NetworkPolicy hygiene prefer k8s-hardening-audit.
---

# Security Audit (change-scoped)

## Overview

Use this skill to perform a focused, diff-driven security review for an MCP
Runtime change. Combine manual threat review with the repository's existing
scanners, and keep findings grounded in concrete file and line references.

If the user asks for a deep, platform-wide audit, switch to
`security-audit-platform`. For SBOM, signed images, GitHub Actions, or
dependency-only work, use `supply-chain-audit`. For RBAC, Pod Security
Standards, NetworkPolicy, and manifest hygiene, use `k8s-hardening-audit`.

When a requested review includes API, CRD, CLI, or system design choices, also
read [the shared design principles](../_shared/design-principles.md). Load it
only for design questions; ordinary change-scoped security reviews do not need it.

## Workflow

1. Define the security boundary.
   - Inspect `git status --short`, `git diff --stat`, and diffs for touched
     files.
   - Identify whether the change affects trust boundaries: user input,
     JSON/YAML parsing, HTTP handlers, auth cookies or headers, Kubernetes
     API calls, shell commands, image references, registry endpoints,
     ingress hosts, TLS material, audit events, or CI workflows.
   - Read nearby tests and existing implementations before judging a
     pattern unsafe.

2. Review MCP Runtime security invariants for the changed surface.
   - Secrets must come from Kubernetes Secrets, environment references,
     local test fixtures, or documented dev-only setup; do not hardcode
     real credentials or log token values. `PLATFORM_ADMIN_PASSWORD` is
     bootstrap-only and must not remain in steady-state env.
   - Admin and platform APIs must enforce the intended role, namespace,
     grant, session, and trust checks before mutating resources or
     returning sensitive data. The check is the `requireRole(roleAdmin, …)`
     wrap in each split service `routes.go`; absence on a mutating handler is a
     finding.
   - Gateway policy denies by default, scopes decisions to the rendered
     grant/session policy, and emits audit events for allow and deny paths
     without leaking credentials.
   - Public ingress and TLS changes preserve the documented
     `registry.<domain>`, `mcp.<domain>`, and `platform.<domain>` split;
     do not expose Grafana or Prometheus on the public platform host
     without an auth-aware design.
   - Registry and image changes respect dev HTTP versus production TLS
     behavior, image-pull-secret handling, and the single supported
     `registry/registry-cert` owner for `registry/registry-tls`.
   - Kubernetes manifests keep RBAC narrow, avoid privileged pods unless
     explicitly justified, preserve restricted pod-security labels in
     repo-owned namespaces, and avoid broad secret access.
   - HTML, JavaScript, logs, and API responses escape untrusted content
     and avoid reflecting secrets, auth headers, session cookies, JWTs,
     OIDC tokens, or API keys.
   - `services/ui/session_proxy.go` translates the HttpOnly `mcp_ui_session`
     cookie into the caller's stored bearer/API-key credential for an
     explicit GET-only path allowlist (`sessionProxyRuntimePrefixes`,
     `sessionProxyAnalyticsPrefixes`) — any change widening that allowlist,
     or any new cookie-backed route that accepts a non-GET method, needs a
     CSRF/anti-forgery check (token or equivalent) as a first-class finding
     if one isn't present; a cookie-authenticated write with no CSRF
     defense beyond `SameSite` is at least a Medium finding, High if the
     action is destructive.
   - GitHub Actions and dependency changes keep actions pinned to commit
     SHAs, minimize token permissions, and avoid exposing repository or
     deploy secrets to untrusted PR code.

3. Choose checks that match the risk. Run the smallest set that covers
   the changed surface; missing tools become a recorded blocker, not a
   silent skip.
   - **Secret scan:** `pre-commit run gitleaks --all-files` when
     pre-commit is available, or
     `gitleaks detect --source . --config .gitleaks.toml --redact` with
     the CLI.
   - **Go SAST:** install the pinned CI version once with
     `go install github.com/securego/gosec/v2/cmd/gosec@v2.26.1` (verify
     against `.github/workflows/security-gosec.yaml`), then
     `$(go env GOPATH)/bin/gosec ./...`.
   - **Vulnerable Go deps:** `go install golang.org/x/vuln/cmd/govulncheck@latest`,
     then `govulncheck ./...` from the repo root and from each touched
     `services/<name>/`. Unlike gosec this catches reachable CVEs.
   - **Trivy repo scan:** mirror CI with
     `bash hack/trivy-sentinel-images.sh` (builds + scans all sentinel images;
     stdlib CVEs require image/gobinary scan, not `go.mod` alone).
     For filesystem-only: `trivy fs --scanners vuln,secret,license,misconfig --severity CRITICAL,HIGH --ignore-unfixed --skip-dirs inspirations/mcp-gateway-registry .`.
   - **Dependency or workflow change:** review
     `.github/workflows/dependency-review.yaml`, Go module diffs, and any
     new action permissions or secret references; if the diff adds a
     workflow, confirm `permissions:` is minimal and triggers do not
     include `pull_request_target` plus a checkout of untrusted HEAD.
   - **Auth, policy, or gateway change:** run the nearest unit tests plus
     the governance or smoke-auth E2E path
     (`KUBECONFIG="$HOME/.kube/test-mcp-runtime-config" E2E_SCENARIOS=smoke-auth,governance bash test/e2e/qa-e2e.sh`)
     when the live request path changed. Confirm that kubeconfig resolves to
     `test-mcp-runtime` before running it; never let this command inherit a
     production context.

4. Report using the shared finding format.
   - Use the template and severity rubric in
     `../_shared/FINDINGS-TEMPLATE.md`.
   - Lead with findings ordered by severity. Include file and line,
     exploit path, impact, and a specific fix.
   - Separate scanner failures from manual findings.
   - If no issues are found, say that directly and list the checks run
     plus any checks skipped because tooling or cluster prerequisites
     were unavailable.
   - When the change touches platform-wide concerns (auth surface,
     ingress/TLS posture, RBAC, supply chain), note that
     `security-audit-platform`, `k8s-hardening-audit`, or
     `supply-chain-audit` would extend coverage.
