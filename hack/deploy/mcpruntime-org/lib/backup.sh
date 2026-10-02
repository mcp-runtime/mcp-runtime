#!/usr/bin/env bash
# Platform-runtime backup and restore for mcpruntime.org k3s deployments.

# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

MCP_TLS_BACKUP_ROOT="${MCP_TLS_BACKUP_DIR:-$HOME/.mcpruntime/backups/mcpruntime-org}"
MCP_TLS_SNAPSHOT_DIR=""
MCP_TLS_DRY_RUN="${MCP_TLS_DRY_RUN:-0}"

mcpruntime_org_backup_ensure_root() {
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    MCP_TLS_SNAPSHOT_DIR="$MCP_TLS_BACKUP_ROOT/dry-run"
    return 0
  fi
  umask 077
  mkdir -p "$MCP_TLS_BACKUP_ROOT"
  chmod 0700 "$MCP_TLS_BACKUP_ROOT"
}

mcpruntime_org_backup_init_snapshot() {
  mcpruntime_org_backup_ensure_root
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] snapshot directory: $MCP_TLS_SNAPSHOT_DIR"
    return 0
  fi
  local stamp
  stamp="$(date -u +%Y-%m-%dT%H%M%SZ)"
  if [[ -e "$MCP_TLS_BACKUP_ROOT/$stamp" ]]; then
    stamp="${stamp}-$$"
  fi
  MCP_TLS_SNAPSHOT_DIR="$MCP_TLS_BACKUP_ROOT/$stamp"
  mkdir -p "$MCP_TLS_SNAPSHOT_DIR"
  chmod 0700 "$MCP_TLS_SNAPSHOT_DIR"
  echo "backup snapshot staging: $MCP_TLS_SNAPSHOT_DIR"
}

mcpruntime_org_backup_publish_latest() {
  local backup_kind="${1:-platform}"
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] would publish $backup_kind snapshot: $MCP_TLS_SNAPSHOT_DIR"
    return 0
  fi
  [[ -n "$MCP_TLS_SNAPSHOT_DIR" && -d "$MCP_TLS_SNAPSHOT_DIR" ]] || {
    echo "error: backup snapshot directory is not initialized" >&2
    return 1
  }
  date -u +%Y-%m-%dT%H:%M:%SZ >"$MCP_TLS_SNAPSHOT_DIR/BACKUP_COMPLETE.tmp"
  chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/BACKUP_COMPLETE.tmp"
  mv "$MCP_TLS_SNAPSHOT_DIR/BACKUP_COMPLETE.tmp" "$MCP_TLS_SNAPSHOT_DIR/BACKUP_COMPLETE"
  if [[ "$backup_kind" == "full" ]]; then
    cp "$MCP_TLS_SNAPSHOT_DIR/BACKUP_COMPLETE" "$MCP_TLS_SNAPSHOT_DIR/FULL_BACKUP_COMPLETE"
    chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/FULL_BACKUP_COMPLETE"
  fi
  local stamp
  stamp="$(basename "$MCP_TLS_SNAPSHOT_DIR")"
  ln -sfn "$stamp" "$MCP_TLS_BACKUP_ROOT/latest"
  echo "complete $backup_kind backup snapshot: $MCP_TLS_SNAPSHOT_DIR"
}

mcpruntime_org_backup_resolve_dir() {
  if [[ -L "$MCP_TLS_BACKUP_ROOT/latest" && -d "$MCP_TLS_BACKUP_ROOT/latest" ]]; then
    MCP_TLS_SNAPSHOT_DIR="$(cd "$MCP_TLS_BACKUP_ROOT/latest" && pwd)"
    return 0
  fi
  if [[ -f "$MCP_TLS_BACKUP_ROOT/registry-tls.yaml" ]]; then
    MCP_TLS_SNAPSHOT_DIR="$MCP_TLS_BACKUP_ROOT"
    return 0
  fi
  return 1
}

mcpruntime_org_backup_is_not_found() {
  local msg="$1"
  [[ "$msg" == *"(NotFound)"* || "$msg" == *"not found"* ]]
}

mcpruntime_org_backup_validate_yaml() {
  local file="$1"
  [[ -s "$file" ]] || return 1
  grep -q '^kind:' "$file" && grep -q '^metadata:' "$file"
}

mcpruntime_org_backup_resource() {
  local file="$1"
  shift
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] would backup: $* -> $file"
    return 0
  fi
  local tmp="${file}.tmp"
  local err
  if err="$(mcpruntime_org_kubectl "$@" -o yaml 2>&1 >"$tmp")"; then
    if mcpruntime_org_backup_validate_yaml "$tmp"; then
      mv "$tmp" "$file"
      chmod 0600 "$file"
      echo "backed up $(basename "$file")"
      return 0
    fi
    rm -f "$tmp"
    echo "skip backup $(basename "$file") (empty or invalid YAML)" >&2
    return 0
  fi
  rm -f "$tmp"
  if mcpruntime_org_backup_is_not_found "$err"; then
    echo "skip backup $(basename "$file") (resource missing)"
    return 0
  fi
  echo "backup failed for $(basename "$file"): $err" >&2
  return 1
}

mcpruntime_org_backup_platform_auth_env() {
  local auth_file="$MCP_TLS_SNAPSHOT_DIR/platform-auth.env"
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] would export OIDC keys from mcp-shared-config -> platform-auth.env"
    return 0
  fi
  if ! mcpruntime_org_kubectl get configmap mcp-shared-config -n mcp-platform >/dev/null 2>&1; then
    echo "skip platform-auth.env (mcp-shared-config missing)"
    return 0
  fi
  {
    echo "# Platform OIDC exports captured before clean ($(date -u +%Y-%m-%dT%H:%M:%SZ))"
    echo "# Merge into config/deployments/mcpruntime-org.env when rerunning setup."
    for key in GOOGLE_CLIENT_ID MCP_GOOGLE_CLIENT_ID OIDC_ISSUER OIDC_AUDIENCE OIDC_JWKS_URL; do
      val="$(mcpruntime_org_kubectl get configmap mcp-shared-config -n mcp-platform -o "jsonpath={.data.${key}}" 2>/dev/null || true)"
      if [[ -n "$val" ]]; then
        printf 'export %s=%q\n' "$key" "$val"
      fi
    done
  } >"${auth_file}.tmp"
  mv "${auth_file}.tmp" "$auth_file"
  chmod 0600 "$auth_file"
  echo "backed up platform-auth.env"
}

mcpruntime_org_backup_platform_runtime() {
  if [[ -z "$MCP_TLS_SNAPSHOT_DIR" ]]; then
    mcpruntime_org_backup_init_snapshot
  fi
  echo "Platform-runtime backup root: $MCP_TLS_BACKUP_ROOT"
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/registry-tls.yaml" get secret registry-tls -n registry
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-tls.yaml" get secret mcp-platform-tls -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/registry-cert.yaml" get certificate registry-cert -n registry
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-cert.yaml" get certificate mcp-platform-tls -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/letsencrypt-prod-clusterissuer.yaml" get clusterissuer letsencrypt-prod
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-shared-config.yaml" get configmap mcp-shared-config -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-api-credentials.yaml" get secret mcp-platform-api-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-ui-credentials.yaml" get secret mcp-ui-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-postgres-credentials.yaml" get secret mcp-postgres-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-runtime-api-credentials.yaml" get secret mcp-runtime-api-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-signing-credentials.yaml" get secret mcp-platform-signing-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-runtime-ingest-credentials.yaml" get secret mcp-runtime-ingest-credentials -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-analytics-api-credentials.yaml" get secret mcp-analytics-api-credentials -n mcp-observability
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-ingest-credentials.yaml" get secret mcp-ingest-credentials -n mcp-observability
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-grafana-credentials.yaml" get secret mcp-grafana-credentials -n mcp-observability
  # Optional authorization-server and identity-provider credentials. Provider
  # database/realm state still requires the provider's own export workflow.
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-connectors.yaml" get configmap mcp-auth-connectors -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-connector-secrets.yaml" get secret mcp-auth-connector-secrets -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-signing-key.yaml" get secret mcp-auth-signing-key -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-server-tls.yaml" get secret mcp-auth-server-tls -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/keycloak-admin.yaml" get secret keycloak-admin -n mcp-platform
  mcpruntime_org_backup_resource "$MCP_TLS_SNAPSHOT_DIR/keycloak-tls.yaml" get secret keycloak-tls -n mcp-platform
  mcpruntime_org_backup_platform_auth_env
  if [[ "${MCP_TLS_DEFER_PUBLISH:-0}" != "1" ]]; then
    mcpruntime_org_backup_publish_latest platform
  fi
}

# Save Kubernetes API objects as a protected inventory. It is a recovery
# reference and must not be blindly applied; the K3s datastore snapshot is the
# source of truth for full cluster-state recovery.
mcpruntime_org_backup_kubernetes_resources() {
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] would export namespaced and cluster-scoped Kubernetes resources"
    return 0
  fi
  local scope resource base_resource path tmp resources
  mkdir -p "$MCP_TLS_SNAPSHOT_DIR/resources/namespaced" \
    "$MCP_TLS_SNAPSHOT_DIR/resources/cluster-scoped"
  chmod 0700 "$MCP_TLS_SNAPSHOT_DIR/resources" \
    "$MCP_TLS_SNAPSHOT_DIR/resources/namespaced" \
    "$MCP_TLS_SNAPSHOT_DIR/resources/cluster-scoped"
  mcpruntime_org_kubectl get crds -o yaml >"$MCP_TLS_SNAPSHOT_DIR/crds.yaml"
  chmod 0600 "$MCP_TLS_SNAPSHOT_DIR/crds.yaml"

  for scope in namespaced cluster-scoped; do
    local list_args=(--namespaced=true)
    if [[ "$scope" == "cluster-scoped" ]]; then
      list_args=(--namespaced=false)
    fi
    if ! resources="$(mcpruntime_org_kubectl api-resources "${list_args[@]}" --verbs=list -o name | sort -u)"; then
      echo "error: unable to enumerate $scope Kubernetes resources" >&2
      return 1
    fi
    while IFS= read -r resource; do
      [[ -n "$resource" ]] || continue
      base_resource="${resource%%.*}"
      case "$base_resource" in
        pods|events|endpoints|endpointslices|leases|replicasets|controllerrevisions) continue ;;
      esac
      path="$MCP_TLS_SNAPSHOT_DIR/resources/$scope/${resource//\//_}.yaml"
      tmp="${path}.tmp"
      if [[ "$scope" == "namespaced" ]]; then
        if ! mcpruntime_org_kubectl get "$resource" -A -o yaml >"$tmp"; then
          rm -f "$tmp"
          echo "error: unable to export $scope resource $resource" >&2
          return 1
        fi
      elif ! mcpruntime_org_kubectl get "$resource" -o yaml >"$tmp"; then
        rm -f "$tmp"
        echo "error: unable to export $scope resource $resource" >&2
        return 1
      fi
      chmod 0600 "$tmp"
      mv "$tmp" "$path"
    done <<<"$resources"
  done
}

mcpruntime_org_backup_strip_and_apply() {
  local file="$1"
  local label="$2"
  if [[ ! -f "$file" ]]; then
    echo "skip restore $label (backup missing)"
    return 0
  fi
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    echo "[dry-run] would apply $file"
    return 0
  fi
  mcpruntime_org_kubectl create --dry-run=client -f "$file" -o json \
    | jq 'del(
        .metadata.uid,
        .metadata.resourceVersion,
        .metadata.creationTimestamp,
        .metadata.generation,
        .metadata.managedFields,
        .metadata.selfLink,
        .metadata.ownerReferences,
        .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"],
        .status
      )' \
    | mcpruntime_org_kubectl apply --server-side --force-conflicts -f -
  echo "restored $label"
}

mcpruntime_org_backup_warn_certificates() {
  if [[ "$MCP_TLS_DRY_RUN" == "1" ]]; then
    return 0
  fi
  local had_tls_backup=0
  for f in registry-tls.yaml mcp-platform-tls.yaml; do
    if [[ -f "$MCP_TLS_SNAPSHOT_DIR/$f" ]]; then
      had_tls_backup=1
    fi
  done
  if [[ "$had_tls_backup" == "0" ]]; then
    echo "warning: no TLS secret backups found; setup may request new Let's Encrypt certificates" >&2
    echo "         LE rate limit is 5 duplicate certs / 7 days per domain set" >&2
    return 0
  fi
  local cert ns name ready
  for cert in registry/registry-cert mcp-platform/mcp-platform-tls; do
    ns="${cert%%/*}"
    name="${cert##*/}"
    if ! mcpruntime_org_kubectl get certificate "$name" -n "$ns" >/dev/null 2>&1; then
      continue
    fi
    ready="$(mcpruntime_org_kubectl get certificate "$name" -n "$ns" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
    if [[ "$ready" != "True" ]]; then
      echo "warning: certificate ${ns}/${name} is not Ready (status=${ready:-unknown})" >&2
    fi
  done
}

mcpruntime_org_restore_platform_runtime() {
  if ! mcpruntime_org_backup_resolve_dir; then
    echo "error: no platform backup found under $MCP_TLS_BACKUP_ROOT" >&2
    echo "       expected $MCP_TLS_BACKUP_ROOT/latest or flat registry-tls.yaml" >&2
    exit 1
  fi
  echo "Restoring platform-runtime backups from $MCP_TLS_SNAPSHOT_DIR"

  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/letsencrypt-prod-clusterissuer.yaml" "letsencrypt-prod ClusterIssuer"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/registry-tls.yaml" "registry TLS secret"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-tls.yaml" "platform UI TLS secret"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/registry-cert.yaml" "registry Certificate"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-cert.yaml" "platform UI Certificate"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-shared-config.yaml" "mcp-shared-config"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-api-credentials.yaml" "mcp-platform-api-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-ui-credentials.yaml" "mcp-ui-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-postgres-credentials.yaml" "mcp-postgres-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-runtime-api-credentials.yaml" "mcp-runtime-api-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-platform-signing-credentials.yaml" "mcp-platform-signing-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-runtime-ingest-credentials.yaml" "mcp-runtime-ingest-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-analytics-api-credentials.yaml" "mcp-analytics-api-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-ingest-credentials.yaml" "mcp-ingest-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-grafana-credentials.yaml" "mcp-grafana-credentials"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-connectors.yaml" "mcp-auth connectors"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-connector-secrets.yaml" "mcp-auth connector secrets"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-signing-key.yaml" "mcp-auth signing key"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/mcp-auth-server-tls.yaml" "mcp-auth TLS secret"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/keycloak-admin.yaml" "Keycloak admin secret"
  mcpruntime_org_backup_strip_and_apply "$MCP_TLS_SNAPSHOT_DIR/keycloak-tls.yaml" "Keycloak TLS secret"

  mcpruntime_org_backup_warn_certificates

  if [[ -f "$MCP_TLS_SNAPSHOT_DIR/platform-auth.env" ]]; then
    echo ""
    echo "OIDC exports: $MCP_TLS_SNAPSHOT_DIR/platform-auth.env"
    echo "Ensure config/deployments/mcpruntime-org.env includes GOOGLE_CLIENT_ID / OIDC_* when needed."
  fi

  if [[ "$MCP_TLS_DRY_RUN" != "1" ]]; then
    echo ""
    echo "Restarting API and UI deployments so restored secrets and config take effect ..."
    local deployment ns
    for deployment in mcp-platform-api mcp-runtime-api mcp-ui mcp-analytics-api; do
      ns=mcp-platform
      if [[ "$deployment" == "mcp-analytics-api" ]]; then
        ns=mcp-observability
      fi
      if mcpruntime_org_kubectl get deployment "$deployment" -n "$ns" >/dev/null 2>&1; then
        mcpruntime_org_kubectl rollout restart deployment "$deployment" -n "$ns" >/dev/null
        mcpruntime_org_kubectl rollout status deployment "$deployment" -n "$ns" --timeout="${MCP_DEPLOYMENT_TIMEOUT:-180s}" >/dev/null
        echo "restarted $deployment"
      fi
    done
  fi
}
