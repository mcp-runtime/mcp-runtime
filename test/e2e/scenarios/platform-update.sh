# `mcp-runtime update` checks for QA E2E. Sourced from qa-e2e.sh when the
# platform-update scenario is selected. Requires a test-mode setup with the
# Platform stack installed.
#
# Proves:
#   1. a release manifest matching the running images is an all-unchanged plan
#      and `update --yes` performs no rollout (no Deployment generation change);
#   2. update refuses to apply without --yes when stdin is not a terminal;
#   3. pinning one service (ui) to its running digest rolls out only that
#      Deployment;
#   4. an immediate rerun of the same manifest is a no-op;
#   5. a release that declares a ServiceAccount, a ConfigMap and Deployment
#      wiring (env + resolver option) applies all of it in one pass, rolls only
#      the workloads that changed (a ConfigMap rolls its dependent), is a no-op
#      on rerun, and a later ConfigMap change rolls only that dependent;
#   6. --expect-cluster-id refuses a different cluster and accepts the real one.

PLATFORM_UPDATE_DEPLOYMENTS=(
  "mcp-runtime/mcp-runtime-operator-controller-manager"
  "mcp-platform/mcp-platform-api"
  "mcp-platform/mcp-runtime-api"
  "mcp-observability/mcp-analytics-api"
  "mcp-observability/mcp-ingest"
  "mcp-observability/mcp-processor"
  "mcp-platform/mcp-ui"
)

platform_update_generations() {
  local item ns name
  for item in "${PLATFORM_UPDATE_DEPLOYMENTS[@]}"; do
    ns="${item%%/*}"
    name="${item#*/}"
    printf '%s=%s\n' "${item}" "$(kubectl get deployment "${name}" -n "${ns}" -o jsonpath='{.metadata.generation}')"
  done
}

# platform_update_component_json <component> <image> [digest] prints one
# manifest component entry, splitting image into repository and tag.
platform_update_component_json() {
  local component="$1" image="$2" digest="${3:-}"
  image="${image%@*}"
  local repository="${image%:*}" tag="${image##*:}"
  if [[ "${repository}" == "${image}" || "${tag}" == */* ]]; then
    repository="${image}"
    tag="latest"
  fi
  jq -n --arg n "${component}" --arg r "${repository}" --arg t "${tag}" --arg d "${digest}" \
    '{name: $n, repository: $r, tag: $t} + (if $d == "" then {} else {digest: $d} end)'
}

# platform_update_write_manifest <path> [ui-digest] writes a manifest matching
# the images currently running in the cluster.
platform_update_write_manifest() {
  local path="$1" ui_digest="${2:-}"
  local operator_image gateway_image
  operator_image="$(kubectl get deployment mcp-runtime-operator-controller-manager -n mcp-runtime -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}')"
  gateway_image="$(kubectl get deployment mcp-runtime-operator-controller-manager -n mcp-runtime -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].env[?(@.name=="MCP_GATEWAY_PROXY_IMAGE")].value}')"
  {
    platform_update_component_json operator "${operator_image}"
    if [[ -n "${gateway_image}" ]]; then
      platform_update_component_json gateway-proxy "${gateway_image}"
    fi
    local pair component deploy container image digest
    for pair in platform-api:mcp-platform-api:platform-api runtime-api:mcp-runtime-api:runtime-api \
      analytics-api:mcp-analytics-api:analytics-api ingest:mcp-ingest:ingest \
      processor:mcp-processor:processor ui:mcp-ui:ui; do
      IFS=: read -r component deploy container <<<"${pair}"
      ns=mcp-platform
      case "${deploy}" in
        mcp-analytics-api|mcp-ingest|mcp-processor) ns=mcp-observability ;;
      esac
      image="$(kubectl get deployment "${deploy}" -n "${ns}" -o jsonpath="{.spec.template.spec.containers[?(@.name==\"${container}\")].image}")"
      digest=""
      if [[ "${component}" == "ui" ]]; then
        digest="${ui_digest}"
      fi
      platform_update_component_json "${component}" "${image}" "${digest}"
    done
  } | jq -s '{apiVersion: "mcpruntime.org/v1alpha1", kind: "PlatformRelease", version: "v0.0.0-e2e", components: .}' >"${path}"
}

# platform_update_remove_dns_option <namespace> <deployment> <option>: removes one
# pod resolver option (and dnsConfig itself if it was the only one).
platform_update_remove_dns_option() {
  local namespace="$1" deployment="$2" option="$3" index count patch_path
  index="$(kubectl get deployment "${deployment}" -n "${namespace}" -o json | jq --arg o "${option}" '(.spec.template.spec.dnsConfig.options // []) | map(.name) | index($o) // empty')"
  [[ -n "${index}" ]] || return 0
  count="$(kubectl get deployment "${deployment}" -n "${namespace}" -o json | jq '(.spec.template.spec.dnsConfig.options // []) | length')"
  if [[ "${count}" == "1" ]]; then
    patch_path="/spec/template/spec/dnsConfig"
  else
    patch_path="/spec/template/spec/dnsConfig/options/${index}"
  fi
  kubectl patch deployment "${deployment}" -n "${namespace}" --type=json -p "[{\"op\":\"remove\",\"path\":\"${patch_path}\"}]" >/dev/null
}

platform_update_cli() {
  ./bin/mcp-runtime update --kubeconfig "${KUBECONFIG_FILE}" "$@"
}

# platform_update_assert_generations <before> [allowed-change...]: every
# Deployment's generation must be unchanged, except the allowed ones, which must
# have changed. Allowed changes are space-separated in the second argument.
platform_update_assert_generations() {
  local before="$1" allowed_changes="${2:-}"
  local after line item allowed is_allowed
  after="$(platform_update_generations)"
  while IFS= read -r line; do
    item="${line%%=*}"
    is_allowed=0
    for allowed in ${allowed_changes}; do
      [[ "${item}" == "${allowed}" ]] && is_allowed=1
    done
    if [[ "${is_allowed}" == 1 ]]; then
      if grep -Fxq -- "${line}" <<<"${after}"; then
        echo "[platform-update] expected ${item} generation to change" >&2
        return 1
      fi
      continue
    fi
    if ! grep -Fxq -- "${line}" <<<"${after}"; then
      echo "[platform-update] unexpected rollout of ${item}: before=${line} after=$(grep -F "${item}=" <<<"${after}")" >&2
      return 1
    fi
  done <<<"${before}"
}

run_e2e_platform_update_scenario() {
  local dir="${WORKDIR}/platform-update"
  mkdir -p "${dir}"
  refresh_kind_kubeconfig

  local original_ui_image
  original_ui_image="$(kubectl get deployment mcp-ui -n mcp-platform -o jsonpath='{.spec.template.spec.containers[?(@.name=="ui")].image}')"

  echo "[platform-update] up-to-date manifest produces an all-unchanged plan and no rollout"
  platform_update_write_manifest "${dir}/current.json"
  local before
  before="$(platform_update_generations)"
  platform_update_cli --release-manifest "${dir}/current.json" --dry-run --output json >"${dir}/plan-current.json"
  if jq -e '[.plan.components[] | select(.action == "update" or .action == "blocked")] | length > 0' "${dir}/plan-current.json" >/dev/null; then
    echo "[platform-update] expected no changed components for the current manifest" >&2
    cat "${dir}/plan-current.json" >&2
    return 1
  fi
  jq -e '.plan.cluster.clusterID != "" and .plan.cluster.context != ""' "${dir}/plan-current.json" >/dev/null
  platform_update_cli --release-manifest "${dir}/current.json" --yes | tee "${dir}/apply-current.txt"
  grep -Fq "Platform is up to date" "${dir}/apply-current.txt"
  platform_update_assert_generations "${before}"

  echo "[platform-update] refuses to apply without --yes on a non-interactive stdin"
  jq '(.components[] | select(.name == "ui") | .tag) = "e2e-refused"' "${dir}/current.json" >"${dir}/refused.json"
  if platform_update_cli --release-manifest "${dir}/refused.json" --only ui </dev/null >"${dir}/refused.txt" 2>&1; then
    echo "[platform-update] expected update without --yes to be refused" >&2
    cat "${dir}/refused.txt" >&2
    return 1
  fi
  grep -Fq -- "--yes" "${dir}/refused.txt"
  platform_update_assert_generations "${before}"

  echo "[platform-update] pinning ui to its running digest rolls out only ui"
  local ui_digest
  ui_digest="$(kubectl get pods -n mcp-platform -l app=mcp-ui -o jsonpath='{range .items[*]}{range .status.containerStatuses[?(@.name=="ui")]}{.imageID}{"\n"}{end}{end}' | grep -o 'sha256:[a-f0-9]\{64\}' | sort -u)"
  if [[ -z "${ui_digest}" || "$(wc -l <<<"${ui_digest}" | tr -d ' ')" != "1" ]]; then
    echo "[platform-update] could not resolve a single running ui digest: '${ui_digest}'" >&2
    return 1
  fi
  platform_update_write_manifest "${dir}/pinned.json" "${ui_digest}"
  platform_update_cli --release-manifest "${dir}/pinned.json" --dry-run --output json >"${dir}/plan-pinned.json"
  if [[ "$(jq -r '[.plan.components[] | select(.action == "update") | .component] | join(",")' "${dir}/plan-pinned.json")" != "ui" ]]; then
    echo "[platform-update] expected only ui in the pinned plan" >&2
    cat "${dir}/plan-pinned.json" >&2
    return 1
  fi
  platform_update_cli --release-manifest "${dir}/pinned.json" --yes --timeout 300s | tee "${dir}/apply-pinned.txt"
  platform_update_assert_generations "${before}" "mcp-platform/mcp-ui"
  kubectl get deployment mcp-ui -n mcp-platform -o jsonpath='{.spec.template.spec.containers[?(@.name=="ui")].image}' | grep -Fq "@${ui_digest}"
  kubectl get deployment mcp-ui -n mcp-platform -o jsonpath='{.metadata.annotations.mcpruntime\.org/previous-image\.ui}' | grep -Fxq "${original_ui_image}"

  echo "[platform-update] rerun of the same manifest is a no-op"
  local after_pin
  after_pin="$(platform_update_generations)"
  platform_update_cli --release-manifest "${dir}/pinned.json" --yes | tee "${dir}/apply-rerun.txt"
  grep -Fq "Platform is up to date" "${dir}/apply-rerun.txt"
  platform_update_assert_generations "${after_pin}"

  echo "[platform-update] --expect-cluster-id refuses another cluster and accepts the real one"
  local cluster_id
  cluster_id="$(jq -r '.plan.cluster.clusterID' "${dir}/plan-current.json")"
  if platform_update_cli --release-manifest "${dir}/current.json" --dry-run --expect-cluster-id "not-this-cluster" >"${dir}/wrong-cluster.txt" 2>&1; then
    echo "[platform-update] expected a mismatched --expect-cluster-id to be refused" >&2
    cat "${dir}/wrong-cluster.txt" >&2
    return 1
  fi
  grep -Fq "${cluster_id}" "${dir}/wrong-cluster.txt"
  platform_update_cli --release-manifest "${dir}/current.json" --dry-run --expect-cluster-id "${cluster_id}" >/dev/null
  # ui was legitimately rolled by the digest-pin step above; nothing else may change.
  platform_update_assert_generations "${after_pin}"

  echo "[platform-update] declared ServiceAccount, ConfigMap and wiring are applied and roll only what changed"
  local config_before
  config_before="$(platform_update_generations)"
  jq '
    .platformObjects = "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: mcp-update-e2e\n  namespace: mcp-observability\n"
    | .configMaps = [{namespace: "mcp-observability", name: "mcp-update-e2e", data: {"probe.txt": "v1"}, restart: ["processor"]}]
    | (.components[] | select(.name == "ui") | .wiring) = {env: {MCP_UPDATE_E2E: "1"}, dnsOptions: [{name: "attempts", value: "2"}]}
  ' "${dir}/current.json" >"${dir}/declared.json"
  platform_update_cli --release-manifest "${dir}/declared.json" --dry-run --output json >"${dir}/plan-declared.json"
  jq -e '[.plan.platformObjects[] | select(.action == "create")] | length == 1' "${dir}/plan-declared.json" >/dev/null
  jq -e '[.plan.configMaps[] | select(.action == "create")] | length == 1' "${dir}/plan-declared.json" >/dev/null
  jq -e '[.plan.components[] | select(.component == "ui" and .action == "update")] | length == 1' "${dir}/plan-declared.json" >/dev/null
  jq -e '[.plan.components[] | select(.component == "processor" and .action == "update")] | length == 1' "${dir}/plan-declared.json" >/dev/null
  platform_update_assert_generations "${config_before}"
  platform_update_cli --release-manifest "${dir}/declared.json" --yes --timeout 300s | tee "${dir}/apply-declared.txt"
  kubectl get serviceaccount mcp-update-e2e -n mcp-observability >/dev/null
  [[ "$(kubectl get configmap mcp-update-e2e -n mcp-observability -o jsonpath='{.data.probe\.txt}')" == "v1" ]]
  [[ "$(kubectl get deployment mcp-ui -n mcp-platform -o jsonpath='{.spec.template.spec.containers[?(@.name=="ui")].env[?(@.name=="MCP_UPDATE_E2E")].value}')" == "1" ]]
  [[ "$(kubectl get deployment mcp-ui -n mcp-platform -o jsonpath='{.spec.template.spec.dnsConfig.options[?(@.name=="attempts")].value}')" == "2" ]]
  [[ -n "$(kubectl get deployment mcp-processor -n mcp-observability -o jsonpath='{.spec.template.metadata.annotations.mcpruntime\.org/config-hash\.mcp-update-e2e}')" ]]
  platform_update_assert_generations "${config_before}" "mcp-platform/mcp-ui mcp-observability/mcp-processor"

  echo "[platform-update] rerun of the declared release is a no-op"
  local config_after
  config_after="$(platform_update_generations)"
  platform_update_cli --release-manifest "${dir}/declared.json" --yes | tee "${dir}/apply-declared-rerun.txt"
  grep -Fq "Platform is up to date" "${dir}/apply-declared-rerun.txt"
  platform_update_assert_generations "${config_after}"

  echo "[platform-update] a ConfigMap change rolls only the workload that reads it"
  jq '(.configMaps[0].data["probe.txt"]) = "v2"' "${dir}/declared.json" >"${dir}/declared-v2.json"
  platform_update_cli --release-manifest "${dir}/declared-v2.json" --yes --timeout 300s | tee "${dir}/apply-declared-v2.txt"
  [[ "$(kubectl get configmap mcp-update-e2e -n mcp-observability -o jsonpath='{.data.probe\.txt}')" == "v2" ]]
  platform_update_assert_generations "${config_after}" "mcp-observability/mcp-processor"

  echo "[platform-update] removing the declared objects and wiring"
  kubectl delete configmap mcp-update-e2e -n mcp-observability >/dev/null
  kubectl delete serviceaccount mcp-update-e2e -n mcp-observability >/dev/null
  kubectl set env deployment/mcp-ui -n mcp-platform -c ui MCP_UPDATE_E2E- >/dev/null
  # Remove only the probe's resolver option; the platform's own ndots option stays.
  # A JSON patch has no resourceVersion precondition, so it cannot lose a race
  # with the Deployment controller the way get-then-replace does.
  platform_update_remove_dns_option mcp-platform mcp-ui attempts
  kubectl patch deployment mcp-processor -n mcp-observability --type=json \
    -p '[{"op":"remove","path":"/spec/template/metadata/annotations/mcpruntime.org~1config-hash.mcp-update-e2e"}]' >/dev/null 2>&1 || true
  rollout_status_with_logs mcp-observability deploy mcp-processor 300s

  echo "[platform-update] restoring original ui image"
  kubectl set image deployment/mcp-ui -n mcp-platform "ui=${original_ui_image}" >/dev/null
  kubectl annotate deployment/mcp-ui -n mcp-platform mcpruntime.org/previous-image.ui- mcpruntime.org/platform-version- >/dev/null
  rollout_status_with_logs mcp-platform deploy mcp-ui 300s
}
