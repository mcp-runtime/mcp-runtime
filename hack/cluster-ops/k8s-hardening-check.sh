#!/usr/bin/env bash
# Deterministic Kubernetes posture checks for MCP Runtime namespaces.
# Covers inventory + common manifest hygiene. Judgment-heavy RBAC graph
# review remains in security-audit mode k8s.
#
# Usage:
#   KUBECONFIG=$HOME/.kube/test-mcp-runtime-config bash hack/cluster-ops/k8s-hardening-check.sh
#   NAMESPACES="mcp-runtime,mcp-sentinel" bash hack/cluster-ops/k8s-hardening-check.sh
set -euo pipefail

TEST_KUBECONFIG="${TEST_KUBECONFIG:-${KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}}"
export KUBECONFIG="$TEST_KUBECONFIG"
NAMESPACES="${NAMESPACES:-mcp-runtime,mcp-sentinel,mcp-servers,registry,traefik}"

pass=0
fail=0
warn=0

log() { printf '%s\n' "$*"; }
ok() { log "PASS $*"; pass=$((pass + 1)); }
bad() { log "FAIL $*"; fail=$((fail + 1)); }
note() { log "WARN $*"; warn=$((warn + 1)); }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { log "FAIL missing command: $1"; exit 1; }
}
need_cmd kubectl
need_cmd jq

ctx="$(kubectl config current-context 2>/dev/null || true)"
if [[ -z "$ctx" ]]; then
  log "FAIL no current kube context"
  exit 1
fi
log "context=${ctx}"

IFS=',' read -r -a NS_LIST <<<"$NAMESPACES"

for ns in "${NS_LIST[@]}"; do
  if ! kubectl get ns "$ns" >/dev/null 2>&1; then
    note "namespace ${ns} missing (skip)"
    continue
  fi
  log "=== inventory ${ns} ==="
  kubectl -n "$ns" get deploy,sts,ds,sa,role,rolebinding,networkpolicy -o name 2>/dev/null | sed "s/^/[${ns}] /" || true

  # NetworkPolicy presence for tenant/workload namespaces
  case "$ns" in
    mcp-sentinel|mcp-servers|registry)
      np_count="$(kubectl -n "$ns" get networkpolicy -o json 2>/dev/null | jq '.items | length')"
      if [[ "$np_count" -gt 0 ]]; then
        ok "${ns} has ${np_count} NetworkPolicy object(s)"
      else
        bad "${ns} has no NetworkPolicy"
      fi
      ;;
  esac

  # Pod security / container hygiene on Deployments
  while IFS= read -r dep; do
    [[ -z "$dep" ]] && continue
    json="$(kubectl -n "$ns" get deploy "$dep" -o json)"
    # privileged
    if echo "$json" | jq -e '
      .spec.template.spec.containers[]? | select(.securityContext.privileged == true)
    ' >/dev/null 2>&1; then
      bad "${ns}/deploy/${dep} has privileged=true container"
    else
      ok "${ns}/deploy/${dep} no privileged=true"
    fi
    # runAsNonRoot when set should not be false
    if echo "$json" | jq -e '
      (.spec.template.spec.securityContext.runAsNonRoot == false)
      or (.spec.template.spec.containers[]?.securityContext.runAsNonRoot == false)
    ' >/dev/null 2>&1; then
      bad "${ns}/deploy/${dep} runAsNonRoot=false"
    else
      ok "${ns}/deploy/${dep} runAsNonRoot not false"
    fi
    # hostNetwork / hostPID
    if echo "$json" | jq -e '.spec.template.spec.hostNetwork == true or .spec.template.spec.hostPID == true' >/dev/null 2>&1; then
      bad "${ns}/deploy/${dep} uses hostNetwork or hostPID"
    else
      ok "${ns}/deploy/${dep} no hostNetwork/hostPID"
    fi
  done < <(kubectl -n "$ns" get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
done

# ClusterRoleBindings that look overly broad for mcp/sentinel names
log "=== cluster RBAC name scan ==="
broad="$(kubectl get clusterrolebinding -o json \
  | jq -r '
    .items[]
    | select(.metadata.name | test("mcp|sentinel"; "i"))
    | select(
        (.subjects // [])[]?
        | .name == "system:authenticated" or .name == "system:unauthenticated"
      )
    | .metadata.name
  ' 2>/dev/null || true)"
if [[ -z "$broad" ]]; then
  ok "no mcp/sentinel ClusterRoleBinding to system:authenticated/unauthenticated"
else
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    bad "ClusterRoleBinding ${name} binds system:authenticated or system:unauthenticated"
  done <<<"$broad"
fi

log "=== k8s-hardening-check SUMMARY pass=${pass} fail=${fail} warn=${warn} context=${ctx} ==="
[[ "$fail" -eq 0 ]]
