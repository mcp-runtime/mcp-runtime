#!/usr/bin/env bash
# Glue between the Staging E2E VM and the reference backup helpers in
# hack/deploy/reference/lib/backup.sh. The helpers record and verify which
# cluster a snapshot belongs to, which suits the long-lived reference platform.
# The staging VM is different: k3s is reinstalled every run, so the cluster
# identity changes on purpose and snapshots exist to carry certificates and
# credentials across rebuilds. These functions supply what the helpers need
# without involving the production deployment profile.

# staging_snapshot_identity exports the identity the backup helper stamps into
# each snapshot, taken from the cluster the VM is about to tear down.
staging_snapshot_identity() {
  local uid context
  uid="$(kubectl get namespace kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  [[ -n "${uid}" ]] || {
    echo "error: cannot read the kube-system namespace UID to identify the snapshot's cluster" >&2
    return 1
  }
  context="$(kubectl config current-context 2>/dev/null || true)"
  export MCP_EXPECTED_CLUSTER_UID="${uid}"
  export MCP_KUBE_CONTEXT="${context:-staging-vm}"
}

# staging_snapshot_select_latest points the restore helper at the newest
# snapshot under backup_root. The helper no longer follows `latest` itself, and
# its cluster-identity check is intentionally not applied: the previous run's
# cluster no longer exists.
staging_snapshot_select_latest() {
  local backup_root="$1" latest
  [[ -L "${backup_root}/latest" && -d "${backup_root}/latest" ]] || return 1
  latest="$(cd "${backup_root}/latest" && pwd -P)"
  # These are read by the sourced reference backup helper.
  # shellcheck disable=SC2034
  MCP_TLS_BACKUP_ROOT="${backup_root}"
  # shellcheck disable=SC2034
  MCP_TLS_SNAPSHOT_DIR="${latest}"
  # shellcheck disable=SC2034
  MCP_TLS_DRY_RUN=0
}
