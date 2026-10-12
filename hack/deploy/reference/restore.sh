#!/usr/bin/env bash
# Explicit configuration recovery, not database/PVC restoration.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/deploy/reference/lib/backup.sh
source "$SCRIPT_DIR/lib/backup.sh"
dry_run=0; yes=0; snapshot=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1; shift ;;
    --yes) yes=1; shift ;;
    --snapshot) snapshot="${2:?Expected snapshot path}"; shift 2 ;;
    --help|-h)
      echo 'Usage: restore.sh --snapshot PATH --dry-run | --snapshot PATH --yes'
      echo 'Restores selected TLS/config/credentials and restarts APIs; does not restore databases or volume contents.'
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$snapshot" && ( "$yes" == 1 || "$dry_run" == 1 ) ]] || { echo 'Select --snapshot and --dry-run or --yes' >&2; exit 2; }
mcpruntime_org_load_env
mcpruntime_org_require_cluster
MCP_TLS_SNAPSHOT_DIR="$(python3 "$SCRIPT_DIR/lib/verify-snapshot.py" "$snapshot" "$MCP_EXPECTED_CLUSTER_UID")"
# Resolve exactly the reviewed snapshot; never follow the automatic latest pointer.
MCP_TLS_BACKUP_ROOT="$MCP_TLS_SNAPSHOT_DIR"
MCP_TLS_DRY_RUN="$dry_run"
mcpruntime_org_restore_platform_runtime
