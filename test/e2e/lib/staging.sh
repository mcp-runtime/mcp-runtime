# shellcheck shell=bash
# Shared helpers for the Staging E2E runners (staging-vm.sh, staging-remote.sh)
# and the runner-side target check (staging-target.sh).
#
# Three groups of helpers live here:
#
#   1. The disposable-target guard. Staging E2E installs k3s, wipes it, prunes
#      Docker and re-runs setup. Pointed at the live production cluster that
#      would destroy it, so every runner refuses to start unless the target is
#      provably the disposable VM: every E2E hostname ends in the disposable
#      suffix, none of them resolves to an address that a production hostname
#      resolves to (derived by DNS at runtime; no address is hardcoded here),
#      and the VM carries a marker written only by `staging-target.sh
#      bootstrap`.
#   2. A stage runner that records pass/fail/skip per stage, keeps one log per
#      stage, and renders summary.json and summary.md.
#   3. The post-setup assertions both runners share: TLS, registry auth, image
#      pulls, platform API/UI, OIDC, adapter enrollment, governance, analytics.
#
# The caller must set KUBECONFIG before running cluster assertions. Nothing
# here prints secret values; tokens are passed through the environment and
# never written into the artifact directory.

STAGING_LOG_PREFIX="${STAGING_LOG_PREFIX:-staging-e2e}"
STAGING_MARKER_MAGIC="mcp-runtime-disposable-e2e-vm"
STAGING_DEFAULT_SUFFIX="e2e.mcpruntime.org"
STAGING_DEFAULT_PRODUCTION_DOMAIN="mcpruntime.org"
# Exit status a stage body uses to report "skipped" rather than pass/fail.
STAGING_SKIP_RC=77

# Shared enroll credential-dir parser (scoped …/certs/<hash>/ PEMs).
# shellcheck source=adapter-certificates.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/adapter-certificates.sh"

staging_log() { printf '[%s] %s\n' "${STAGING_LOG_PREFIX}" "$*"; }
staging_warn() { printf '[%s] WARNING: %s\n' "${STAGING_LOG_PREFIX}" "$*" >&2; }
staging_err() { printf '[%s] ERROR: %s\n' "${STAGING_LOG_PREFIX}" "$*" >&2; }

# The workflows pass GitHub booleans ("true"/"false"); older callers pass 1/0.
staging_flag_enabled() {
  case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
    1 | true | yes | on) return 0 ;;
    *) return 1 ;;
  esac
}

staging_lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# ---------------------------------------------------------------------------
# 1. Disposable-target guard
# ---------------------------------------------------------------------------

staging_disposable_suffix() {
  local suffix="${E2E_DISPOSABLE_DOMAIN_SUFFIX:-${STAGING_DEFAULT_SUFFIX}}"
  suffix="$(staging_lower "${suffix}")"
  suffix="${suffix#.}"
  printf '%s' "${suffix%.}"
}

staging_production_domain() {
  local domain="${E2E_PRODUCTION_DOMAIN:-${STAGING_DEFAULT_PRODUCTION_DOMAIN}}"
  domain="$(staging_lower "${domain}")"
  domain="${domain#.}"
  printf '%s' "${domain%.}"
}

# Hostnames of the live production install. Their addresses are the ones the
# E2E target must never share.
staging_production_hosts() {
  local domain host
  domain="$(staging_production_domain)"
  for host in platform registry mcp auth; do
    printf '%s.%s\n' "${host}" "${domain}"
  done
  for host in ${E2E_PRODUCTION_EXTRA_HOSTS:-}; do
    printf '%s\n' "$(staging_lower "${host}")"
  done
}

# Machine hostnames that identify the production VM. A marker found on a
# machine with one of these names is ignored.
staging_production_machine_names() {
  # shellcheck disable=SC2086 # a space-separated list on purpose
  printf '%s\n' ${E2E_PRODUCTION_MACHINE_NAMES:-devbox}
}

staging_url_host() {
  local value="$1"
  value="${value#*://}"
  value="${value%%/*}"
  value="${value%%\?*}"
  value="${value##*@}"
  value="${value%%:*}"
  value="$(staging_lower "${value}")"
  printf '%s' "${value%.}"
}

staging_is_ipv4() {
  [[ "$1" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]
}

# host_has_suffix HOST SUFFIX: HOST is a strict subdomain of SUFFIX.
staging_host_has_suffix() {
  local host suffix
  host="$(staging_lower "${1%.}")"
  suffix="$(staging_lower "${2#.}")"
  suffix="${suffix%.}"
  [[ -n "${host}" && -n "${suffix}" ]] || return 1
  [[ "${host}" == *".${suffix}" ]] || return 1
  local label="${host%".${suffix}"}"
  [[ -n "${label}" && "${label}" != *..* && "${label}" != .* ]]
}

# The disposable suffix itself must not be (or contain) the production domain,
# otherwise `platform.mcpruntime.org` would pass a check for `mcpruntime.org`.
staging_suffix_is_safe() {
  local suffix production
  suffix="$(staging_lower "${1#.}")"
  production="$(staging_lower "${2#.}")"
  [[ -n "${suffix}" && -n "${production}" ]] || return 1
  [[ "${suffix}" == *.*.* ]] || return 1
  [[ "${suffix}" != "${production}" ]] || return 1
  # A suffix that is a parent of the production domain would admit it.
  [[ "${production}" != *".${suffix}" ]] || return 1
  return 0
}

# Resolve HOST to its IPv4 addresses, one per line. Tests replace resolution
# with a static "host ip" table through STAGING_RESOLVE_TABLE.
staging_resolve_ipv4() {
  local host="$1"
  if staging_is_ipv4 "${host}"; then
    printf '%s\n' "${host}"
    return 0
  fi
  if [[ -n "${STAGING_RESOLVE_TABLE:-}" ]]; then
    awk -v h="${host}" '$1 == h { print $2 }' "${STAGING_RESOLVE_TABLE}" | sort -u
    return 0
  fi
  local out=""
  if command -v getent >/dev/null 2>&1; then
    out="$(getent ahostsv4 "${host}" 2>/dev/null | awk '{ print $1 }' | sort -u || true)"
  fi
  if [[ -z "${out}" ]] && command -v dig >/dev/null 2>&1; then
    out="$(dig +short A "${host}" 2>/dev/null | grep -E '^[0-9.]+$' | sort -u || true)"
  fi
  if [[ -z "${out}" ]] && command -v host >/dev/null 2>&1; then
    out="$(host -t A "${host}" 2>/dev/null | awk '/has address/ { print $NF }' | sort -u || true)"
  fi
  if [[ -z "${out}" ]] && command -v python3 >/dev/null 2>&1; then
    out="$(python3 -c 'import socket,sys
try:
    print("\n".join(sorted(set(socket.gethostbyname_ex(sys.argv[1])[2]))))
except Exception:
    pass' "${host}" 2>/dev/null || true)"
  fi
  [[ -n "${out}" ]] && printf '%s\n' "${out}"
  return 0
}

# Addresses the production hostnames resolve to right now, one per line.
staging_production_ips() {
  local host all=""
  while IFS= read -r host; do
    [[ -n "${host}" ]] || continue
    all+="$(staging_resolve_ipv4 "${host}")"$'\n'
  done < <(staging_production_hosts)
  printf '%s' "${all}" | grep -E '^[0-9.]+$' | sort -u || true
}

# staging_guard_local_ips IP... -- refuse when this machine owns an address a
# production hostname resolves to (the on-VM runner's self check).
staging_guard_local_ips() {
  local production_ips ip
  production_ips="$(staging_production_ips)"
  for ip in "$@"; do
    staging_is_ipv4 "${ip}" || continue
    if printf '%s\n' "${production_ips}" | grep -Fxq -- "${ip}"; then
      staging_err "refusing to run: this machine owns an address shared with the production install"
      return 1
    fi
  done
  return 0
}

# staging_guard_hosts VM_HOST HOST... -- refuse unless every HOST is a
# disposable E2E hostname and neither the VM nor any HOST shares an address
# with production. VM_HOST may be empty when the runner is the VM itself.
staging_guard_hosts() {
  local vm_host="$1"
  shift
  local suffix production host ip
  suffix="$(staging_disposable_suffix)"
  production="$(staging_production_domain)"

  if ! staging_suffix_is_safe "${suffix}" "${production}"; then
    staging_err "refusing to run: disposable suffix '${suffix}' is not a subdomain distinct from the production domain '${production}'"
    return 1
  fi
  if [[ $# -eq 0 ]]; then
    staging_err "refusing to run: no E2E hostnames were configured"
    return 1
  fi

  local production_hosts
  production_hosts="$(staging_production_hosts)"
  for host in "$@"; do
    host="$(staging_lower "${host%.}")"
    if printf '%s\n' "${production_hosts}" | grep -Fxq -- "${host}"; then
      staging_err "refusing to run: ${host} is a production hostname"
      return 1
    fi
    if ! staging_host_has_suffix "${host}" "${suffix}"; then
      staging_err "refusing to run: ${host} does not end in .${suffix} (set E2E_DISPOSABLE_DOMAIN_SUFFIX only for another disposable zone)"
      return 1
    fi
  done

  local production_ips
  production_ips="$(staging_production_ips)"
  if [[ -z "${production_ips}" ]]; then
    if staging_flag_enabled "${E2E_GUARD_ALLOW_UNRESOLVED_PRODUCTION:-0}"; then
      staging_warn "production hostnames did not resolve; continuing because E2E_GUARD_ALLOW_UNRESOLVED_PRODUCTION is set"
    else
      staging_err "refusing to run: could not resolve any production hostname under ${production}, so the target cannot be proven distinct from it"
      return 1
    fi
  fi

  local target_ips="" resolved
  for host in "$@"; do
    resolved="$(staging_resolve_ipv4 "${host}")"
    if [[ -z "${resolved}" ]]; then
      staging_err "refusing to run: DNS does not resolve ${host}"
      return 1
    fi
    while IFS= read -r ip; do
      [[ -n "${ip}" ]] || continue
      if printf '%s\n' "${production_ips}" | grep -Fxq -- "${ip}"; then
        staging_err "refusing to run: ${host} resolves to an address shared with the production install"
        return 1
      fi
      target_ips+="${ip}"$'\n'
    done <<<"${resolved}"
  done

  if [[ -n "${vm_host}" ]]; then
    if printf '%s\n' "${production_hosts}" | grep -Fxq -- "$(staging_lower "${vm_host}")"; then
      staging_err "refusing to run: the VM host is a production hostname"
      return 1
    fi
    resolved="$(staging_resolve_ipv4 "${vm_host}")"
    if [[ -z "${resolved}" ]]; then
      staging_err "refusing to run: the VM host does not resolve"
      return 1
    fi
    local matched=0
    while IFS= read -r ip; do
      [[ -n "${ip}" ]] || continue
      if printf '%s\n' "${production_ips}" | grep -Fxq -- "${ip}"; then
        staging_err "refusing to run: the VM host resolves to an address shared with the production install"
        return 1
      fi
      if printf '%s' "${target_ips}" | grep -Fxq -- "${ip}"; then
        matched=1
      fi
    done <<<"${resolved}"
    # The E2E hostnames must point at the machine being wiped. This is what
    # stops a mistyped VM secret from reaching some other machine while the
    # DNS checks above pass.
    if [[ "${matched}" != "1" ]] && ! staging_flag_enabled "${E2E_GUARD_ALLOW_VM_DNS_MISMATCH:-0}"; then
      staging_err "refusing to run: the VM host does not resolve to the address the E2E hostnames point at"
      return 1
    fi
  fi
  staging_log "target guard: ${#} E2E hostname(s) under .${suffix}, none shared with ${production}"
  return 0
}

# staging_marker_valid FILE HOSTNAME -- the marker exists, carries the magic
# first line, and was written on this machine for this disposable suffix.
staging_marker_valid() {
  local file="$1" expected_host="$2" first recorded_host recorded_suffix
  [[ -f "${file}" ]] || {
    staging_err "disposable marker ${file} is missing; run 'test/e2e/staging-target.sh bootstrap' once for this VM (see docs/contributor/staging-e2e.md)"
    return 1
  }
  first="$(head -n 1 "${file}" | tr -d '\r')"
  if [[ "${first}" != "${STAGING_MARKER_MAGIC}" ]]; then
    staging_err "disposable marker ${file} is not a staging E2E marker"
    return 1
  fi
  recorded_host="$(awk -F= '$1 == "hostname" { print $2; exit }' "${file}" | tr -d '\r')"
  recorded_suffix="$(awk -F= '$1 == "suffix" { print $2; exit }' "${file}" | tr -d '\r')"
  if [[ -n "${expected_host}" && "${recorded_host}" != "${expected_host}" ]]; then
    staging_err "disposable marker was written for host '${recorded_host}', not '${expected_host}'; re-run bootstrap if this VM was rebuilt"
    return 1
  fi
  if [[ "${recorded_suffix}" != "$(staging_disposable_suffix)" ]]; then
    staging_err "disposable marker was written for suffix '${recorded_suffix}', not '$(staging_disposable_suffix)'"
    return 1
  fi
  if staging_production_machine_names | grep -Fxq -- "${expected_host}"; then
    staging_err "refusing to run: ${expected_host} is a production machine name"
    return 1
  fi
  return 0
}

staging_marker_path() {
  printf '%s/DISPOSABLE' "${E2E_BACKUP_DIR:-/var/lib/mcp-runtime-e2e-backup}"
}

# SSH to the disposable VM (runner-side callers). Password auth is only used
# when SSHPASS is exported; sshpass needs BatchMode off to answer the prompt.
staging_ssh_init() {
  STAGING_SSH_OPTS=(
    -o StrictHostKeyChecking="${E2E_SSH_STRICT_HOST_KEY:-yes}"
    -o ConnectTimeout=15
    -o ServerAliveInterval=30
    -o ServerAliveCountMax=20
  )
  if [[ -n "${E2E_VM_KNOWN_HOSTS:-}" ]]; then
    STAGING_SSH_OPTS+=(-o UserKnownHostsFile="${E2E_VM_KNOWN_HOSTS}")
  fi
}

# shellcheck disable=SC2153 # VM_HOST/VM_USER are set by the calling runner
staging_vm_ssh() {
  # shellcheck disable=SC2029 # the remote command is composed locally on purpose
  if [[ -n "${SSHPASS:-}" ]]; then
    sshpass -e ssh -o BatchMode=no "${STAGING_SSH_OPTS[@]}" "${VM_USER}@${VM_HOST}" "$@"
  else
    ssh -o BatchMode="${E2E_SSH_BATCH:-yes}" "${STAGING_SSH_OPTS[@]}" "${VM_USER}@${VM_HOST}" "$@"
  fi
}

# staging_verify_remote_target -- runner-side guard: DNS/suffix checks, then
# the marker and machine name read back over SSH. Needs VM_HOST/VM_USER.
staging_verify_remote_target() {
  local hosts=("$@") machine marker_tmp
  staging_guard_hosts "${VM_HOST}" "${hosts[@]}" || return 1
  machine="$(staging_vm_ssh 'hostname' 2>/dev/null | tr -d '\r' | head -n 1)"
  if [[ -z "${machine}" ]]; then
    staging_err "refusing to run: could not read the VM hostname over SSH"
    return 1
  fi
  marker_tmp="$(mktemp)"
  if ! staging_vm_ssh "cat '$(staging_marker_path)'" >"${marker_tmp}" 2>/dev/null; then
    rm -f "${marker_tmp}"
    staging_err "refusing to run: $(staging_marker_path) is missing on the VM; bootstrap it once with 'E2E_CONFIRM_DISPOSABLE_VM=<vm host> test/e2e/staging-target.sh bootstrap' (docs/contributor/staging-e2e.md)"
    return 1
  fi
  if ! staging_marker_valid "${marker_tmp}" "${machine}"; then
    rm -f "${marker_tmp}"
    return 1
  fi
  rm -f "${marker_tmp}"
  staging_log "target guard: disposable marker verified on ${machine}"
}

staging_marker_content() {
  local machine="$1"
  printf '%s\nhostname=%s\nsuffix=%s\ncreated=%s\n' "${STAGING_MARKER_MAGIC}" \
    "${machine}" "$(staging_disposable_suffix)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

# ---------------------------------------------------------------------------
# 2. Stage runner and summary
# ---------------------------------------------------------------------------

# staging_stages_init ARTIFACT_DIR STATE_FILE
staging_stages_init() {
  STAGING_ARTIFACT_DIR="$1"
  STAGING_STATE_FILE="$2"
  STAGING_STAGE_SEQ=0
  STAGING_ABORTED=0
  STAGING_FAILED=0
  mkdir -p "${STAGING_ARTIFACT_DIR}/stages" "${STAGING_ARTIFACT_DIR}/diagnostics"
  : >"${STAGING_ARTIFACT_DIR}/stages.tsv"
  touch "${STAGING_STATE_FILE}"
  chmod 600 "${STAGING_STATE_FILE}"
}

# Stage bodies run in a subshell, so values they must hand to later stages go
# through the state file. It lives in the work directory, never the artifacts.
staging_state_set() {
  printf 'export %s=%q\n' "$1" "$2" >>"${STAGING_STATE_FILE}"
}

staging_skip() {
  printf '[%s] [stage] %s: SKIP %s\n' "${STAGING_LOG_PREFIX}" "${STAGING_CURRENT_STAGE:-?}" "$*"
  printf '%s' "$*" >"${STAGING_ARTIFACT_DIR}/stages/.detail"
  exit "${STAGING_SKIP_RC}"
}

# staging_note TEXT -- attach a one-line detail to the current stage's row in
# the summary (for example, accepted known findings on a passing stage).
staging_note() {
  local file="${STAGING_ARTIFACT_DIR}/stages/.detail"
  if [[ -s "${file}" ]]; then
    printf '; %s' "$*" >>"${file}"
  else
    printf '%s' "$*" >"${file}"
  fi
}

staging_tsv_field() { printf '%s' "$1" | tr '\t\n\r' '   '; }

staging_record() {
  local name="$1" status="$2" duration="$3" log="$4" detail="$5" hint="$6"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(staging_tsv_field "${name}")" "${status}" "${duration}" \
    "$(staging_tsv_field "${log}")" "$(staging_tsv_field "${detail}")" "$(staging_tsv_field "${hint}")" \
    >>"${STAGING_ARTIFACT_DIR}/stages.tsv"
}

# staging_run_stage NAME critical|soft HINT CMD [ARGS...]
#
# Runs CMD in a subshell with errexit on, tees its output into
# stages/NN-NAME.log, and records the outcome. A failed critical stage marks
# the run aborted: every later stage is recorded as skipped instead of firing
# assertions at a cluster that was never set up. Always returns 0 so callers
# keep going; staging_finish reports the overall result.
#
# Never call this from a conditional context (`if`, `&&`, `||`, `!`): bash then
# ignores errexit in everything underneath, including the stage subshell, and a
# failing command in the body would no longer fail the stage.
staging_run_stage() {
  local name="$1" mode="$2" hint="$3"
  shift 3
  if [[ "${STAGING_ABORTED}" == "1" ]]; then
    staging_record "${name}" skipped 0 "" "an earlier critical stage failed" ""
    staging_log "[stage] ${name}: skipped (an earlier critical stage failed)"
    return 0
  fi
  STAGING_STAGE_SEQ=$((STAGING_STAGE_SEQ + 1))
  local log rel start rc errexit=0
  rel="stages/$(printf '%02d' "${STAGING_STAGE_SEQ}")-${name}.log"
  log="${STAGING_ARTIFACT_DIR}/${rel}"
  rm -f "${STAGING_ARTIFACT_DIR}/stages/.detail"
  staging_log "[stage] ${name}: start"
  start=${SECONDS}
  [[ $- == *e* ]] && errexit=1
  set +e
  (
    set -Eeuo pipefail
    STAGING_CURRENT_STAGE="${name}"
    "$@"
  ) 2>&1 | tee "${log}"
  rc=${PIPESTATUS[0]}
  [[ "${errexit}" == "1" ]] && set -e
  local duration=$((SECONDS - start))
  # shellcheck disable=SC1090
  [[ -s "${STAGING_STATE_FILE}" ]] && source "${STAGING_STATE_FILE}"
  local detail=""
  [[ -f "${STAGING_ARTIFACT_DIR}/stages/.detail" ]] && detail="$(cat "${STAGING_ARTIFACT_DIR}/stages/.detail")"
  rm -f "${STAGING_ARTIFACT_DIR}/stages/.detail"
  if [[ ${rc} -eq 0 ]]; then
    staging_record "${name}" passed "${duration}" "${rel}" "${detail}" ""
    staging_log "[stage] ${name}: passed (${duration}s)${detail:+ -- ${detail}}"
  elif [[ ${rc} -eq ${STAGING_SKIP_RC} ]]; then
    staging_record "${name}" skipped "${duration}" "${rel}" "${detail}" ""
    staging_log "[stage] ${name}: skipped (${detail})"
  else
    STAGING_FAILED=1
    staging_record "${name}" failed "${duration}" "${rel}" "exit ${rc}${detail:+; ${detail}}" "${hint}"
    staging_err "[stage] ${name}: FAILED (exit ${rc}) after ${duration}s -- likely cause: ${hint}; open ${rel} in the artifacts"
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      printf '::error title=Staging E2E stage %s failed::%s (artifact: %s)\n' "${name}" "${hint}" "${rel}"
    fi
    if [[ "${mode}" == "critical" ]]; then
      STAGING_ABORTED=1
      staging_err "[stage] ${name} is critical; remaining stages will be skipped"
    fi
  fi
  return 0
}

staging_stage_status() {
  awk -F'\t' -v n="$1" '$1 == n { s = $2 } END { print s }' "${STAGING_ARTIFACT_DIR}/stages.tsv"
}

# Render summary.json and summary.md from stages.tsv.
staging_write_summary() {
  local result="$1" run_id="$2" dir="${STAGING_ARTIFACT_DIR}"
  jq -Rn --arg result "${result}" --arg run "${run_id}" \
    --arg ref "${GITHUB_REF_NAME:-local}" --arg sha "${GITHUB_SHA:-${STAGING_GIT_SHA:-unknown}}" \
    --arg runner "${STAGING_RUNNER_KIND:-unknown}" '
    [inputs | select(length > 0) | split("\t") |
      {stage: .[0], status: .[1], seconds: (.[2] | tonumber? // 0), log: .[3], detail: .[4], hint: .[5]}]
    | {result: $result, run_id: $run, runner: $runner, ref: $ref, sha: $sha,
       counts: (group_by(.status) | map({(.[0].status): length}) | add // {}),
       stages: .}' <"${dir}/stages.tsv" >"${dir}/summary.json"

  {
    printf '# Staging E2E %s: %s\n\n' "${STAGING_RUNNER_KIND:-run}" "$(printf '%s' "${result}" | tr '[:lower:]' '[:upper:]')"
    # shellcheck disable=SC2016 # backticks are Markdown, not command substitution
    printf -- '- Run: `%s`\n- Ref: `%s` (`%s`)\n\n' "${run_id}" "${GITHUB_REF_NAME:-local}" "${GITHUB_SHA:-${STAGING_GIT_SHA:-unknown}}"
    printf '| # | Stage | Result | Time | Detail | Artifact |\n|---|---|---|---|---|---|\n'
    awk -F'\t' '{
      icon = ($2 == "passed") ? "pass" : ($2 == "failed") ? "**FAIL**" : "skip"
      detail = $5; if ($2 == "failed" && $6 != "") detail = detail "; likely cause: " $6
      printf "| %d | %s | %s | %ss | %s | %s |\n", NR, $1, icon, $3, detail, ($4 == "" ? "-" : "`" $4 "`")
    }' "${dir}/stages.tsv"
    if grep -q $'\tfailed\t' "${dir}/stages.tsv"; then
      printf '\n## Failures\n\n'
      awk -F'\t' '$2 == "failed" { printf "- **%s**: %s. Open `%s`, then `diagnostics/` (pods, events, describe, logs, certificates, ingress).\n", $1, $6, $4 }' "${dir}/stages.tsv"
    fi
  } >"${dir}/summary.md"
}

# Print the failure summary to the job log and, when available, the GitHub
# step summary. Returns non-zero if any stage failed.
staging_finish() {
  local run_id="$1" result=passed
  [[ "${STAGING_FAILED}" == "1" ]] && result=failed
  staging_write_summary "${result}" "${run_id}"
  staging_log "stage summary (${result}):"
  awk -F'\t' -v p="${STAGING_LOG_PREFIX}" '{ printf "[%s]   %-22s %-8s %5ss %s\n", p, $1, $2, $3, $5 }' "${STAGING_ARTIFACT_DIR}/stages.tsv"
  if [[ "${result}" == "failed" ]]; then
    awk -F'\t' -v p="${STAGING_LOG_PREFIX}" '$2 == "failed" {
      printf "[%s] FAILED %s -- likely cause: %s -- open %s and diagnostics/\n", p, $1, $6, $4 }' "${STAGING_ARTIFACT_DIR}/stages.tsv" >&2
  fi
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" && -w "${GITHUB_STEP_SUMMARY}" ]]; then
    cat "${STAGING_ARTIFACT_DIR}/summary.md" >>"${GITHUB_STEP_SUMMARY}" || true
  fi
  [[ "${result}" == "passed" ]]
}

# ---------------------------------------------------------------------------
# Diagnostics (always collected; never includes Secret data)
# ---------------------------------------------------------------------------

staging_platform_namespaces() {
  printf '%s\n' mcp-runtime mcp-sentinel registry cert-manager mcp-servers traefik kube-system
}

staging_collect_diagnostics() {
  local out="$1" ns deploy
  mkdir -p "${out}"
  kubectl get nodes -o wide >"${out}/nodes.txt" 2>&1 || return 0
  kubectl describe nodes >"${out}/nodes-describe.txt" 2>&1 || true
  kubectl get pods -A -o wide >"${out}/pods.txt" 2>&1 || true
  kubectl get deploy,sts,ds -A -o wide >"${out}/workloads.txt" 2>&1 || true
  kubectl get svc -A >"${out}/services.txt" 2>&1 || true
  kubectl get events -A --sort-by=.lastTimestamp >"${out}/events.txt" 2>&1 || true
  kubectl get pvc -A >"${out}/pvcs.txt" 2>&1 || true
  kubectl get ingress -A -o yaml >"${out}/ingresses.yaml" 2>&1 || true
  kubectl get ingress -A >"${out}/ingresses.txt" 2>&1 || true
  kubectl get ingressroutes.traefik.io,middlewares.traefik.io,tlsoptions.traefik.io,tlsstores.traefik.io,serverstransports.traefik.io -A -o yaml \
    >"${out}/traefik-crs.yaml" 2>&1 || true
  kubectl get clusterissuers -o yaml >"${out}/clusterissuers.yaml" 2>&1 || true
  kubectl get certificates,certificaterequests,orders,challenges -A -o wide >"${out}/certificates.txt" 2>&1 || true
  kubectl describe certificates,certificaterequests,orders,challenges -A >"${out}/certificates-describe.txt" 2>&1 || true
  # Names and types only: Secret data is never collected.
  kubectl get secrets -A >"${out}/secret-names.txt" 2>&1 || true
  kubectl get mcpservers -A -o yaml >"${out}/mcpservers.yaml" 2>&1 || true
  kubectl get mcpaccessgrants,mcpagentsessions -A >"${out}/grants-sessions.txt" 2>&1 || true
  kubectl get configmap mcp-sentinel-config -n mcp-sentinel -o yaml >"${out}/mcp-sentinel-config.yaml" 2>&1 || true
  while IFS= read -r ns; do
    kubectl get namespace "${ns}" >/dev/null 2>&1 || continue
    kubectl -n "${ns}" get pods -o wide >"${out}/${ns}-pods.txt" 2>&1 || true
    kubectl -n "${ns}" describe pods >"${out}/${ns}-pods-describe.txt" 2>&1 || true
    for deploy in $(kubectl -n "${ns}" get deploy -o name 2>/dev/null); do
      kubectl -n "${ns}" logs "${deploy}" --all-containers --prefix --tail=400 \
        >"${out}/${ns}-${deploy#deployment.apps/}.log" 2>&1 || true
    done
  done < <(staging_platform_namespaces)
  local pair secret_ns secret_name
  for pair in registry/registry-tls mcp-sentinel/mcp-sentinel-platform-tls; do
    secret_ns="${pair%%/*}"
    secret_name="${pair##*/}"
    kubectl -n "${secret_ns}" get secret "${secret_name}" -o jsonpath='{.data.tls\.crt}' 2>/dev/null |
      base64 --decode 2>/dev/null |
      openssl x509 -noout -subject -issuer -dates -serial -ext subjectAltName \
        >"${out}/cert-${secret_ns}-${secret_name}.txt" 2>&1 || true
  done
}

# ---------------------------------------------------------------------------
# 3. Shared assertions. Each runs as a stage body (errexit on, in a subshell).
# ---------------------------------------------------------------------------

# Required by the assertions below:
#   BIN PLATFORM_URL MCP_URL REGISTRY_HOST AUTH_URL WORK_DIR RUN_ID ROOT_DIR
#   STAGING_ARTIFACT_DIR E2E_PLATFORM_API_TOKEN (after platform-login)

staging_acme_staging() { staging_flag_enabled "${E2E_ACME_STAGING:-1}"; }

staging_expected_issuer() {
  if staging_acme_staging; then printf 'letsencrypt-staging'; else printf 'letsencrypt-prod'; fi
}

staging_ca_bundle() {
  local candidate
  for candidate in "${STAGING_CA_BUNDLE:-}" "${CURL_CA_BUNDLE:-}" "${SSL_CERT_FILE:-}" \
    /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt; do
    if [[ -n "${candidate}" && -r "${candidate}" ]]; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}

staging_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

staging_traefik_namespace() {
  local candidate
  for candidate in traefik kube-system; do
    if kubectl -n "${candidate}" get deploy traefik >/dev/null 2>&1; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}

# Check names from `cluster diagnostics`/`cluster doctor` that failed, one per
# line ("ERROR  <check name> — <message>").
staging_failed_checks() {
  awk '{ gsub(/\033\[[0-9;]*m/, "") } /^ *ERROR +[^ ]/ { sub(/^ *ERROR +/, ""); sub(/ — .*$/, ""); print }' "$1" | sort -u
}

# Findings that fail on every fresh staging install for reasons outside the
# suite, accepted by name so every other check stays a hard gate. Override with
# E2E_DIAGNOSTICS_ACCEPTED (a |-separated list; empty = strict).
#   - "sentinel OIDC configuration": tenant mode wants Google/OIDC login, and the
#     staging VM has no identity provider unless E2E_WITH_MCP_AUTH/OIDC is set.
#   - "mcp-servers image pull smoke" / "MCPServer reconcile smoke": the doctor
#     smoke pod in mcp-servers pulls from the auth-protected public registry
#     before any managed deploy has provisioned the mcp-workload pull secret, so
#     the pull is refused (no basic auth credentials). Tracked as a product
#     finding; tenant pulls are asserted by the image-pulls and multitenancy
#     stages instead.
staging_default_accepted_checks() {
  local accepted="mcp-servers image pull smoke|MCPServer reconcile smoke"
  if [[ -z "${OIDC_ISSUER:-}${GOOGLE_CLIENT_ID:-}${MCP_GOOGLE_CLIENT_ID:-}" ]] &&
    ! staging_flag_enabled "${E2E_WITH_MCP_AUTH:-0}"; then
    accepted+="|sentinel OIDC configuration"
  fi
  printf '%s' "${accepted}"
}

staging_check_diagnostics() {
  local dir="${STAGING_ARTIFACT_DIR}" rc_diag=0 rc_doctor=0 rc_status=0
  "${BIN}" cluster diagnostics >"${dir}/diagnostics-after.log" 2>&1 || rc_diag=$?
  cat "${dir}/diagnostics-after.log"
  "${BIN}" cluster doctor >"${dir}/doctor-after.log" 2>&1 || rc_doctor=$?
  cat "${dir}/doctor-after.log"
  "${BIN}" cluster status >"${dir}/cluster-status.log" 2>&1 || rc_status=$?
  cat "${dir}/cluster-status.log"
  staging_log "exit codes: diagnostics=${rc_diag} doctor=${rc_doctor} status=${rc_status}"
  [[ ${rc_status} -eq 0 ]] || {
    staging_err "cluster status failed"
    return 1
  }
  local accepted="${E2E_DIAGNOSTICS_ACCEPTED-$(staging_default_accepted_checks)}" failed_checks check unexpected="" known=""
  failed_checks="$( (staging_failed_checks "${dir}/diagnostics-after.log"; staging_failed_checks "${dir}/doctor-after.log") | sort -u)"
  while IFS= read -r check; do
    [[ -n "${check}" ]] || continue
    if [[ -n "${accepted}" && "|${accepted}|" == *"|${check}|"* ]]; then
      known+="${known:+, }${check}"
    else
      unexpected+="${unexpected:+, }${check}"
    fi
  done <<<"${failed_checks}"
  [[ -n "${known}" ]] && staging_note "accepted known findings: ${known}"
  if [[ -n "${unexpected}" ]]; then
    staging_note "failed checks: ${unexpected}"
    staging_err "diagnostics/doctor failed checks: ${unexpected}"
    return 1
  fi
  if [[ ${rc_diag} -ne 0 || ${rc_doctor} -ne 0 ]] && [[ -z "${known}" ]]; then
    staging_err "diagnostics/doctor exited non-zero without a recognizable failed check"
    return 1
  fi
  staging_log "diagnostics/doctor: no unexpected failed checks${known:+ (accepted: ${known})}"
}

staging_check_rollouts() {
  local ns kind name failed=0 traefik_ns
  traefik_ns="$(staging_traefik_namespace || true)"
  for ns in mcp-runtime mcp-sentinel registry cert-manager ${traefik_ns}; do
    if ! kubectl get namespace "${ns}" >/dev/null 2>&1; then
      staging_err "namespace ${ns} is missing"
      failed=1
      continue
    fi
    for kind in deploy sts; do
      for name in $(kubectl -n "${ns}" get "${kind}" -o name 2>/dev/null); do
        if [[ "${ns}" == "kube-system" && "${name}" != */traefik ]]; then
          continue
        fi
        if kubectl -n "${ns}" rollout status "${name}" --timeout="${E2E_ROLLOUT_TIMEOUT:-300s}"; then
          staging_log "rollout ok: ${ns}/${name#*/}"
        else
          staging_err "rollout not complete: ${ns}/${name#*/}"
          failed=1
        fi
      done
    done
  done
  local bad
  bad="$(kubectl get pods -A -o json | jq -r '
    .items[] | . as $p
    | ([.status.containerStatuses[]?, .status.initContainerStatuses[]?]
       | map(select(.state.waiting.reason // "" | test("ErrImagePull|ImagePullBackOff|CrashLoopBackOff|CreateContainerConfigError|InvalidImageName")))
       | .[0]) as $c
    | select($c != null)
    | "\($p.metadata.namespace)/\($p.metadata.name): \($c.state.waiting.reason)"')"
  if [[ -n "${bad}" ]]; then
    staging_err "pods stuck waiting:"
    printf '%s\n' "${bad}" >&2
    failed=1
  fi
  [[ "${failed}" == "0" ]]
}

staging_check_cluster_issuer() {
  local issuer server
  issuer="$(staging_expected_issuer)"
  kubectl wait --for=condition=Ready "clusterissuer/${issuer}" --timeout=180s
  server="$(kubectl get clusterissuer "${issuer}" -o jsonpath='{.spec.acme.server}')"
  staging_log "ClusterIssuer ${issuer} Ready; ACME server ${server}"
  if staging_acme_staging; then
    [[ "${server}" == *acme-staging-v02.api.letsencrypt.org* ]] || {
      staging_err "expected the Let's Encrypt staging ACME directory, got ${server}"
      return 1
    }
  else
    [[ "${server}" == *acme-v02.api.letsencrypt.org* ]] || {
      staging_err "expected the Let's Encrypt production ACME directory, got ${server}"
      return 1
    }
  fi
}

# staging_verify_cert_pem FILE HOST... -- SAN coverage, validity window and,
# under the staging CA, a staging issuer.
staging_verify_cert_pem() {
  local pem="$1" host issuer
  shift
  openssl x509 -in "${pem}" -noout -subject -issuer -dates -serial -ext subjectAltName
  for host in "$@"; do
    if ! openssl x509 -in "${pem}" -noout -checkhost "${host}" | grep -q 'does match'; then
      staging_err "certificate does not cover ${host}"
      return 1
    fi
  done
  if ! openssl x509 -in "${pem}" -noout -checkend 86400 >/dev/null; then
    staging_err "certificate expires within 24h or is already expired"
    return 1
  fi
  issuer="$(openssl x509 -in "${pem}" -noout -issuer)"
  if staging_acme_staging && [[ "${issuer}" != *"(STAGING)"* ]]; then
    staging_err "E2E_ACME_STAGING is on but the certificate issuer is not a Let's Encrypt staging CA: ${issuer}"
    return 1
  fi
  if ! staging_acme_staging && [[ "${issuer}" == *"(STAGING)"* ]]; then
    staging_err "E2E_ACME_STAGING is off but the certificate was issued by the staging CA"
    return 1
  fi
}

staging_check_certificates() {
  local issuer failed=0 entry ns cert secret hosts pem ref
  issuer="$(staging_expected_issuer)"
  local mcp_host platform_host
  mcp_host="$(staging_url_host "${MCP_URL}")"
  platform_host="$(staging_url_host "${PLATFORM_URL}")"
  for entry in "registry|registry-cert|registry-tls|${REGISTRY_HOST} ${mcp_host}" \
    "mcp-sentinel|mcp-sentinel-platform-tls|mcp-sentinel-platform-tls|${platform_host}"; do
    IFS='|' read -r ns cert secret hosts <<<"${entry}"
    if ! kubectl -n "${ns}" wait --for=condition=Ready "certificate/${cert}" --timeout=300s; then
      staging_err "Certificate ${ns}/${cert} is not Ready"
      kubectl -n "${ns}" describe certificate "${cert}" || true
      failed=1
      continue
    fi
    ref="$(kubectl -n "${ns}" get certificate "${cert}" -o jsonpath='{.spec.issuerRef.kind}/{.spec.issuerRef.name}')"
    staging_log "Certificate ${ns}/${cert} Ready (issuerRef ${ref})"
    if [[ "${ref}" != "ClusterIssuer/${issuer}" ]]; then
      staging_err "Certificate ${ns}/${cert} references ${ref}, want ClusterIssuer/${issuer}"
      failed=1
    fi
    pem="${WORK_DIR}/cert-${secret}.pem"
    kubectl -n "${ns}" get secret "${secret}" -o jsonpath='{.data.tls\.crt}' | base64 --decode >"${pem}"
    # shellcheck disable=SC2086 # hosts is a space-separated list on purpose
    if ! staging_verify_cert_pem "${pem}" ${hosts}; then
      failed=1
    fi
  done
  [[ "${failed}" == "0" ]]
}

# staging_check_tls_endpoint HOST [SNI] [CONNECT_HOST] -- full handshake with
# chain verification against the trust bundle, hostname match, and issuer.
staging_check_tls_endpoint() {
  local host="$1" sni="${2:-$1}" connect="${3:-$1}" bundle out leaf
  bundle="$(staging_ca_bundle)" || {
    staging_err "no CA bundle available for chain verification"
    return 1
  }
  out="${WORK_DIR}/s_client-${sni}.txt"
  leaf="${WORK_DIR}/leaf-${sni}.pem"
  if ! openssl s_client -connect "${connect}:443" -servername "${sni}" -verify_hostname "${sni}" \
    -verify_return_error -CAfile "${bundle}" -showcerts </dev/null >"${out}" 2>&1; then
    staging_err "TLS handshake/verification failed for ${sni}"
    grep -E 'verify error|Verify return code|depth=' "${out}" >&2 || tail -20 "${out}" >&2
    return 1
  fi
  # Callers use this in `if`, where errexit does not apply: return explicitly.
  grep -E 'Verify return code' "${out}" || true
  awk '/BEGIN CERTIFICATE/ { p = 1 } p { print } /END CERTIFICATE/ { exit }' "${out}" >"${leaf}"
  staging_verify_cert_pem "${leaf}" "${sni}" || return 1
  # Chain depth evidence: staging chains are leaf -> (STAGING) intermediate.
  grep -E '^ *[0-9] s:|^ *i:' "${out}" | head -6 || true
}

staging_check_tls_endpoints() {
  local host failed=0
  for host in "$(staging_url_host "${PLATFORM_URL}")" "${REGISTRY_HOST}" "$(staging_url_host "${MCP_URL}")"; do
    if staging_check_tls_endpoint "${host}"; then
      staging_log "TLS ok: ${host}"
    else
      failed=1
    fi
  done
  if staging_flag_enabled "${E2E_WITH_MCP_AUTH:-0}"; then
    host="$(staging_url_host "${AUTH_URL}")"
    if staging_check_tls_endpoint "${host}"; then staging_log "TLS ok: ${host}"; else failed=1; fi
  fi
  [[ "${failed}" == "0" ]]
}

# A short DNS label that is unique per run: run-<id>.<suffix>.
# shellcheck disable=SC2153 # RUN_ID is set by the calling runner
staging_fresh_host() {
  local id
  id="$(printf '%s' "${RUN_ID}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | tail -c 24)"
  printf 'run-%s.%s' "${id}" "$(staging_disposable_suffix)"
}

# Issue a brand-new certificate for a never-before-used hostname on the
# staging issuer, serve it through Traefik, and verify the served chain. Gated
# behind E2E_FRESH_CERTIFICATE so routine reruns reuse the snapshot.
staging_check_fresh_certificate() {
  if ! staging_flag_enabled "${E2E_FRESH_CERTIFICATE:-0}"; then
    staging_skip "fresh-certificate input is false; routine runs reuse the TLS snapshot"
  fi
  if ! staging_acme_staging && ! staging_flag_enabled "${E2E_FRESH_CERT_ALLOW_PRODUCTION_CA:-0}"; then
    staging_skip "fresh issuance is limited to the staging CA (set E2E_FRESH_CERT_ALLOW_PRODUCTION_CA=1 to override)"
  fi
  local host issuer name ns=mcp-sentinel platform_ips host_ips
  host="$(staging_fresh_host)"
  issuer="$(staging_expected_issuer)"
  name="e2e-fresh-$(printf '%s' "${host%%.*}" | cut -c5-)"
  staging_guard_hosts "" "${host}"
  platform_ips="$(staging_resolve_ipv4 "$(staging_url_host "${PLATFORM_URL}")")"
  host_ips="$(staging_resolve_ipv4 "${host}")"
  if [[ "${platform_ips}" != "${host_ips}" ]]; then
    staging_err "${host} resolves to '${host_ips}', not the VM address '${platform_ips}'; wildcard DNS for *.$(staging_disposable_suffix) is required"
    return 1
  fi
  staging_log "requesting a fresh ${issuer} certificate for ${host}"
  # shellcheck disable=SC2064 # expand now: the names are fixed for this stage
  trap "kubectl -n ${ns} delete ingress/${name} certificate/${name} secret/${name}-tls --ignore-not-found >/dev/null 2>&1 || true" EXIT
  kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app.kubernetes.io/managed-by: staging-e2e
spec:
  secretName: ${name}-tls
  dnsNames:
    - ${host}
  issuerRef:
    kind: ClusterIssuer
    name: ${issuer}
  privateKey:
    rotationPolicy: Always
EOF
  if ! kubectl -n "${ns}" wait --for=condition=Ready "certificate/${name}" --timeout="${E2E_FRESH_CERT_TIMEOUT:-420s}"; then
    staging_err "fresh certificate for ${host} was not issued"
    kubectl -n "${ns}" describe certificate "${name}" || true
    kubectl -n "${ns}" describe certificaterequests,orders,challenges || true
    return 1
  fi
  local pem="${WORK_DIR}/fresh-${name}.pem"
  kubectl -n "${ns}" get secret "${name}-tls" -o jsonpath='{.data.tls\.crt}' | base64 --decode >"${pem}"
  staging_verify_cert_pem "${pem}" "${host}"
  kubectl apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app.kubernetes.io/managed-by: staging-e2e
spec:
  ingressClassName: traefik
  tls:
    - hosts: [${host}]
      secretName: ${name}-tls
  rules:
    - host: ${host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: mcp-sentinel-ui
                port:
                  number: 8082
EOF
  local attempt
  for attempt in $(seq 1 20); do
    if staging_check_tls_endpoint "${host}" >"${WORK_DIR}/fresh-tls-attempt.log" 2>&1; then
      cat "${WORK_DIR}/fresh-tls-attempt.log"
      jq -n --arg host "${host}" --arg issuer "$(openssl x509 -in "${pem}" -noout -issuer)" \
        --arg serial "$(openssl x509 -in "${pem}" -noout -serial)" \
        --arg notBefore "$(openssl x509 -in "${pem}" -noout -startdate)" \
        '{host: $host, issuer: $issuer, serial: $serial, notBefore: $notBefore}' \
        >"${STAGING_ARTIFACT_DIR}/fresh-certificate.json"
      staging_log "fresh certificate issued and served for ${host}"
      return 0
    fi
    sleep 6
  done
  cat "${WORK_DIR}/fresh-tls-attempt.log" >&2
  staging_err "Traefik never served the fresh certificate for ${host}"
  return 1
}

staging_api_curl() {
  curl --silent --show-error -H "x-api-key: ${E2E_PLATFORM_API_TOKEN}" \
    -H "authorization: Bearer ${E2E_PLATFORM_API_TOKEN}" "$@"
}

staging_http_code() { curl --silent --output /dev/null --write-out '%{http_code}' "$@" || true; }

staging_expect_code() {
  local label="$1" want="$2" got
  shift 2
  got="$(staging_http_code "$@")"
  if [[ " ${want} " == *" ${got} "* ]]; then
    staging_log "${label}: HTTP ${got} (want ${want})"
    return 0
  fi
  staging_err "${label}: HTTP ${got}, want one of ${want}"
  return 1
}

staging_check_platform_login() {
  local token="${E2E_PLATFORM_API_TOKEN:-}"
  if [[ -z "${token}" ]] || [[ "$(curl --silent --output /dev/null --write-out '%{http_code}' \
    -H "x-api-key: ${token}" -H "authorization: Bearer ${token}" "${PLATFORM_URL}/api/v1/auth/me")" != "200" ]]; then
    local encoded
    encoded="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel -o jsonpath='{.data.ADMIN_API_KEYS}' 2>/dev/null || true)"
    token="$(printf '%s' "${encoded}" | base64 --decode 2>/dev/null | cut -d',' -f1 | tr -d '\r\n')"
    [[ -n "${token}" ]] || {
      staging_err "setup did not produce an ADMIN_API_KEYS value and no E2E_PLATFORM_API_TOKEN was accepted"
      return 1
    }
    staging_log "using the first generated admin API key from mcp-sentinel-secrets"
  fi
  staging_state_set E2E_PLATFORM_API_TOKEN "${token}"
  export E2E_PLATFORM_API_TOKEN="${token}"

  local command
  for command in auth bootstrap cluster catalog registry server access adapter admin setup status sentinel team; do
    "${BIN}" "${command}" --help >"${STAGING_ARTIFACT_DIR}/help-${command}.txt"
  done
  printf '%s' "${token}" | "${BIN}" auth login --api-url "${PLATFORM_URL}" --profile e2e --token-stdin
  MCP_PLATFORM_API_PROFILE=e2e "${BIN}" auth status
  MCP_PLATFORM_API_PROFILE=e2e "${BIN}" status | tee "${STAGING_ARTIFACT_DIR}/cli-status.txt"
  MCP_PLATFORM_API_PROFILE=e2e "${BIN}" server list | tee "${STAGING_ARTIFACT_DIR}/cli-server-list.txt"
  MCP_PLATFORM_API_PROFILE=e2e "${BIN}" registry info | tee "${STAGING_ARTIFACT_DIR}/cli-registry-info.txt"
}

staging_check_platform_api() {
  local name path failed=0
  for entry in auth-me:/api/v1/auth/me servers:/api/v1/runtime/servers teams:/api/v1/runtime/teams \
    components:/api/v1/runtime/components; do
    name="${entry%%:*}"
    path="${entry#*:}"
    if staging_api_curl --fail "${PLATFORM_URL}${path}" >"${STAGING_ARTIFACT_DIR}/api-${name}.json"; then
      staging_log "GET ${path}: ok"
    else
      staging_err "GET ${path} failed with the admin key"
      failed=1
    fi
  done
  jq -e '(.role // .user.role // .principal.role // "") | test("admin")' \
    "${STAGING_ARTIFACT_DIR}/api-auth-me.json" >/dev/null ||
    { staging_err "/api/v1/auth/me did not report an admin role"; failed=1; }

  staging_expect_code "anonymous GET /api/v1/runtime/servers" "401 403" "${PLATFORM_URL}/api/v1/runtime/servers" || failed=1
  staging_expect_code "bad key GET /api/v1/runtime/servers" "401 403" \
    -H "x-api-key: staging-e2e-invalid-key" "${PLATFORM_URL}/api/v1/runtime/servers" || failed=1
  staging_expect_code "anonymous POST /api/v1/runtime/grants" "401 403" -X POST \
    -H 'content-type: application/json' --data '{}' "${PLATFORM_URL}/api/v1/runtime/grants" || failed=1

  # Password login is the path a human takes in the UI and CLI. The password
  # goes through stdin, never argv.
  if [[ -n "${MCP_PLATFORM_ADMIN_EMAIL:-}" && -n "${MCP_PLATFORM_ADMIN_PASSWORD:-}" ]]; then
    local body="${WORK_DIR}/password-login.json" code
    code="$(jq -n --arg e "${MCP_PLATFORM_ADMIN_EMAIL}" --arg p "${MCP_PLATFORM_ADMIN_PASSWORD}" '{email: $e, password: $p}' |
      curl --silent --output "${body}" --write-out '%{http_code}' -X POST -H 'content-type: application/json' \
        --data-binary @- "${PLATFORM_URL}/api/v1/auth/login" || true)"
    if [[ "${code}" == "200" ]] && jq -e '.access_token | length > 0' "${body}" >/dev/null; then
      local access
      access="$(jq -r '.access_token' "${body}")"
      staging_expect_code "password-login token GET /api/v1/auth/me" "200" \
        -H "authorization: Bearer ${access}" "${PLATFORM_URL}/api/v1/auth/me" || failed=1
    else
      staging_err "admin password login returned HTTP ${code}"
      failed=1
    fi
    rm -f "${body}"
    code="$(jq -n --arg e "${MCP_PLATFORM_ADMIN_EMAIL}" '{email: $e, password: "staging-e2e-wrong-password"}' |
      curl --silent --output /dev/null --write-out '%{http_code}' -X POST -H 'content-type: application/json' \
        --data-binary @- "${PLATFORM_URL}/api/v1/auth/login" || true)"
    if [[ "${code}" == "401" || "${code}" == "403" ]]; then
      staging_log "wrong-password login denied: HTTP ${code}"
    else
      staging_err "wrong-password login returned HTTP ${code}, want 401/403"
      failed=1
    fi
  else
    staging_log "admin email/password not configured; password login not exercised"
  fi
  [[ "${failed}" == "0" ]]
}

# The public registry route must answer anonymous /v2/ with 401 (auth
# required). Traefik's plain 404 means the registry Ingress rule host no
# longer matches the public hostname (e.g. downgraded to registry.local), which
# breaks every node image pull. Run before and after the user-flow stages.
staging_check_registry_route() {
  local code hosts
  hosts="$(kubectl get ingress registry -n registry \
    -o jsonpath='rules={.spec.rules[*].host} tls={.spec.tls[*].hosts}' 2>/dev/null || true)"
  staging_log "registry Ingress hosts: ${hosts:-<missing>}"
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 "https://${REGISTRY_HOST}/v2/" || true)"
  staging_log "anonymous GET https://${REGISTRY_HOST}/v2/ -> ${code}"
  if [[ "${code}" != "401" ]]; then
    staging_err "expected 401 from https://${REGISTRY_HOST}/v2/, got ${code} (404 = Traefik has no router for the registry host; run mcp-runtime cluster doctor)"
    return 1
  fi
  if [[ "${hosts}" == *"rules=registry.local"* ]]; then
    staging_err "registry Ingress rule host is registry.local on a public install"
    return 1
  fi
}

staging_check_registry_auth() {
  local reg="https://${REGISTRY_HOST}" failed=0 image repo tag probe accept
  accept='application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.index.v1+json'
  image="$(kubectl -n mcp-runtime get deploy mcp-runtime-operator-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}')"
  staging_log "operator image: ${image}"
  if [[ "${image}" != "${REGISTRY_HOST}/"* ]]; then
    staging_err "operator image is not served from ${REGISTRY_HOST}"
    failed=1
  fi
  repo="${image#"${REGISTRY_HOST}/"}"
  tag="${repo##*:}"
  repo="${repo%:*}"

  staging_expect_code "anonymous GET /v2/" "401 403" "${reg}/v2/" || failed=1
  staging_expect_code "anonymous manifest HEAD ${repo}:${tag}" "401 403" -I -H "accept: ${accept}" \
    "${reg}/v2/${repo}/manifests/${tag}" || failed=1
  probe="staging-e2e-probe-$(printf '%s' "${RUN_ID}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | tail -c 16)"
  staging_expect_code "anonymous push (blob upload) to ${probe}" "401 403" -X POST \
    "${reg}/v2/${probe}/blobs/uploads/" || failed=1

  # Basic auth with any username and an API key as the password is the
  # credential docker/skopeo present. -K keeps the key off the command line.
  local auth_cfg="${WORK_DIR}/registry-auth.curlrc"
  printf 'user = "staging-e2e:%s"\n' "${E2E_PLATFORM_API_TOKEN}" >"${auth_cfg}"
  chmod 600 "${auth_cfg}"
  staging_expect_code "authenticated GET /v2/" "200" -K "${auth_cfg}" "${reg}/v2/" || failed=1
  staging_expect_code "authenticated manifest HEAD ${repo}:${tag}" "200" -K "${auth_cfg}" -I \
    -H "accept: ${accept}" "${reg}/v2/${repo}/manifests/${tag}" || failed=1

  # Authenticated push of a minimal OCI artifact: config blob + manifest.
  local cfg="${WORK_DIR}/probe-config.json" manifest="${WORK_DIR}/probe-manifest.json" digest size location
  printf '{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"config":{}}' >"${cfg}"
  digest="sha256:$(staging_sha256 "${cfg}")"
  size="$(wc -c <"${cfg}" | tr -d ' ')"
  location="$(curl --silent --show-error -K "${auth_cfg}" -X POST -o /dev/null -D - \
    "${reg}/v2/${probe}/blobs/uploads/" | awk 'tolower($1) == "location:" { sub(/\r$/, "", $2); print $2 }')"
  if [[ -z "${location}" ]]; then
    staging_err "authenticated blob upload did not return a Location"
    failed=1
  else
    [[ "${location}" == http* ]] || location="${reg}${location}"
    local sep='?'
    [[ "${location}" == *\?* ]] && sep='&'
    staging_expect_code "authenticated blob PUT" "201" -K "${auth_cfg}" -X PUT \
      -H 'content-type: application/octet-stream' --data-binary "@${cfg}" \
      "${location}${sep}digest=${digest}" || failed=1
    jq -n --arg d "${digest}" --argjson s "${size}" '{schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {mediaType: "application/vnd.oci.image.config.v1+json", digest: $d, size: $s},
      layers: []}' >"${manifest}"
    staging_expect_code "authenticated manifest PUT ${probe}:probe" "201" -K "${auth_cfg}" -X PUT \
      -H 'content-type: application/vnd.oci.image.manifest.v1+json' --data-binary "@${manifest}" \
      "${reg}/v2/${probe}/manifests/probe" || failed=1
    staging_expect_code "authenticated pull ${probe}:probe" "200" -K "${auth_cfg}" \
      -H 'accept: application/vnd.oci.image.manifest.v1+json' "${reg}/v2/${probe}/manifests/probe" || failed=1
    staging_expect_code "anonymous pull ${probe}:probe" "401 403" \
      -H 'accept: application/vnd.oci.image.manifest.v1+json' "${reg}/v2/${probe}/manifests/probe" || failed=1
  fi
  rm -f "${auth_cfg}"
  [[ "${failed}" == "0" ]]
}

# In-cluster pulls: platform workloads pull from the public registry host with
# the provisioned pull secret, and a pod without that secret is refused.
staging_check_image_pulls() {
  local failed=0 ns deploy image secrets
  for ns in mcp-runtime mcp-sentinel; do
    for deploy in $(kubectl -n "${ns}" get deploy -o name); do
      image="$(kubectl -n "${ns}" get "${deploy}" -o jsonpath='{.spec.template.spec.containers[0].image}')"
      [[ "${image}" == "${REGISTRY_HOST}/"* ]] || continue
      secrets="$(kubectl -n "${ns}" get "${deploy}" -o jsonpath='{.spec.template.spec.imagePullSecrets[*].name}')"
      if [[ " ${secrets} " == *" mcp-runtime-registry-pull "* ]]; then
        staging_log "${ns}/${deploy#*/}: ${image} with pull secret mcp-runtime-registry-pull"
      else
        staging_err "${ns}/${deploy#*/} pulls ${image} without mcp-runtime-registry-pull (has: ${secrets:-none})"
        failed=1
      fi
    done
  done

  image="$(kubectl -n mcp-runtime get deploy mcp-runtime-operator-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}')"
  local sa=staging-e2e-pull-probe pod_ok=staging-e2e-pull-authorized pod_denied=staging-e2e-pull-anonymous
  # shellcheck disable=SC2064
  trap "kubectl -n mcp-sentinel delete pod ${pod_ok} ${pod_denied} --ignore-not-found --wait=false >/dev/null 2>&1; kubectl -n mcp-sentinel delete sa ${sa} --ignore-not-found >/dev/null 2>&1 || true" EXIT
  kubectl -n mcp-sentinel create serviceaccount "${sa}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  local pod secret_block
  for pod in "${pod_ok}" "${pod_denied}"; do
    secret_block=""
    [[ "${pod}" == "${pod_ok}" ]] && secret_block=$'  imagePullSecrets:\n    - name: mcp-runtime-registry-pull'
    kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  namespace: mcp-sentinel
  labels:
    app.kubernetes.io/managed-by: staging-e2e
spec:
  serviceAccountName: ${sa}
  automountServiceAccountToken: false
  restartPolicy: Never
${secret_block}
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: probe
      image: ${image}
      imagePullPolicy: Always
      args: ["--help"]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
      resources:
        requests:
          cpu: 1m
          memory: 16Mi
EOF
  done
  local deadline=$((SECONDS + 180)) ok_id="" denied_reason=""
  while ((SECONDS < deadline)); do
    ok_id="$(kubectl -n mcp-sentinel get pod "${pod_ok}" -o jsonpath='{.status.containerStatuses[0].imageID}' 2>/dev/null || true)"
    denied_reason="$(kubectl -n mcp-sentinel get pod "${pod_denied}" -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || true)"
    if [[ -n "${ok_id}" && ( "${denied_reason}" == "ErrImagePull" || "${denied_reason}" == "ImagePullBackOff" ) ]]; then
      break
    fi
    sleep 5
  done
  kubectl -n mcp-sentinel get events --field-selector "involvedObject.name=${pod_denied}" -o custom-columns=REASON:.reason,MESSAGE:.message 2>/dev/null |
    sed -E 's/(Basic|Bearer) [A-Za-z0-9+/=._-]+/\1 <redacted>/g' | tail -5 || true
  if [[ -n "${ok_id}" ]]; then
    staging_log "authorized in-cluster pull ok (${ok_id##*@})"
  else
    staging_err "authorized in-cluster pull of ${image} did not complete"
    kubectl -n mcp-sentinel describe pod "${pod_ok}" | tail -20 >&2 || true
    failed=1
  fi
  if [[ "${denied_reason}" == "ErrImagePull" || "${denied_reason}" == "ImagePullBackOff" ]]; then
    staging_log "anonymous in-cluster pull refused (${denied_reason})"
  else
    staging_err "pull without the registry secret was not refused (state: ${denied_reason:-pulled})"
    failed=1
  fi
  [[ "${failed}" == "0" ]]
}

staging_check_ui() {
  local headers="${STAGING_ARTIFACT_DIR}/ui-headers.txt" html="${STAGING_ARTIFACT_DIR}/ui-index.html" code platform_host
  code="$(curl --silent --show-error -D "${headers}" -o "${html}" --write-out '%{http_code}' "${PLATFORM_URL}/")"
  if [[ "${code}" != "200" ]] || ! grep -qi '<html' "${html}"; then
    staging_err "platform UI returned HTTP ${code} or no HTML"
    return 1
  fi
  staging_log "platform UI: HTTP 200 with HTML ($(wc -c <"${html}" | tr -d ' ') bytes)"
  grep -iE '^(strict-transport-security|content-security-policy|x-frame-options|x-content-type-options):' "${headers}" || true
  platform_host="$(staging_url_host "${PLATFORM_URL}")"
  code="$(staging_http_code "http://${platform_host}/")"
  staging_log "plain-HTTP request to ${platform_host}: HTTP ${code}"
  [[ "${code}" =~ ^30[0-9]$ ]] || staging_log "WARNING: plain HTTP is not redirected to HTTPS"
  grep -qi '^strict-transport-security:' "${headers}" || {
    staging_err "platform UI response has no Strict-Transport-Security header"
    return 1
  }
  grep -qi '^content-security-policy:' "${headers}" || {
    staging_err "platform UI response has no Content-Security-Policy header"
    return 1
  }
  # The UI proxies the API under the same origin; an anonymous API call through
  # it must be refused.
  staging_expect_code "anonymous UI-origin GET /api/v1/dashboard/summary" "401 403" \
    "${PLATFORM_URL}/api/v1/dashboard/summary"
}

staging_check_oidc() {
  local oidc_issuer
  oidc_issuer="$(kubectl -n mcp-sentinel get configmap mcp-sentinel-config -o jsonpath='{.data.OIDC_ISSUER}' 2>/dev/null || true)"
  staging_log "platform OIDC issuer configured: ${oidc_issuer:-<none>}"
  if ! staging_flag_enabled "${E2E_WITH_MCP_AUTH:-0}"; then
    staging_skip "E2E_WITH_MCP_AUTH is not enabled in e2e.env, so the mcp-auth/Keycloak OAuth path is not installed on this run"
  fi
  local dir="${STAGING_ARTIFACT_DIR}" issuer jwks
  curl --fail --silent --show-error "${AUTH_URL}/.well-known/openid-configuration" >"${dir}/auth-oidc-discovery.json"
  curl --fail --silent --show-error "${AUTH_URL}/.well-known/oauth-authorization-server" >"${dir}/auth-server-metadata.json"
  issuer="$(jq -r '.issuer' "${dir}/auth-oidc-discovery.json")"
  jwks="$(jq -r '.jwks_uri' "${dir}/auth-oidc-discovery.json")"
  staging_log "mcp-auth issuer ${issuer}"
  [[ "${issuer}" == "${AUTH_URL}"* ]] || {
    staging_err "discovery issuer ${issuer} is not under ${AUTH_URL}"
    return 1
  }
  curl --fail --silent --show-error "${jwks}" | jq -e '.keys | length > 0' >/dev/null
  staging_log "JWKS publishes signing keys"
  if [[ -n "${E2E_MCP_AUTH_ISSUER_URL:-}" ]]; then
    "${BIN}" auth provider-check --issuer-url "${E2E_MCP_AUTH_ISSUER_URL}"
  fi
  # An unauthenticated MCP request must be challenged towards the auth server.
  local challenge
  challenge="$(curl --silent --output /dev/null -D - -X POST -H 'content-type: application/json' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "${MCP_URL}/" | grep -i '^www-authenticate:' || true)"
  staging_log "MCP ingress challenge: ${challenge:-<none>}"
  if [[ -n "${E2E_OIDC_TEST_USERNAME:-}" && -n "${E2E_OIDC_TEST_PASSWORD:-}" && -n "${E2E_OIDC_TEST_CLIENT_ID:-}" ]]; then
    local token_endpoint body="${WORK_DIR}/oidc-token.json" code
    token_endpoint="$(curl --fail --silent "${E2E_MCP_AUTH_ISSUER_URL}/.well-known/openid-configuration" | jq -r '.token_endpoint')"
    code="$(curl --silent --output "${body}" --write-out '%{http_code}' -X POST "${token_endpoint}" \
      --data-urlencode grant_type=password --data-urlencode "client_id=${E2E_OIDC_TEST_CLIENT_ID}" \
      --data-urlencode "username=${E2E_OIDC_TEST_USERNAME}" --data-urlencode "password@-" \
      <<<"${E2E_OIDC_TEST_PASSWORD}" || true)"
    if [[ "${code}" != "200" ]] || ! jq -e '.access_token | length > 0' "${body}" >/dev/null; then
      rm -f "${body}"
      staging_err "Keycloak test-user login returned HTTP ${code}"
      return 1
    fi
    rm -f "${body}"
    staging_log "Keycloak test-user login issued an access token"
  else
    staging_log "E2E_OIDC_TEST_{USERNAME,PASSWORD,CLIENT_ID} not set; interactive Keycloak login not exercised"
  fi
}

# Adapter certificates are an opt-in platform feature. The operator layers
# optional client certificates onto gateway MCPServer routes when
# MCP_ADAPTER_CERTIFICATES=true, signing them with the platform-wide workload
# issuer (--mtls-cluster-issuer) under MCP_TRUST_DOMAIN. Path-based servers
# share the public MCP host, so the operator keeps one Traefik "default"
# TLSOption in MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE that requests, but
# never requires, a client certificate. Omitting MCPServer.spec.auth keeps
# OAuth off (cert-only); setting spec.auth enables OAuth + optional cert.
# Setup reads these from the environment.
STAGING_ADAPTER_TLS_NAMESPACE_DEFAULT="mcp-servers"

# The SPIFFE trust domain is a name, not a DNS lookup; default it to the
# disposable suffix so staging identities never look like production ones.
staging_adapter_trust_domain() {
  printf '%s' "${E2E_ADAPTER_TRUST_DOMAIN:-${E2E_MTLS_TRUST_DOMAIN:-$(staging_disposable_suffix)}}"
}

# Export the setup environment that enables adapter certificates. Off when no
# workload issuer is configured (E2E_MTLS_CLUSTER_ISSUER empty) or when
# E2E_ADAPTER_CERTIFICATES is false.
staging_configure_adapter_certificates() {
  if [[ -z "${E2E_MTLS_CLUSTER_ISSUER:-}" ]] || ! staging_flag_enabled "${E2E_ADAPTER_CERTIFICATES:-1}"; then
    unset MCP_ADAPTER_CERTIFICATES
    return 0
  fi
  export MCP_ADAPTER_CERTIFICATES=true
  export MCP_TRUST_DOMAIN="${MCP_TRUST_DOMAIN:-$(staging_adapter_trust_domain)}"
  export MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE="${MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE:-${STAGING_ADAPTER_TLS_NAMESPACE_DEFAULT}}"
}

staging_operator_env() {
  kubectl -n mcp-runtime get deploy mcp-runtime-operator-controller-manager \
    -o jsonpath="{.spec.template.spec.containers[0].env[?(@.name==\"$1\")].value}" 2>/dev/null || true
}

# One MCP JSON-RPC POST through the public MCP ingress; prints the HTTP status.
# CERT_DIR empty sends no client certificate. Pass oauth token as $5, or "-" to
# force no bearer even when STAGING_ADAPTER_OAUTH_TOKEN is set.
staging_adapter_mcp_post() {
  local cert_dir="$1" url="$2" body="$3" out="$4" args=()
  local oauth_token="${5:-${STAGING_ADAPTER_OAUTH_TOKEN:-}}"
  if [[ -n "${cert_dir}" ]]; then
    args+=(--cert "${cert_dir}/client.crt" --key "${cert_dir}/client.key")
  fi
  if [[ -n "${oauth_token}" && "${oauth_token}" != "-" ]]; then
    args+=(-H "Authorization: Bearer ${oauth_token}")
  fi
  curl --silent --show-error --max-time 15 ${args[@]+"${args[@]}"} -o "${out}" --write-out '%{http_code}' \
    -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -H 'Mcp-Protocol-Version: 2025-06-18' --data "${body}" "${url}" || true
}

# Evidence for a failed adapter-enrollment stage. The stage deletes its
# objects on exit, so capture them first. Secret data is never collected.
staging_adapter_diagnostics() {
  local out="$1" ns="$2" tls_ns="$3"
  shift 3
  local server
  mkdir -p "${out}"
  kubectl -n "${ns}" get mcpservers "$@" -o yaml >"${out}/mcpservers.yaml" 2>&1 || true
  kubectl -n "${ns}" get mcpaccessgrants,mcpagentsessions -o yaml >"${out}/grants-sessions.yaml" 2>&1 || true
  kubectl -n "${ns}" get ingressroutes.traefik.io,middlewares.traefik.io,serverstransports.traefik.io,tlsoptions.traefik.io -o yaml \
    >"${out}/traefik-crs.yaml" 2>&1 || true
  kubectl -n "${tls_ns}" get tlsoptions.traefik.io,tlsstores.traefik.io -o yaml >"${out}/traefik-default-tls.yaml" 2>&1 || true
  kubectl -n "${ns}" describe certificates,certificaterequests >"${out}/certificates-describe.txt" 2>&1 || true
  kubectl get certificaterequests -A -o wide >"${out}/certificaterequests.txt" 2>&1 || true
  kubectl -n "${ns}" get secrets >"${out}/secret-names.txt" 2>&1 || true
  kubectl -n "${ns}" get networkpolicies -o yaml >"${out}/networkpolicies.yaml" 2>&1 || true
  for server in "$@"; do
    kubectl -n "${ns}" get configmap "${server}-gateway-policy" -o jsonpath='{.data.policy\.json}' \
      >"${out}/${server}-gateway-policy.json" 2>&1 || true
    kubectl -n "${ns}" describe deploy "${server}" >"${out}/${server}-deploy-describe.txt" 2>&1 || true
    kubectl -n "${ns}" describe pods -l "app=${server}" >"${out}/${server}-pods-describe.txt" 2>&1 || true
    kubectl -n "${ns}" logs "deploy/${server}" --all-containers --prefix --tail=300 >"${out}/${server}.log" 2>&1 || true
  done
  kubectl -n mcp-runtime logs deploy/mcp-runtime-operator-controller-manager --tail=400 >"${out}/operator.log" 2>&1 || true
  kubectl -n mcp-sentinel logs deploy/mcp-runtime-api --tail=400 >"${out}/runtime-api.log" 2>&1 || true
  local traefik_ns
  traefik_ns="$(staging_traefik_namespace 2>/dev/null || true)"
  [[ -z "${traefik_ns}" ]] || kubectl -n "${traefik_ns}" logs deploy/traefik --tail=300 >"${out}/traefik.log" 2>&1 || true
  staging_err "adapter-enrollment evidence saved under ${out#"${STAGING_ARTIFACT_DIR}/"}/"
}

staging_adapter_cleanup() {
  local rc="$1" ns="$2" tls_ns="$3" grant="$4" pull="$5" certs="$6" session_file="$7"
  shift 7
  set +e
  if [[ "${rc}" -ne 0 && "${rc}" -ne "${STAGING_SKIP_RC}" ]]; then
    staging_adapter_diagnostics "${STAGING_ARTIFACT_DIR}/adapter-enrollment" "${ns}" "${tls_ns}" "$@"
  fi
  if [[ -s "${session_file}" ]]; then
    while read -r session; do
      [[ -n "${session}" ]] || continue
      kubectl -n "${ns}" delete mcpagentsession "${session}" --ignore-not-found >/dev/null 2>&1
    done <"${session_file}"
  fi
  kubectl -n "${ns}" delete mcpaccessgrant -l app.kubernetes.io/managed-by=staging-e2e --ignore-not-found >/dev/null 2>&1
  kubectl -n "${ns}" delete mcpserver "$@" --ignore-not-found --wait=false >/dev/null 2>&1
  kubectl -n "${ns}" delete secret "${pull}" --ignore-not-found >/dev/null 2>&1
  rm -rf "${certs}" "${session_file}"
}

# Apply one gateway MCPServer for the adapter stage. AUTH_YAML is either empty
# (OAuth off) or a ready-to-indent auth: block.
staging_adapter_apply_server() {
  local name="$1" image_repo="$2" image_tag="$3" pull="$4" ns="$5" auth_yaml="${6:-}"
  kubectl apply -f - <<EOF
apiVersion: mcpruntime.org/v1alpha1
kind: MCPServer
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app.kubernetes.io/managed-by: staging-e2e
spec:
  image: ${image_repo}
  imageTag: ${image_tag}
  imagePullSecrets: [${pull}]
  replicas: 1
  port: 8088
  ingressPath: /${name}/mcp
  publicPathPrefix: ${name}
  envVars:
    - name: PORT
      value: "8088"
  tools:
    - name: aaa-ping
      requiredTrust: low
      sideEffect: read
    - name: upper
      requiredTrust: low
      sideEffect: read
${auth_yaml}
  policy:
    mode: allow-list
    defaultDecision: deny
    policyVersion: v1
  session:
    required: false
  gateway:
    enabled: true
EOF
}

staging_adapter_apply_grant() {
  local grant="$1" server="$2" ns="$3" team_id="$4" agent="$5" expires_at="$6"
  kubectl apply -f - <<EOF
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: ${grant}
  namespace: ${ns}
  labels:
    app.kubernetes.io/managed-by: staging-e2e
spec:
  serverRef:
    name: ${server}
  subject:
    teamID: ${team_id}
    agentID: ${agent}
  expiresAt: "${expires_at}"
  maxTrust: low
  allowedSideEffects: [read]
  policyVersion: v1
  toolRules:
    - name: aaa-ping
      decision: allow
      requiredTrust: low
EOF
}

staging_adapter_wait_route() {
  local ns="$1" name="$2" tls_ns="$3"
  local deadline=$((SECONDS + 120))
  until kubectl -n "${ns}" get deploy "${name}" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || {
      staging_err "operator never created deploy/${name}; see adapter-enrollment/operator.log"
      return 1
    }
    sleep 3
  done
  kubectl -n "${ns}" rollout status "deploy/${name}" --timeout=300s
  kubectl -n "${ns}" wait --for=condition=Ready "certificate/${name}-gateway-mtls" --timeout=120s
  kubectl -n "${ns}" get "ingressroute.traefik.io/${name}" >/dev/null
  deadline=$((SECONDS + 120))
  until [[ "$(kubectl -n "${tls_ns}" get tlsoption.traefik.io default -o jsonpath='{.spec.clientAuth.clientAuthType}' 2>/dev/null || true)" == "VerifyClientCertIfGiven" ]]; do
    ((SECONDS < deadline)) || {
      staging_err "operator never created the client-auth TLSOption ${tls_ns}/default"
      return 1
    }
    sleep 3
  done
}

# Enroll a session-bound client cert for SERVER/AGENT; writes the credential
# directory path to CERTS/.credential_dir and appends the session id to
# SESSION_FILE. Uses MCP_RUNTIME_CONFIG_DIR so enroll writes under CERTS
# without a removed --output-dir flag. Logs go to stdout for the stage tee.
staging_adapter_enroll() {
  local certs="$1" human_token="$2" server="$3" ns="$4" agent="$5" trust="$6" session_file="$7"
  local out="" attempt credential_dir spiffe session
  mkdir -p "${certs}"
  for attempt in 1 2 3 4 5 6; do
    if out="$(MCP_RUNTIME_CONFIG_DIR="${certs}" MCP_PLATFORM_API_TOKEN="${human_token}" "${BIN}" adapter enroll \
      --platform-url "${PLATFORM_URL}" --server "${server}" --namespace "${ns}" \
      --agent "${agent}" --trust-domain "${trust}" 2>&1)"; then
      break
    fi
    staging_log "enroll attempt ${attempt} for ${server} failed: ${out}"
    out=""
    sleep 10
  done
  [[ -n "${out}" ]] || {
    staging_err "adapter enroll never succeeded for ${server}"
    return 1
  }
  staging_log "$(printf '%s\n' "${out}" | head -1)"
  credential_dir="$(parse_adapter_enroll_credential_dir "${out}")" || {
    staging_err "adapter enroll did not report a usable certificate directory for ${server}"
    return 1
  }
  spiffe="$(printf '%s\n' "${out}" | sed -n 's#.*issued \(spiffe://[^ ]*\).*#\1#p' | head -1)"
  session="${spiffe##*/session/}"
  [[ "${spiffe}" == "spiffe://${trust}/ns/${ns}/session/"* && -n "${session}" && "${session}" != */* ]] || {
    staging_err "adapter enroll returned ${spiffe:-no SPIFFE ID}, want spiffe://${trust}/ns/${ns}/session/<id>"
    return 1
  }
  printf '%s\n' "${session}" >>"${session_file}"
  openssl verify -CAfile "${credential_dir}/ca.crt" "${credential_dir}/client.crt" >/dev/null
  openssl x509 -in "${credential_dir}/client.crt" -noout -ext subjectAltName | grep -qF "URI:${spiffe}" || {
    staging_err "client certificate does not carry the session-bound URI SAN ${spiffe}"
    return 1
  }
  local session_ref
  session_ref="$(kubectl -n "${ns}" get mcpagentsession "${session}" -o jsonpath='{.spec.serverRef.name}/{.spec.subject.agentID}')"
  [[ "${session_ref}" == "${server}/${agent}" ]] || {
    staging_err "MCPAgentSession ${ns}/${session} is for ${session_ref:-<missing>}, want ${server}/${agent}"
    return 1
  }
  local deadline=$((SECONDS + 120))
  until kubectl -n "${ns}" get configmap "${server}-gateway-policy" -o jsonpath='{.data.policy\.json}' 2>/dev/null |
    jq -e --arg s "${session}" '[.sessions[]?.name] | index($s) != null' >/dev/null 2>&1; do
    ((SECONDS < deadline)) || {
      staging_err "session ${session} never reached ${server}-gateway-policy"
      return 1
    }
    sleep 2
  done
  staging_log "gateway policy for ${server} binds session ${session}"
  printf '%s' "${credential_dir}" >"${certs}/.credential_dir"
}

# Exercise the HTTPS cert matrix against URL. MODE is "cert" (OAuth off) or
# "oauth" (bearer required with the cert). WRONG_URL proves session binding.
staging_adapter_assert_https_matrix() {
  local mode="$1" credential_dir="$2" url="$3" wrong_url="$4" label="$5"
  local body="${WORK_DIR}/adapter-mcp-body-${mode}.json" code failed=0
  local init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"staging-e2e","version":"1"}}}'
  local token_arg="-"
  if [[ "${mode}" == "oauth" ]]; then
    token_arg="${STAGING_ADAPTER_OAUTH_TOKEN}"
  fi

  local deadline=$((SECONDS + ${STAGING_ADAPTER_POLICY_WAIT_SECONDS:-240}))
  while true; do
    code="$(staging_adapter_mcp_post "${credential_dir}" "${url}" "${init}" "${body}" "${token_arg}")"
    [[ "${code}" == "200" ]] && break
    if ((SECONDS >= deadline)); then
      staging_err "${label} initialize via ${url}: HTTP ${code}: $(head -c 400 "${body}" 2>/dev/null)"
      return 1
    fi
    sleep 3
  done
  staging_log "${label} initialize via ${url}: HTTP 200"

  code="$(staging_adapter_mcp_post "${credential_dir}" "${url}" \
    '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aaa-ping","arguments":{}}}' "${body}" "${token_arg}")"
  if [[ "${code}" == "200" ]]; then
    staging_log "${label} granted tool aaa-ping: HTTP 200"
  else
    staging_err "${label} granted tool aaa-ping: HTTP ${code}: $(head -c 400 "${body}")"
    failed=1
  fi
  code="$(staging_adapter_mcp_post "${credential_dir}" "${url}" \
    '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"upper","arguments":{"text":"x"}}}' "${body}" "${token_arg}")"
  if [[ "${code}" == "403" ]]; then
    staging_log "${label} ungranted tool upper denied: HTTP 403"
  else
    staging_err "${label} ungranted tool upper: HTTP ${code}, want 403: $(head -c 400 "${body}")"
    failed=1
  fi

  if [[ "${mode}" == "cert" ]]; then
    # OAuth off: initialize may succeed anonymously; tools/call still needs identity.
    code="$(staging_adapter_mcp_post "" "${url}" \
      '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"aaa-ping","arguments":{}}}' "${body}" -)"
    if [[ "${code}" == "401" ]]; then
      staging_log "${label} tools/call without a certificate denied: HTTP 401"
    else
      staging_err "${label} tools/call without a certificate: HTTP ${code}, want 401: $(head -c 400 "${body}")"
      failed=1
    fi
  else
    code="$(staging_adapter_mcp_post "" "${url}" "${init}" "${body}" -)"
    if [[ "${code}" == "401" ]]; then
      staging_log "${label} initialize without certificate or token denied: HTTP 401"
    else
      staging_err "${label} initialize without certificate or token: HTTP ${code}, want 401: $(head -c 400 "${body}")"
      failed=1
    fi
    code="$(staging_adapter_mcp_post "${credential_dir}" "${url}" "${init}" "${body}" -)"
    if [[ "${code}" == "401" ]]; then
      staging_log "${label} initialize with certificate but no bearer denied: HTTP 401"
    else
      staging_err "${label} initialize with certificate but no bearer: HTTP ${code}, want 401: $(head -c 400 "${body}")"
      failed=1
    fi
  fi

  deadline=$((SECONDS + 60))
  while true; do
    code="$(staging_adapter_mcp_post "${credential_dir}" "${wrong_url}" "${init}" "${body}" "${token_arg}")"
    if [[ " 000 404 502 503 " != *" ${code} "* ]] || ((SECONDS >= deadline)); then
      break
    fi
    sleep 3
  done
  if [[ "${mode}" == "cert" ]]; then
    if [[ "${code}" == "401" ]] && grep -q session_not_found "${body}"; then
      staging_log "${label} certificate refused by other server: HTTP 401 session_not_found"
    else
      staging_err "${label} certificate on other server: HTTP ${code}, want 401 session_not_found: $(head -c 400 "${body}")"
      failed=1
    fi
  else
    if [[ "${code}" == "401" ]] && grep -Eq 'invalid_token|session_not_found' "${body}"; then
      staging_log "${label} credentials refused by other server: HTTP 401 ($(jq -r '.error // empty' "${body}" 2>/dev/null))"
    else
      staging_err "${label} credentials on other server: HTTP ${code}, want 401 invalid_token|session_not_found: $(head -c 400 "${body}")"
      failed=1
    fi
  fi
  rm -f "${body}"
  [[ "${failed}" == "0" ]]
}

# Adapter certificate enrollment through the public HTTPS MCP ingress.
# Always exercises the cert-only cell (omit spec.auth). When
# E2E_MCP_OAUTH_ACCESS_TOKEN is set for the managed Globex member and MCP
# resource, also exercises OAuth + certificate on a second pair of servers.
staging_check_adapter_enrollment() {
  if [[ "$(staging_stage_status multitenancy)" != "passed" ]]; then
    staging_skip "adapter enrollment uses the managed Globex team and agents created by the multitenancy stage"
  fi
  staging_mt_names
  staging_mt_read_agents || return 1
  if ! "${BIN}" adapter enroll --help >/dev/null 2>&1; then
    staging_skip "adapter enroll is not supported on this ref"
  fi
  local issuer
  issuer="$(kubectl -n mcp-sentinel get configmap mcp-sentinel-config -o jsonpath='{.data.MCP_MTLS_CLUSTER_ISSUER}' 2>/dev/null || true)"
  if [[ -z "${issuer}" ]]; then
    staging_skip "setup ran without --mtls-cluster-issuer (E2E_MTLS_CLUSTER_ISSUER empty), so adapter certificates are not enabled"
  fi
  if [[ "$(staging_operator_env MCP_ADAPTER_CERTIFICATES)" != "true" ]]; then
    staging_skip "the operator does not have MCP_ADAPTER_CERTIFICATES=true (E2E_ADAPTER_CERTIFICATES off, or a ref without the adapter-certificate platform feature)"
  fi
  kubectl wait --for=condition=Ready "clusterissuer/${issuer}" --timeout=120s
  local operator_issuer trust platform_trust tls_ns
  operator_issuer="$(staging_operator_env MCP_MTLS_CLUSTER_ISSUER)"
  trust="$(staging_operator_env MCP_TRUST_DOMAIN)"
  platform_trust="$(kubectl -n mcp-sentinel get configmap mcp-sentinel-config -o jsonpath='{.data.MCP_TRUST_DOMAIN}' 2>/dev/null || true)"
  tls_ns="$(staging_operator_env MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE)"
  staging_log "workload issuer ${issuer}; trust domain operator=${trust:-<empty>} platform=${platform_trust:-<empty>}; client-auth TLSOption namespace ${tls_ns:-<none>}"
  [[ "${operator_issuer}" == "${issuer}" ]] || {
    staging_err "operator MCP_MTLS_CLUSTER_ISSUER=${operator_issuer:-<empty>}, platform has ${issuer}"
    return 1
  }
  [[ -n "${trust}" && "${trust}" == "${platform_trust}" ]] || {
    staging_err "MCP_TRUST_DOMAIN must be set and equal on the operator (${trust:-<empty>}) and in mcp-sentinel-config (${platform_trust:-<empty>}); setup did not receive it"
    return 1
  }
  [[ -n "${tls_ns}" ]] || {
    staging_err "operator has no MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE; path routes on the shared MCP host would never request a client certificate"
    return 1
  }
  local image
  image="$(kubectl -n mcp-sentinel get configmap mcp-sentinel-config -o jsonpath='{.data.MCP_DOCTOR_SMOKE_IMAGE}' 2>/dev/null || true)"
  [[ -n "${image}" ]] || {
    staging_err "mcp-sentinel-config has no MCP_DOCTOR_SMOKE_IMAGE to run as the upstream server"
    return 1
  }
  local image_repo="${image}" image_tag=latest
  if [[ "${image##*/}" == *:* ]]; then
    image_repo="${image%:*}"
    image_tag="${image##*:}"
  fi

  local ns=mcp-servers
  local server=staging-e2e-adapter wrong=staging-e2e-adapter-other
  local oauth_server=staging-e2e-adapter-oauth oauth_wrong=staging-e2e-adapter-oauth-other
  local agent="${MT_GLOBEX_AGENT_ID}" team_id="${MT_GLOBEX_TEAM_ID}"
  local grant=staging-e2e-adapter-grant oauth_grant=staging-e2e-adapter-oauth-grant
  local pull=staging-e2e-adapter-pull
  local grant_expires_at
  grant_expires_at="$(python3 -c 'from datetime import datetime,timedelta,timezone; print((datetime.now(timezone.utc)+timedelta(minutes=30)).replace(microsecond=0).isoformat().replace("+00:00","Z"))')"
  local certs="${WORK_DIR}/adapter-certs" session_file="${WORK_DIR}/adapter-session"
  local oauth_issuer="${E2E_MCP_AUTH_ISSUER_URL:-${AUTH_URL}}"
  unset STAGING_ADAPTER_OAUTH_TOKEN
  if [[ -n "${E2E_MCP_OAUTH_ACCESS_TOKEN:-}" ]]; then
    STAGING_ADAPTER_OAUTH_TOKEN="${E2E_MCP_OAUTH_ACCESS_TOKEN}"
    staging_log "OAuth + certificate cell enabled (E2E_MCP_OAUTH_ACCESS_TOKEN set)"
  else
    staging_log "OAuth + certificate cell skipped (set E2E_MCP_OAUTH_ACCESS_TOKEN for the managed Globex member and MCP resource to exercise it)"
  fi
  rm -rf "${certs}" "${session_file}"
  : >"${session_file}"
  # shellcheck disable=SC2064 # expand the names now; the trap runs after they go out of scope
  trap "staging_adapter_cleanup \$? '${ns}' '${tls_ns}' '${grant}' '${pull}' '${certs}' '${session_file}' '${server}' '${wrong}' '${oauth_server}' '${oauth_wrong}'" EXIT

  kubectl -n mcp-sentinel get secret mcp-runtime-registry-pull -o json |
    jq --arg name "${pull}" --arg ns "${ns}" \
      '{apiVersion, kind, type, data, metadata: {name: $name, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "staging-e2e"}}}' |
    kubectl apply -f - >/dev/null

  # Cert-only pair (omit spec.auth): HTTPS client-certificate matrix without a bearer.
  staging_adapter_apply_server "${server}" "${image_repo}" "${image_tag}" "${pull}" "${ns}" ""
  staging_adapter_apply_server "${wrong}" "${image_repo}" "${image_tag}" "${pull}" "${ns}" ""
  staging_adapter_apply_grant "${grant}" "${server}" "${ns}" "${team_id}" "${agent}" "${grant_expires_at}"

  local name
  for name in "${server}" "${wrong}"; do
    staging_adapter_wait_route "${ns}" "${name}" "${tls_ns}" || return 1
  done
  staging_log "Cert-only routes are on Traefik IngressRoutes with the ${tls_ns}/default client-auth TLSOption"

  local human_token
  human_token="$(jq -r --arg p "${MT_GLOBEX_PROFILE}" '.accounts[$p].token // empty' "${MT_CONFIG}")"
  [[ -n "${human_token}" ]] || {
    staging_err "no saved member token for ${MT_GLOBEX_PROFILE}"
    return 1
  }

  local credential_dir
  staging_adapter_enroll "${certs}" "${human_token}" "${server}" "${ns}" "${agent}" "${trust}" "${session_file}" || return 1
  credential_dir="$(cat "${certs}/.credential_dir")"
  staging_adapter_assert_https_matrix "cert" "${credential_dir}" \
    "${MCP_URL}/${server}/mcp" "${MCP_URL}/${wrong}/mcp" "cert-only" || return 1

  # Optional OAuth + cert cell when a resource token is supplied.
  if [[ -n "${STAGING_ADAPTER_OAUTH_TOKEN:-}" ]]; then
    local auth_yaml
    auth_yaml="$(printf '  auth:\n    issuerURL: %s\n    audience: %s/%s/mcp\n' "${oauth_issuer}" "${MCP_URL}" "${oauth_server}")"
    # wrong server needs its own audience
    local auth_yaml_wrong
    auth_yaml_wrong="$(printf '  auth:\n    issuerURL: %s\n    audience: %s/%s/mcp\n' "${oauth_issuer}" "${MCP_URL}" "${oauth_wrong}")"
    staging_adapter_apply_server "${oauth_server}" "${image_repo}" "${image_tag}" "${pull}" "${ns}" "${auth_yaml}"
    staging_adapter_apply_server "${oauth_wrong}" "${image_repo}" "${image_tag}" "${pull}" "${ns}" "${auth_yaml_wrong}"
    staging_adapter_apply_grant "${oauth_grant}" "${oauth_server}" "${ns}" "${team_id}" "${agent}" "${grant_expires_at}"
    for name in "${oauth_server}" "${oauth_wrong}"; do
      staging_adapter_wait_route "${ns}" "${name}" "${tls_ns}" || return 1
    done
    local oauth_cred
    staging_adapter_enroll "${certs}/oauth" "${human_token}" "${oauth_server}" "${ns}" "${agent}" "${trust}" "${session_file}" || return 1
    oauth_cred="$(cat "${certs}/oauth/.credential_dir")"
    staging_adapter_assert_https_matrix "oauth" "${oauth_cred}" \
      "${MCP_URL}/${oauth_server}/mcp" "${MCP_URL}/${oauth_wrong}/mcp" "oauth+cert" || return 1
  fi

  # Deny path: an agent without a grant is refused a session.
  local code
  code="$(staging_http_code -X POST -H "authorization: Bearer ${human_token}" \
    -H 'content-type: application/json' \
    --data "{\"serverName\":\"${server}\",\"namespace\":\"${ns}\",\"agentID\":\"${MT_GLOBEX_DENIED_AGENT_ID}\"}" \
    "${PLATFORM_URL}/api/v1/runtime/adapter/sessions")"
  if [[ "${code}" == "403" ]]; then
    staging_log "ungranted agent refused an adapter session: HTTP 403"
  else
    staging_err "adapter session for an ungranted agent returned HTTP ${code}, want 403"
    return 1
  fi
}

# Names the multitenancy suite derives from its RUN_ID.
staging_mt_names() {
  MT_RUN_ID="${STAGING_MT_RUN_ID}"
  MT_ACME_NS="mcp-team-acme-${MT_RUN_ID}"
  MT_ACME_SERVER="acme-tools-${MT_RUN_ID}"
  MT_GLOBEX_PROFILE="globex-user-${MT_RUN_ID}"
  MT_CONFIG="${STAGING_MT_CONFIG_DIR}/config.json"
  MT_AGENTS_JSON="${WORK_DIR}/multitenancy/managed-agents.json"
}

staging_mt_read_agents() {
  [[ -s "${MT_AGENTS_JSON}" ]] || {
    staging_err "managed-agent identities were not saved by the multitenancy stage (${MT_AGENTS_JSON})"
    return 1
  }
  MT_GLOBEX_TEAM_ID="$(jq -er '.globex.teamID' "${MT_AGENTS_JSON}")"
  MT_GLOBEX_AGENT_ID="$(jq -er '.globex.agentID' "${MT_AGENTS_JSON}")"
  MT_GLOBEX_DENIED_AGENT_ID="$(jq -er '.globex.deniedAgentID' "${MT_AGENTS_JSON}")"
}

staging_check_multitenancy() {
  if ! staging_flag_enabled "${E2E_RUN_MULTITENANCY:-1}"; then
    staging_skip "run-multitenancy is false"
  fi
  mkdir -p "${STAGING_MT_CONFIG_DIR}"
  PLATFORM_URL="${PLATFORM_URL}" MCP_URL="${MCP_URL}" REGISTRY_HOST="${REGISTRY_HOST}" \
    ADMIN_TOKEN_INPUT="${E2E_PLATFORM_API_TOKEN}" RUN_ID="${STAGING_MT_RUN_ID}" \
    MCP_RUNTIME_CONFIG_DIR="${STAGING_MT_CONFIG_DIR}" VERIFY_DEPLOY_PULL_SECRET=1 \
    BIN="${BIN}" WORK_DIR="${WORK_DIR}/multitenancy" \
    bash "${ROOT_DIR}/hack/deploy/mcpruntime-org/multitenancy-test.sh" | tee "${STAGING_ARTIFACT_DIR}/multitenancy.log"
  # Evidence that the tenant server went through build -> push -> deploy and is
  # pulled in-cluster from the public registry.
  staging_mt_names
  local image
  image="$(kubectl -n "${MT_ACME_NS}" get mcpserver "${MT_ACME_SERVER}" -o jsonpath='{.spec.image}:{.spec.imageTag}')"
  staging_log "tenant server image ${image}"
  # The tenant flow runs with only the saved auth profile (no MCP_* registry
  # env), like a quickstart user. The node resolves names through public DNS,
  # so an image on the in-cluster registry Service name can never be pulled.
  if [[ "${image}" == registry.registry.svc* ]]; then
    staging_err "tenant server ${MT_ACME_SERVER} was deployed with the in-cluster registry host (${image}); the CLI ignored the saved profile registry"
    return 1
  fi
  kubectl -n "${MT_ACME_NS}" rollout status "deploy/${MT_ACME_SERVER}" --timeout=120s
}

staging_check_governance() {
  if [[ "$(staging_stage_status multitenancy)" != "passed" ]]; then
    staging_skip "needs the multitenancy stage's tenants, grants and sessions"
  fi
  staging_mt_names
  staging_mt_read_agents || return 1
  local token code failed=0
  token="$(jq -r --arg p "${MT_GLOBEX_PROFILE}" '.accounts[$p].token // empty' "${MT_CONFIG}")"
  [[ -n "${token}" ]] || {
    staging_err "no saved token for ${MT_GLOBEX_PROFILE}"
    return 1
  }
  # Allow path (granted agent) is exercised by the multitenancy adapter calls;
  # re-assert it at the session API, then the deny paths.
  code="$(staging_http_code -X POST -H "authorization: Bearer ${token}" -H "x-api-key: ${token}" \
    -H 'content-type: application/json' \
    --data "{\"serverName\":\"${MT_ACME_SERVER}\",\"namespace\":\"${MT_ACME_NS}\",\"agentID\":\"${MT_GLOBEX_AGENT_ID}\"}" \
    "${PLATFORM_URL}/api/v1/runtime/adapter/sessions")"
  if [[ "${code}" == "200" || "${code}" == "201" ]]; then
    staging_log "granted agent session: HTTP ${code}"
  else
    staging_err "granted agent session returned HTTP ${code}"
    failed=1
  fi
  code="$(staging_http_code -X POST -H "authorization: Bearer ${token}" -H "x-api-key: ${token}" \
    -H 'content-type: application/json' \
    --data "{\"serverName\":\"${MT_ACME_SERVER}\",\"namespace\":\"${MT_ACME_NS}\",\"agentID\":\"${MT_GLOBEX_DENIED_AGENT_ID}\"}" \
    "${PLATFORM_URL}/api/v1/runtime/adapter/sessions")"
  if [[ "${code}" == "403" ]]; then
    staging_log "ungranted agent session denied: HTTP 403"
  else
    staging_err "ungranted agent session returned HTTP ${code}, want 403"
    failed=1
  fi
  grep -E '^=== .*(OK|denied)' "${STAGING_ARTIFACT_DIR}/multitenancy.log" || true
  [[ "${failed}" == "0" ]]
}

staging_check_analytics() {
  local dir="${STAGING_ARTIFACT_DIR}" count
  staging_api_curl --fail "${PLATFORM_URL}/api/v1/stats" >"${dir}/analytics-stats.json"
  staging_log "analytics stats: $(jq -c '.' "${dir}/analytics-stats.json" | cut -c1-300)"
  staging_api_curl --fail "${PLATFORM_URL}/api/v1/events?limit=100" >"${dir}/analytics-events.json"
  count="$(jq '(.events // .data // .) | if type == "array" then length else 0 end' "${dir}/analytics-events.json")"
  staging_log "analytics API returned ${count} event(s)"
  local ch
  ch="$(kubectl -n mcp-sentinel exec statefulset/clickhouse -- clickhouse-client \
    --query "SELECT count() FROM mcp.events" 2>/dev/null | tr -d '[:space:]' || true)"
  staging_log "ClickHouse mcp.events rows: ${ch:-unavailable}"
  if [[ "$(staging_stage_status multitenancy)" != "passed" ]]; then
    staging_log "no tenant traffic this run; analytics reachability checked only"
    return 0
  fi
  staging_mt_names
  [[ "${count}" -gt 0 ]] || {
    staging_err "analytics API has no events after tenant traffic"
    return 1
  }
  local server_rows
  server_rows="$(kubectl -n mcp-sentinel exec statefulset/clickhouse -- clickhouse-client \
    --query "SELECT count() FROM mcp.events WHERE server = '${MT_ACME_SERVER}'" | tr -d '[:space:]')"
  staging_log "ClickHouse rows for ${MT_ACME_SERVER}: ${server_rows}"
  [[ "${server_rows}" =~ ^[0-9]+$ && "${server_rows}" -gt 0 ]] || {
    staging_err "no ClickHouse events for the tenant server ${MT_ACME_SERVER}"
    return 1
  }
}

# The stages both runners share once setup has run.
staging_run_platform_stages() {
  STAGING_MT_RUN_ID="${STAGING_MT_RUN_ID:-mt$(printf '%s' "${RUN_ID}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | tail -c 10)}"
  STAGING_MT_CONFIG_DIR="${STAGING_MT_CONFIG_DIR:-${WORK_DIR}/mcpruntime-config}"
  export STAGING_MT_RUN_ID STAGING_MT_CONFIG_DIR
  staging_run_stage diagnostics soft "setup finished but cluster diagnostics/doctor report unmet checks; read the failing check names" staging_check_diagnostics
  staging_run_stage rollouts soft "a platform deployment is not Ready or a pod is stuck in image pull/crash loop; see diagnostics/<ns>-pods-describe.txt" staging_check_rollouts
  staging_run_stage cluster-issuer soft "the ACME ClusterIssuer is not Ready (account registration failed or wrong directory); see diagnostics/clusterissuers.yaml" staging_check_cluster_issuer
  staging_run_stage certificates soft "a public Certificate is not Ready, uses the wrong issuer, or its SANs do not cover the host; see diagnostics/certificates-describe.txt" staging_check_certificates
  staging_run_stage tls-endpoints soft "the host serves an untrusted/mismatched certificate (Traefik default cert, stale snapshot, or missing staging root)" staging_check_tls_endpoints
  staging_run_stage fresh-certificate soft "HTTP-01 for the unique host failed (DNS, port 80 routing, or Let's Encrypt staging limits); see the stage log's order/challenge describe" staging_check_fresh_certificate
  staging_run_stage platform-login critical "the platform API is unreachable or rejected the admin key; check mcp-platform-api logs in diagnostics/" staging_check_platform_login
  staging_run_stage platform-api soft "an admin API call failed or an anonymous/bad-key request was not denied" staging_check_platform_api
  staging_run_stage registry-route soft "https://registry.<domain>/v2/ is not 401: the registry Ingress rule host no longer matches its TLS host (Traefik 404); run mcp-runtime cluster doctor" staging_check_registry_route
  staging_run_stage registry-auth soft "registry forward-auth is not enforcing (anonymous allowed) or rejects valid credentials; see traefik and mcp-platform-api logs" staging_check_registry_auth
  staging_run_stage image-pulls soft "a workload lacks mcp-runtime-registry-pull, or the node cannot pull from the public registry (x509/auth)" staging_check_image_pulls
  staging_run_stage ui soft "the platform UI ingress or mcp-sentinel-ui is down" staging_check_ui
  staging_run_stage oidc soft "mcp-auth discovery/JWKS or the Keycloak issuer is unreachable" staging_check_oidc
  staging_run_stage multitenancy soft "a tenant build/push/deploy, grant, adapter call, or event check failed; read multitenancy.log from the bottom" staging_check_multitenancy
  staging_run_stage adapter-enrollment soft "adapter HTTPS certificate enrollment failed: setup did not pass MCP_ADAPTER_CERTIFICATES/MCP_TRUST_DOMAIN/MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE to the operator, the cert-only IngressRoute/TLSOption/gateway certificate never converged (adapter-enrollment/operator.log, traefik-crs.yaml, certificates-describe.txt), the CertificateRequest was not signed or the session not owned (adapter-enrollment/runtime-api.log), the gateway rejected the SPIFFE identity, or (when E2E_MCP_OAUTH_ACCESS_TOKEN is set) the OAuth+cert cell failed" staging_check_adapter_enrollment
  staging_run_stage governance soft "a grant/session deny path allowed traffic, or a granted agent was refused" staging_check_governance
  staging_run_stage analytics soft "events did not reach the analytics API/ClickHouse (ingest -> kafka -> processor path)" staging_check_analytics
  staging_run_stage registry-route-after-user-flows soft "a push/deploy/reconcile stage rewrote the registry Ingress rule host (Traefik 404 on the registry host); compare registry Ingress hosts in both registry-route stage logs" staging_check_registry_route
}
