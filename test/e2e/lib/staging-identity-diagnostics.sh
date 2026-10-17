# shellcheck shell=bash

# This fixture explicitly tests diagnostic reads of malformed public PKI
# resources. It never changes a deployed server, active root, issuer, operator,
# TLSOption or runtime API configuration. The CLI under test remains read-only.
staging_identity_finish() {
  local original_status="$1"
  if ! cleanup_identity_probe && [[ "${original_status}" == 0 ]]; then
    original_status=1
  fi
  exit "${original_status}"
}

staging_identity_expect_failure() {
  local server="$1" expected="$2" output="$3" code=0
  "${BIN}" cluster identity status --server "${server}" >"${output}" 2>&1 || code=$?
  if [[ "${code}" == 0 ]] || ! grep -Fq -- "${expected}" "${output}" || ! grep -Fq 'Remedy:' "${output}"; then
    staging_err "identity diagnostic did not fail with the expected public PKI reason"
    return 1
  fi
  if grep -Eq -- 'BEGIN .*PRIVATE KEY|BEGIN CERTIFICATE|e2e-private-field-sentinel|"data"[[:space:]]*:' "${output}"; then
    staging_err "identity diagnostic exposed certificate/Secret material"
    return 1
  fi
}

staging_check_identity_diagnostics() (
  set -e
  [[ "$(staging_stage_status adapter-enrollment)" == passed ]] ||
    staging_skip "identity diagnostics require the successful adapter enrollment fixture"
  staging_marker_valid "$(staging_marker_path)" "$(hostname)" || return 1
  # The stage runner uses this VM's explicit k3s kubeconfig. Refuse a remote
  # endpoint even if somebody invokes the fixture from a marked E2E machine.
  local endpoint
  endpoint="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  [[ "${endpoint}" == https://127.0.0.1:6443 || "${endpoint}" == https://localhost:6443 ]] || {
    staging_err "identity diagnostic fixture requires the local disposable k3s API"
    return 1
  }
  # The enrollment stage cleans up its private server pair on exit. Use the
  # persistent managed Acme server also used by monitor/deactivation stages.
  staging_mt_names
  local ns="${MT_ACME_NS}" live="${MT_ACME_SERVER}" probe scratch route_uid='' secret_uid=''
  local artifacts="${STAGING_ARTIFACT_DIR}/identity-diagnostics"
  probe="identity-probe-$(openssl rand -hex 8)"
  scratch="$(mktemp -d "${WORK_DIR}/identity-diagnostics.XXXXXXXX")"
  chmod 700 "${scratch}"
  mkdir -p "${artifacts}"
  # shellcheck disable=SC2329 # invoked by the EXIT trap in this subshell
  cleanup_identity_probe() {
    local current failed=0
    if [[ -n "${route_uid}" ]]; then
      current="$(kubectl -n "${ns}" get ingressroute.traefik.io "${probe}" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
      if [[ "${current}" == "${route_uid}" ]]; then
        kubectl -n "${ns}" delete ingressroute.traefik.io "${probe}" --wait=false >/dev/null || failed=1
      fi
    fi
    if [[ -n "${secret_uid}" ]]; then
      current="$(kubectl -n "${ns}" get secret "${probe}-gateway-mtls" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
      if [[ "${current}" == "${secret_uid}" ]]; then
        kubectl -n "${ns}" delete secret "${probe}-gateway-mtls" --wait=false >/dev/null || failed=1
      fi
    fi
    rm -rf -- "${scratch}" || failed=1
    return "${failed}"
  }
  trap 'staging_identity_finish "$?"' EXIT

  "${BIN}" cluster identity status --server "${ns}/${live}" >"${artifacts}/healthy.txt" 2>&1
  grep -Fq 'verifies client certificates' "${artifacts}/healthy.txt"

  # Fresh private fixture keys stay outside artifacts and never enter Kubernetes.
  # One root is valid but foreign to the active issuer/bundle; the other expires
  # inside the diagnostic's 30-day warning window.
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 \
    -subj /CN=e2e-foreign-workload-root -keyout "${scratch}/foreign.key" \
    -out "${scratch}/foreign.crt" >/dev/null 2>&1
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
    -subj /CN=e2e-expiring-workload-root -keyout "${scratch}/expiring.key" \
    -out "${scratch}/expiring.crt" >/dev/null 2>&1
  jq -n --arg name "${probe}-gateway-mtls" --arg ns "${ns}" \
    --rawfile cert "${scratch}/foreign.crt" \
    '{apiVersion:"v1",kind:"Secret",metadata:{name:$name,namespace:$ns},type:"Opaque",stringData:{"ca.crt":$cert,"tls.key":"e2e-private-field-sentinel"}}' \
    >"${scratch}/secret.json"
  secret_uid="$(kubectl create -f "${scratch}/secret.json" -o jsonpath='{.metadata.uid}')"
  [[ -n "${secret_uid}" ]]
  # Reuse the live route's actual TLSOption and service; a reserved .invalid
  # hostname prevents any conflict with real traffic. No ACME resolver/domain
  # declaration is copied into this diagnostic-only IngressRoute.
  kubectl -n "${ns}" get ingressroute.traefik.io "${live}" -o json |
    jq --arg probe "${probe}" --arg ns "${ns}" \
      '{apiVersion:.apiVersion,kind:.kind,metadata:{name:$probe,namespace:$ns},spec:{entryPoints:.spec.entryPoints,tls:(.spec.tls | {options,secretName} | with_entries(select(.value != null))),routes:[(.spec.routes[0] | .match=("Host(`"+$probe+".invalid`)") | .kind="Rule")]}}' \
      >"${scratch}/route.json"
  route_uid="$(kubectl create -f "${scratch}/route.json" -o jsonpath='{.metadata.uid}')"
  [[ -n "${route_uid}" ]]
  staging_identity_expect_failure "${ns}/${probe}" \
    'workload CA and selected route trust bundle fingerprints differ' "${artifacts}/issuer-bundle-mismatch.txt"

  jq -n --rawfile cert "${scratch}/expiring.crt" '{data:{"ca.crt":($cert|@base64)}}' >"${scratch}/expiry-patch.json"
  kubectl -n "${ns}" patch secret "${probe}-gateway-mtls" --type=merge --patch-file "${scratch}/expiry-patch.json" >/dev/null
  staging_identity_expect_failure "${ns}/${probe}" \
    'workload CA expires within 30 days' "${artifacts}/near-expiry.txt"
  # Existing served identity stays healthy throughout both isolated failures.
  "${BIN}" cluster identity status --server "${ns}/${live}" >"${artifacts}/healthy-after.txt" 2>&1
  grep -Fq 'verifies client certificates' "${artifacts}/healthy-after.txt"
  staging_log "identity diagnostics: healthy route, foreign issuer/root mismatch, near-expiry root and safe output verified"
)
