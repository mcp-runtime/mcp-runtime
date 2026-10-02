#!/usr/bin/env bash
# Safe application-namespace wipe for the mcpruntime.org k3s cluster.
#
# Backs up platform-runtime material only (TLS, cert-manager ownership, platform
# ConfigMap/Secret bootstrap keys), then deletes MCP Runtime app namespaces.
# Intentionally does NOT preserve tenant/user data: platform Postgres identity
# store, MCPServer/grant/session CRs, team namespaces, registry images, or
# analytics history. Use this for a fresh platform reinstall / integration test.
#
# Preserved cluster infrastructure: kube-system (k3s Traefik), cert-manager.
# Only MCP Runtime namespaces are deleted (platform names, mcp-team-*, labeled ns).
#
# Typical fresh reinstall:
#   hack/deploy/mcpruntime-org/clean.sh --yes
#   hack/deploy/mcpruntime-org/setup.sh          # auto-restores platform backup when present
#
# Code-only iteration (no wipe): hack/deploy/mcpruntime-org/rollout.sh
#
# Set GOOGLE_CLIENT_ID (and OIDC_*) in config/deployments/mcpruntime-org.env before
# setup when browser sign-in must match the previous deployment.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/backup.sh
source "$SCRIPT_DIR/lib/backup.sh"
# shellcheck source=lib/clean.sh
source "$SCRIPT_DIR/lib/clean.sh"

usage() {
  cat <<'EOF'
Usage: hack/deploy/mcpruntime-org/clean.sh --yes [--restore-platform] [--no-backup] [--dry-run] [--wait]

  --yes               Required for destructive clean (namespace wipe).
  --restore-platform  Run hack/deploy/mcpruntime-org/restore.sh instead of wiping.
  --restore-tls       Alias for --restore-platform.
  --no-backup         Skip backup (not recommended on production TLS clusters).
  --dry-run           Print actions without deleting or writing backups.
  --wait              Wait for namespace deletion to finish (default: fire-and-forget).

Platform backup scope (platform functioning only):
  - TLS Secrets + cert-manager Certificate CRs + letsencrypt-prod ClusterIssuer
  - mcp-shared-config (OIDC, Traefik namespace, registry host, platform flags)
  - owner credential Secrets (API, UI, ingest, Grafana, Postgres)
  - mcp-auth connector/signing/TLS secrets and Keycloak admin/TLS secrets when present

NOT backed up (intentional reset on clean):
  - Tenant teams, platform users, issued API keys in Postgres
  - Keycloak realm/database state (export it with Keycloak before the reset)
  - MCPServer / MCPAccessGrant / MCPAgentSession CRs
  - Registry image blobs, ClickHouse/Tempo/Prometheus history

Environment (via config/deployments/mcpruntime-org.env):
  KUBECONFIG            cluster kubeconfig path (required)
  MCP_TLS_BACKUP_DIR    backup root (default: ~/.mcpruntime/backups/mcpruntime-org)
EOF
}

DO_BACKUP=1
DRY_RUN=0
RESTORE_PLATFORM=0
CONFIRMED=0
WAIT_NS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes)
      CONFIRMED=1
      shift
      ;;
    --restore-platform|--restore-tls)
      RESTORE_PLATFORM=1
      shift
      ;;
    --no-backup)
      DO_BACKUP=0
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --wait)
      WAIT_NS=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

mcpruntime_org_load_env 1
MCP_TLS_DRY_RUN="$DRY_RUN"

if [[ "$RESTORE_PLATFORM" == "1" ]]; then
  if [[ "$DRY_RUN" == "1" ]]; then
    exec "$SCRIPT_DIR/restore.sh" --dry-run
  fi
  exec "$SCRIPT_DIR/restore.sh"
fi

if [[ "$CONFIRMED" != "1" && "$DRY_RUN" != "1" ]]; then
  echo "error: destructive clean requires --yes (or use --dry-run to preview)" >&2
  usage >&2
  exit 1
fi

echo "kubeconfig: $KUBECONFIG"
mcpruntime_org_require_cluster

cat <<'WARN'

WARNING: This removes MCP Runtime namespaces and tenant/user workload data.
Unrelated cluster namespaces are preserved. Platform-runtime backups (TLS, OIDC,
bootstrap secrets) are saved when backup is enabled. Registry images, Postgres
platform users/teams, and MCP CRs are NOT preserved.

WARN

if [[ "$DO_BACKUP" == "1" ]]; then
  mcpruntime_org_backup_platform_runtime
else
  echo "skipping backup (--no-backup)"
fi

mcpruntime_org_clean_cluster_scoped_mcp "$DRY_RUN"
mcpruntime_org_clean_app_namespaces "$DRY_RUN" "$WAIT_NS"

cat <<EOF

Clean complete.

Next steps:
  1. Wait until no app namespaces are Terminating (or rerun clean with --wait).
  2. Ensure config/deployments/mcpruntime-org.env exports GOOGLE_CLIENT_ID (and OIDC_* if used).
  3. hack/deploy/mcpruntime-org/setup.sh
     (restores platform-runtime backup automatically when $MCP_TLS_BACKUP_ROOT/latest exists)
  4. kubectl -n mcp-platform rollout restart deployment/mcp-platform-api deployment/mcp-runtime-api deployment/mcp-ui
     kubectl -n mcp-observability rollout restart deployment/mcp-analytics-api
  5. hack/deploy/mcpruntime-org/multitenancy-test.sh   # PLATFORM_URL / MCP_URL / REGISTRY_HOST

For code-only changes without a wipe: hack/deploy/mcpruntime-org/rollout.sh

EOF
