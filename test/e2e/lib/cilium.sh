#!/usr/bin/env bash
# Explicit security-test provisioning only, sourced by fresh Kind QA.
# Official Cilium 1.20.3 chart/Kind guidance reviewed 2026-10-17:
# https://docs.cilium.io/en/stable/installation/kind/
# https://github.com/cilium/cilium/tree/v1.20.3/install/kubernetes/cilium

scoped_cilium_guard() {
  [[ "${CLUSTER_NAME}" =~ ^mcp-scope-e2e-[a-z0-9-]+$ ]] || {
    echo "Cilium security QA requires its own mcp-scope-e2e-* cluster" >&2; return 1;
  }
  [[ "${E2E_CACHE_MODE}" == "0" ]] || {
    echo "Cilium security QA never reuses an existing cluster" >&2; return 1;
  }
  [[ "${LOCAL_REGISTRY_NAME}" == "${CLUSTER_NAME}-mirror" || "${LOCAL_REGISTRY_NAME}" == "${CLUSTER_NAME}-dockerhub-mirror" ]] || {
    echo "Cilium security QA requires its own cluster-named registry" >&2; return 1;
  }
  command -v helm >/dev/null || { echo "Cilium security QA requires Helm (see docs/contributor/local-kind.md)" >&2; return 1; }
  local clusters
  clusters="$(kind get clusters)" || { echo "Cannot verify the new cluster name is unused" >&2; return 1; }
  if [[ $'\n'"${clusters}"$'\n' == *$'\n'"${CLUSTER_NAME}"$'\n'* ]]; then
    echo "Refusing to mutate an existing Cilium QA cluster" >&2; return 1
  fi
  local containers
  containers="$(docker ps -a --format '{{.Names}}')" || { echo "Cannot verify the new registry name is unused" >&2; return 1; }
  if [[ $'\n'"${containers}"$'\n' == *$'\n'"${LOCAL_REGISTRY_NAME}"$'\n'* ]]; then
    echo "Refusing to mutate an existing Cilium QA registry" >&2; return 1
  fi
}

install_scoped_cilium() {
  # No ambient kubeconfig/context is used, including by Helm.
  kind get kubeconfig --name "${CLUSTER_NAME}" > "${KUBECONFIG_FILE}"
  chmod 600 "${KUBECONFIG_FILE}"
  local context
  context="$(kubectl --kubeconfig "${KUBECONFIG_FILE}" config current-context)"
  [[ "${context}" == "kind-${CLUSTER_NAME}" ]] || { echo "Cilium target context differs" >&2; return 1; }
  kubectl --kubeconfig "${KUBECONFIG_FILE}" get nodes -o json | SCOPED_KIND_CLUSTER="${CLUSTER_NAME}" python3 -c '
import json,os,sys
nodes=json.load(sys.stdin)["items"]
expected="kind://docker/"+os.environ["SCOPED_KIND_CLUSTER"]+"/"
assert nodes and all(n["spec"].get("providerID")==expected+n["metadata"]["name"] for n in nodes), "Cilium target is not the new named Docker Kind cluster"
'
  helm upgrade --install cilium cilium --repo https://helm.cilium.io --version 1.20.3 \
    --kubeconfig "${KUBECONFIG_FILE}" --kube-context "${context}" --namespace kube-system \
    --values "${PROJECT_ROOT}/test/e2e/fixtures/cilium-values.yaml" --wait --timeout 5m
  kubectl --kubeconfig "${KUBECONFIG_FILE}" rollout status daemonset/cilium -n kube-system --timeout=180s
  kubectl --kubeconfig "${KUBECONFIG_FILE}" wait --for=condition=Ready nodes --all --timeout=180s
}
