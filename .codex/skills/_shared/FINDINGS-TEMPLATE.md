# MCP Runtime — Security Findings Template

Shared by `security-audit` modes (`pr`, `platform`, `supply-chain`, `k8s`) and
other skills that report structured findings. Use this template for every reported finding so two
auditors converge on severity and a maintainer can act without follow-up.

## Severity rubric

Use one of these labels. Pick the highest that applies; do not invent new
levels.

- **Critical** — Unauthenticated remote code execution, full cluster takeover,
  cross-tenant data access, signing-key or admin credential leak in code or
  logs, or any default-deploy path that is exploitable from the public ingress.
- **High** — Authenticated privilege escalation, missing auth on an admin
  endpoint, gateway policy bypass, secret material reachable by an unprivileged
  pod, RBAC `*` verbs on `secrets`/`pods/exec`, container running as root with
  hostPath/hostNetwork, supply-chain action with `pull_request_target` checking
  out untrusted HEAD, or a CVE with public exploit reachable in the runtime
  binary.
- **Medium** — Missing security header, weak TLS config, log-level secret
  leakage at debug, denial-of-service that requires sustained traffic, missing
  NetworkPolicy on a sensitive namespace, action pinned by tag instead of SHA,
  CVE in a dependency that the binary reaches but with no public exploit.
- **Low** — Defense-in-depth gap, hardening recommendation, missing
  `readOnlyRootFilesystem`, unbounded but rate-limited input, license-only
  finding without runtime impact.
- **Info** — Observation, code-quality note, link to upstream guidance. Use
  sparingly; do not pad reports with these.

When in doubt between two levels, pick the higher one and explain the
mitigation that drops it down.

## Finding template

Use this exact shape per finding. One file/line per finding; if the same root
cause reappears, list additional locations under `Other locations`.

```
[SEV-{Critical|High|Medium|Low|Info}] <short title, imperative>
Component: <operator | gateway | platform-api | runtime-api | analytics-api | ui | ingest |
            processor | mcp-gateway | registry | traefik | crd | ci |
            other>
Location:  <path>:<line>            # use N/A only for design-level findings
CWE:       CWE-XXX                  # omit if no clean mapping
References: <CIS k8s 5.x.x | OWASP Top 10 | repo doc path | upstream advisory>

Description
  What is wrong. One paragraph. No restating the code; describe the defect.

Evidence
  Minimal repro: scanner output line, curl command + response, kubectl
  output, or a 5-line code excerpt. Redact secrets.

Impact
  What an attacker or operator gets. State the trust boundary crossed
  (anonymous → user, user → admin, tenant A → tenant B, pod → node, …).

Exploitability
  Preconditions, attacker profile, complexity. Note whether the default
  deploy is affected vs only a non-default config.

Recommendation
  Specific code or config change with file:line. Prefer "do X at services/
  api/foo.go:123" over "validate input." Include a regression test idea.

Status
  Open | Fixed in <commit> | Accepted risk (<rationale>, owner)

Other locations (optional)
  - <path>:<line>
  - <path>:<line>
```

## Reporting structure

A full audit report should have these sections, in this order:

1. **Summary** — scope, mode (change-scoped vs platform-wide), commit SHA,
   cluster context if any, count of findings by severity.
2. **Threat model assumptions** (platform audits only) — components reviewed,
   attacker profiles considered, what was explicitly out of scope.
3. **Findings** — one block per finding, ordered by severity then component.
4. **Scanner output** — raw failures from gosec, gitleaks, Trivy, govulncheck,
   kube-linter, etc., kept separate from manual findings to avoid double
   counting.
5. **Checks run** — exact commands, pass/fail, duration when relevant.
6. **Checks skipped** — what and why (missing tool, no live cluster, out of
   scope), so a follow-up auditor knows what is still pending.
7. **Remediation plan** — recommended order to fix, grouped by severity, with
   a verification command per item.

## Verification on remediation

When a fix lands, re-run the exact scanner command from the finding (record it
in `Evidence`) and the regression test from `Recommendation`. Update the
finding `Status` to `Fixed in <commit>` and link the PR.
