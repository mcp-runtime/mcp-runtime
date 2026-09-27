#!/usr/bin/env bash
# Full online backup for the single-node mcpruntime.org k3s deployment.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/backup.sh
source "$SCRIPT_DIR/lib/backup.sh"

ALLOW_ONLINE_COPY=0
FULL_BACKUP=0
SETUP_BACKUP=0
DRY_RUN=0

usage() {
  cat <<'EOF'
Usage: hack/deploy/mcpruntime-org/backup.sh (--setup | --full --online-copy) [--dry-run]

  --setup         Capture Kubernetes resources, PVC/PV definitions, and platform
                  Secrets/config needed to recover a setup redeployment.
  --full          Capture the setup snapshot plus k3s state and local-path data.
  --online-copy   Confirm that PVC files will be copied while services remain
                  online. This avoids downtime; database files are live copies,
                  not application-consistent database exports.
  --dry-run       Check the target and show the backup plan without writing.

The protected setup snapshot contains Kubernetes Secrets/config and resource
definitions, including PV/PVC specs. It does not copy PV file contents. Full
snapshots also contain the k3s server token/configuration, SQLite cluster
database, and every local-path volume. Bundles are not encrypted by this
command; store them on encrypted storage and copy them off the production host.
Do not apply resources/*.yaml as a bulk restore. See
docs/k3s-deployment-runbook.md for restore guidance.

The Kubernetes database is captured with SQLite's online backup API. PVC data
is archived live from the k3s node. Such file copies can require database WAL
recovery and are not a replacement for database-native point-in-time backups.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)
      FULL_BACKUP=1
      shift
      ;;
    --setup)
      SETUP_BACKUP=1
      shift
      ;;
    --online-copy)
      ALLOW_ONLINE_COPY=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
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

if [[ "$FULL_BACKUP" == "$SETUP_BACKUP" ]]; then
  echo "error: specify exactly one of --setup or --full" >&2
  usage >&2
  exit 1
fi
if [[ "$FULL_BACKUP" == "1" && "$ALLOW_ONLINE_COPY" != "1" ]]; then
  echo "error: full backup requires explicit --online-copy acknowledgement" >&2
  usage >&2
  exit 1
fi
if [[ "$SETUP_BACKUP" == "1" && "$ALLOW_ONLINE_COPY" == "1" ]]; then
  echo "error: --online-copy only applies to --full PVC data archives" >&2
  exit 1
fi

mcpruntime_org_load_env 1
if [[ "${MCP_KUBE_CONTEXT:-}" != "prod-mcp-runtime" ]]; then
  echo "error: full public backup requires MCP_KUBE_CONTEXT=prod-mcp-runtime" >&2
  exit 1
fi
mcpruntime_org_require_cluster

if [[ "$FULL_BACKUP" == "1" && -z "${MCP_PRODUCTION_SSH_HOST:-}" ]]; then
  echo "error: MCP_PRODUCTION_SSH_HOST is required for the k3s data backup" >&2
  exit 1
fi

if [[ "$DRY_RUN" == "1" ]]; then
  MCP_TLS_DRY_RUN=1
  mcpruntime_org_backup_init_snapshot
  mcpruntime_org_backup_platform_runtime
  mcpruntime_org_backup_kubernetes_resources
  if [[ "$FULL_BACKUP" == "1" ]]; then
    echo "[dry-run] would capture SQLite state and /etc/rancher/k3s plus /var/lib/rancher/k3s/{server,storage} from root@${MCP_PRODUCTION_SSH_HOST}"
  else
    echo "[dry-run] setup backup excludes live PVC file contents and the k3s host archive"
  fi
  exit 0
fi

mcpruntime_org_backup_init_snapshot
MCP_TLS_DEFER_PUBLISH=1 mcpruntime_org_backup_platform_runtime

cat >"$MCP_TLS_SNAPSHOT_DIR/README.txt" <<EOF
MCP Runtime production backup
Created UTC: $(date -u +%Y-%m-%dT%H:%M:%SZ)
Kubernetes context: ${MCP_KUBE_CONTEXT}
Production node: ${MCP_PRODUCTION_SSH_HOST:-not used by setup snapshot}
Runtime source ref: $(git -C "$(mcpruntime_org_repo_root)" rev-parse HEAD)
Scope: $(if [[ "$FULL_BACKUP" == "1" ]]; then printf 'full node and live local-path data'; else printf 'setup resources, PVC/PV definitions, and platform credentials'; fi)
$(if [[ "$FULL_BACKUP" == "1" ]]; then printf 'Consistency: SQLite cluster state uses the online backup API; local-path PVC files were copied live while services remained online. Database volumes may need WAL recovery and are not application-consistent point-in-time exports.'; else printf 'PVC/PV definitions are included, but live volume file contents and the k3s host/database are excluded.'; fi)

Contents:
- resources/: Kubernetes API object inventory, including CRDs, Secrets, and PVC/PV specs.
$(if [[ "$FULL_BACKUP" == "1" ]]; then printf '%s\n' '- k3s-state.db: online SQLite snapshot of K3s control-plane state.' '- k3s-host-and-pv-data.tar.gz: K3s host configuration/server material and all local-path PV files.'; fi)
- TLS/config/Secret YAML files: selected platform restore inputs used by setup.sh.

Keep this directory private and copy it to encrypted off-host storage. The
resource inventory is for recovery reference and must not be bulk-applied.
EOF
chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/README.txt"

mcpruntime_org_backup_kubernetes_resources

if [[ "$SETUP_BACKUP" == "1" ]]; then
  python3 - "$MCP_TLS_SNAPSHOT_DIR" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = sorted(path for path in root.rglob("*") if path.is_file() and path.name != "SHA256SUMS")
with (root / "SHA256SUMS").open("w") as output:
    for path in files:
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
        output.write(f"{digest.hexdigest()}  {path.relative_to(root)}\n")
PY
  chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/SHA256SUMS"
  mcpruntime_org_backup_publish_latest setup
  exit 0
fi

LOCAL_DB="$MCP_TLS_SNAPSHOT_DIR/k3s-state.db"
REMOTE_DB="/tmp/mcp-runtime-state-${USER:-operator}-$(date -u +%Y%m%dT%H%M%SZ)-$$.db"
REMOTE="root@${MCP_PRODUCTION_SSH_HOST}"
cleanup_remote_db() {
  ssh "$REMOTE" "rm -f '$REMOTE_DB'" >/dev/null 2>&1 || true
}
trap cleanup_remote_db EXIT
REMOTE_DB_CMD="test -s /var/lib/rancher/k3s/server/db/state.db && python3 -c 'import sqlite3; source=sqlite3.connect(\"/var/lib/rancher/k3s/server/db/state.db\"); target=sqlite3.connect(\"${REMOTE_DB}\"); source.backup(target); target.close(); source.close()'"
if ! ssh "$REMOTE" "$REMOTE_DB_CMD"; then
  echo "error: could not create online SQLite state snapshot on the k3s node" >&2
  exit 1
fi
if ! scp "$REMOTE:$REMOTE_DB" "$LOCAL_DB"; then
  ssh "$REMOTE" "rm -f '$REMOTE_DB'" >/dev/null 2>&1 || true
  echo "error: could not copy the SQLite state snapshot from the k3s node" >&2
  exit 1
fi
cleanup_remote_db
chmod 0600 "$LOCAL_DB"
python3 - "$LOCAL_DB" <<'PY'
import sqlite3, sys
connection = sqlite3.connect(sys.argv[1])
result = connection.execute("PRAGMA integrity_check").fetchone()[0]
connection.close()
if result != "ok":
    raise SystemExit(f"SQLite integrity check failed: {result}")
PY

HOST_ARCHIVE="$MCP_TLS_SNAPSHOT_DIR/k3s-host-and-pv-data.tar.gz"
HOST_ARCHIVE_TMP="${HOST_ARCHIVE}.tmp"
HOST_ARCHIVE_LOG="$MCP_TLS_SNAPSHOT_DIR/k3s-host-and-pv-data.tar.log"
REMOTE_TAR_CMD="cd / && tar --numeric-owner --acls --xattrs --exclude='var/lib/rancher/k3s/server/db/state.db*' -czf - etc/rancher/k3s var/lib/rancher/k3s/server var/lib/rancher/k3s/storage"
TAR_STATUS=0
if ssh "$REMOTE" "$REMOTE_TAR_CMD" >"$HOST_ARCHIVE_TMP" 2>"$HOST_ARCHIVE_LOG"; then
  :
else
  TAR_STATUS=$?
fi
chmod 0600 "$HOST_ARCHIVE_LOG"
if [[ "$TAR_STATUS" -gt 1 ]]; then
  rm -f "$HOST_ARCHIVE_TMP"
  echo "error: could not archive live k3s host and local-path data (ssh/tar status $TAR_STATUS)" >&2
  exit 1
fi
[[ -s "$HOST_ARCHIVE_TMP" ]] || {
  rm -f "$HOST_ARCHIVE_TMP"
  echo "error: k3s host/PV archive is empty" >&2
  exit 1
}
mv "$HOST_ARCHIVE_TMP" "$HOST_ARCHIVE"
chmod 0600 "$HOST_ARCHIVE"
tar -tzf "$HOST_ARCHIVE" >/dev/null
if [[ "$TAR_STATUS" == "1" ]]; then
  echo "warning: live files changed or disappeared during archive; review k3s-host-and-pv-data.tar.log" >&2
  printf '\nArchive warning: GNU tar returned status 1 because live files changed or disappeared. See k3s-host-and-pv-data.tar.log; database recovery may be needed.\n' >>"$MCP_TLS_SNAPSHOT_DIR/README.txt"
fi

mcpruntime_org_kubectl get pv -o json \
  | jq -r '.items[] | select(.spec.claimRef != null) | [.metadata.name, (.spec.local.path // "UNSUPPORTED_NON_LOCAL_PATH"), .spec.claimRef.namespace, .spec.claimRef.name] | @tsv' \
  >"$MCP_TLS_SNAPSHOT_DIR/persistent-volumes.tsv"
chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/persistent-volumes.tsv"
python3 - "$MCP_TLS_SNAPSHOT_DIR/persistent-volumes.tsv" "$HOST_ARCHIVE" <<'PY'
import pathlib, sys, tarfile
inventory = pathlib.Path(sys.argv[1])
archive_path = pathlib.Path(sys.argv[2])
required = []
unsupported = []
for line in inventory.read_text().splitlines():
    fields = line.split("\t")
    if len(fields) != 4:
        raise SystemExit("invalid PV inventory row")
    name, path, namespace, claim = fields
    if path == "UNSUPPORTED_NON_LOCAL_PATH":
        unsupported.append(f"{namespace}/{claim} ({name})")
    else:
        required.append(path.lstrip("/"))
if unsupported:
    raise SystemExit("PVCs use storage not covered by the k3s local-path archive: " + ", ".join(unsupported))
with tarfile.open(archive_path, "r:gz") as archive:
    members = {member.name.lstrip("./").rstrip("/") for member in archive.getmembers()}
missing = [path for path in required if path not in members]
if missing:
    raise SystemExit("local-path PV directories missing from archive: " + ", ".join(missing))
print(f"verified {len(required)} bound local-path volume directories in archive")
PY

python3 - "$MCP_TLS_SNAPSHOT_DIR" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = sorted(path for path in root.rglob("*") if path.is_file() and path.name != "SHA256SUMS")
with (root / "SHA256SUMS").open("w") as output:
    for path in files:
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
        output.write(f"{digest.hexdigest()}  {path.relative_to(root)}\n")
PY
chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/SHA256SUMS"
mcpruntime_org_backup_publish_latest full
trap - EXIT
