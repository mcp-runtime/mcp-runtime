#!/usr/bin/env bash
# Deliberate destructive reset. Never use for normal releases.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/deploy/mcpruntime-org/lib/backup.sh
source "$SCRIPT_DIR/lib/backup.sh"
# shellcheck source=hack/deploy/mcpruntime-org/lib/clean.sh
source "$SCRIPT_DIR/lib/clean.sh"
dry_run=0; yes=0; confirm=""; recovery=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1; shift ;;
    --yes) yes=1; shift ;;
    --confirm-cluster) confirm="${2:?Expected cluster UID}"; shift 2 ;;
    --recovery-snapshot) recovery="${2:?Expected snapshot path}"; shift 2 ;;
    --help|-h)
      echo 'Usage: clean.sh --dry-run | --yes --confirm-cluster UID --recovery-snapshot PATH'
      echo 'Reset deletes Runtime namespaces and their data. Requires a checksum-verified full backup from this cluster.'
      echo 'Rehearse database/application recovery first; live PVC copies are not consistent database exports.'
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
mcpruntime_org_load_env
mcpruntime_org_require_cluster
if [[ "$dry_run" == 0 ]]; then
  [[ "$yes" == 1 && "$confirm" == "$MCP_EXPECTED_CLUSTER_UID" && -n "$recovery" ]] || {
    echo 'Reset requires --yes, matching --confirm-cluster and --recovery-snapshot' >&2; exit 1;
  }
  python3 "$SCRIPT_DIR/lib/verify-snapshot.py" "$recovery" "$MCP_EXPECTED_CLUSTER_UID" --full
  # Retain a current credential snapshot without replacing latest full recovery data.
  MCP_TLS_DEFER_PUBLISH=1 mcpruntime_org_backup_platform_runtime
fi
mcpruntime_org_clean_cluster_scoped_mcp "$dry_run"
mcpruntime_org_clean_app_namespaces "$dry_run" 1
echo 'Reset completed. Follow the reviewed fresh-setup/recovery procedure; no automatic restore occurs.'
