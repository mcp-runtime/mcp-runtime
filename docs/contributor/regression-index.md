# Platform Regression Index

This page explains the incident-to-regression-check index for MCP Runtime
([#546](https://github.com/mcp-runtime/mcp-runtime/issues/546), part of
[#548](https://github.com/mcp-runtime/mcp-runtime/issues/548)). The data lives in
[`regression-index.yaml`](regression-index.yaml); this page documents the rules.

The index maps each confirmed platform bug to the automated check that keeps it
fixed. Tests stay in their normal package, integration, Kind QA E2E, or Staging
E2E locations; the index only links them.

## Status values

| Status | Meaning | Requirements |
|--------|---------|--------------|
| `covered` | A check proves the protected behavior. | At least one `checks` entry. |
| `partial` | Checks exist but leave part of the behavior unproven. | `checks`, `gap`, and a `waiver`. |
| `untested` | No automated check yet. | A `waiver`; no `checks`. |

A waiver records `owner`, `follow_up` (linked issue), `expires` (`YYYY-MM-DD`),
and `blocker`. Expired waivers fail CI, so an untested incident cannot stay
silently unmapped. Renew a waiver only with a stated reason. Never mark an
entry `covered` unless the mapped check really asserts the protected behavior.

Each check has `kind` (`go-test`, `e2e-scenario`, or `script`), `path`, `name`
(the Go test function, or the scenario/script marker that must appear in the
file), the exact run `command`, and `ci_job` as `<workflow>.yaml#<job>`.

## What CI enforces

`go test ./test/regression -count=1` fails when:

- an incident is `covered` or `partial` without a mapped check, or `untested` without a valid waiver;
- a waiver is incomplete or expired;
- a mapped check path, Go test function, or scenario name does not exist;
- `ci_job` does not name an existing job in `.github/workflows/`;
- this page does not mention a listed incident.

It runs in the `test` job (all unit tests) of `ci.yaml`, and as an explicit
step in `ci.yaml#lint` and `pre-release-regression.yaml#static`. It is
deterministic and offline: it never contacts a cluster.

## Adding a regression for a new bug

1. Add the narrowest meaningful test next to the code (or a Kind QA E2E
   scenario, or Staging E2E for production-like behavior).
2. Add or update the incident entry in `regression-index.yaml` with the check and run command.
3. Mention the incident (`#NNN`) in the bug-fix PR and in this index.

Cluster-level checks must use guarded Kind or disposable staging targets; see
[Staging E2E](staging-e2e.md) and `hack/cluster-ops/README.md`.

## Current coverage (initial backlog from #546)

| Incident | Protected behavior | Status |
|----------|--------------------|--------|
| #533 | Failed gateway rollout keeps last-good route | untested |
| #532 | Gateway-enabled TypeScript OAuth example | untested |
| #531 | Internal registry isolation for tenants | partial |
| #501 | OAuth refresh-token replay coordination | untested |
| #540 | Operator cannot read the workload CA key | untested |
| #538 | Workload CertificateRequest gating | partial (CSR SAN validation only) |
| #534 | Supported cert-manager version | untested |
| #535 | Workload CA lifecycle | untested |
| #543 | Grafana dashboard/datasource provisioning | untested |
| #146 | Audit reliability, bounded login state | untested |
| #71 | Messaging stack restart recovery | untested |
| #72 | Resource-pressure/eviction recovery | untested |

These are tracked gaps, not claims of coverage. The release gate and the
reporting of passed/failed/skipped/quarantined coverage remain open work on #546.
