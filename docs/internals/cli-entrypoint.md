# CLI Entrypoint

Package `cmd/mcp-runtime` builds the user-facing `mcp-runtime` binary. It should
stay thin: initialize logging, assemble Cobra commands, execute the root command,
and exit with a clear status.

Useful reference command:

```bash
go doc -cmd ./cmd/mcp-runtime
```

## Responsibilities

- Define build metadata variables (`version`, `commit`, `date`) that release
  builds can set with linker flags.
- Create the root Cobra command and global flags.
- Register subcommands through the foldered `internal/cli/root` routing layer.
- Configure a console-oriented zap logger.
- Print command errors to stderr and return a non-zero process exit.

The entrypoint should not contain business logic for command behavior. Route
top-level commands through `internal/cli/root`. Command folders should own Cobra
wiring and, where already migrated, package-local managers; shared CLI-only
infrastructure lives in `internal/cli/core`.

## Command Tree

The root command wires these internal command groups:

| Command | Routing package | Behavior files |
|---|---|---|
| `bootstrap` | `internal/cli/bootstrap` | `bootstrap.go` |
| `catalog` | `internal/cli/catalog` | `catalog.go` |
| `cluster` | `internal/cli/cluster` | `cluster.go`, `manager.go`, `doctor.go`, `register.go`, doctor implementation under `internal/cli/cluster/doctor/`, … |
| `setup` | `internal/cli/setup` | `setup.go`, `providers.go`, setup workflow under `internal/cli/setup/platform/`, setup-owned helpers under `internal/cli/setup/` |
| `status` | `internal/cli/status` | `status.go`, shared workload/probe helpers in `internal/cli/platformstatus` |
| `registry` | `internal/cli/registry` | `registry.go`, `manager.go`, `defaults.go`, registry-owned helpers under `internal/cli/registry/` |
| `server` | `internal/cli/server` | `server.go`, `manager.go`, `validation.go`, `build.go`, `build_image.go`, server-owned helpers under `internal/cli/server/` |
| `access` | `internal/cli/access` | `access.go`, `manager.go`, `validation.go` |
| `agent` | `internal/cli/agent` | `agent.go` |
| `adapter` | `internal/cli/adapter` | `adapter.go`, `flags.go`, `platformsession.go`, `proxy.go`, `enroll.go`; transport behavior in `internal/agentadapter` |
| `auth` | `internal/cli/auth` | `auth.go` |
| `sentinel` | `internal/cli/sentinel` | `sentinel.go`, `manager.go`, `grafana.go`; admin-only status, logs, events, port-forward, restart, and Grafana recovery across the platform-owned namespaces (`mcp-runtime`, `mcp-platform`, `mcp-observability`, `mcp-log-collector`), using shared workload/probe helpers in `internal/cli/platformstatus` |
| `team` | `internal/cli/team` | `team.go`, `manager.go` |
| `update` | `internal/cli/update` | `update.go`, `plan.go`, `apply.go`, `build.go`, `crds.go`, `output.go` |
| `admin` | `internal/cli/admin` | `admin.go` (hidden; operator-only kubectl helpers such as `admin registry push`) |

When adding a command, wire it here only after the implementation has focused
package tests and help text is ready for golden snapshots.

## Contributor Contract

CLI UX changes should preserve these expectations:

- Help text is accurate and reflected in `test/golden/cli/testdata`.
- Errors are human-readable and still return non-zero exit codes.
- Logs are readable in terminals and CI.
- Global flags stay minimal; feature-specific flags belong on their command.
- Commands that shell out to external tools are testable through runner
  abstractions in `internal/cli/core`.
- Top-level command folders under `internal/cli/<command>` should keep Cobra
  wiring thin and delegate to package-local managers or explicit shared
  services.

Before changing this package, run:

```bash
go test ./cmd/mcp-runtime ./internal/cli/... ./test/golden/... -count=1
```
