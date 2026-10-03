#!/usr/bin/env bash

# Live acceptance on the QA-owned Kind cluster. Read only public certificates;
# never fetch CA private keys, and never replace an existing CA or TLS Secret.
certmanager_public_fingerprint() {
  local namespace="$1" secret="$2"
  kubectl get secret "${secret}" -n "${namespace}" -o 'jsonpath={.data.tls\.crt}' \
    | decode_base64 | openssl x509 -noout -fingerprint -sha256
}

run_certmanager_issuance_acceptance() {
  [[ "$(kubectl config current-context)" == "kind-${CLUSTER_NAME}" ]] || {
    echo '[cert-manager] issuance acceptance requires the QA Kind context' >&2; return 1;
  }
  local issuer="${MCP_SETUP_MTLS_CLUSTER_ISSUER:-mcp-runtime-ca}" ca_before ca_after public_before='' public_after=''
  local probe="qa-cert-issuance-${RANDOM}-${RANDOM}" manifest="${WORKDIR}/certmanager-issuance.json"
  kubectl wait --for=condition=Ready "clusterissuer/${issuer}" --timeout=180s
  local ca_secret
  ca_secret="$(kubectl get clusterissuer "${issuer}" -o jsonpath='{.spec.ca.secretName}')"
  [[ -n "${ca_secret}" ]] || { echo '[cert-manager] test requires the workload CA issuer' >&2; return 1; }
  ca_before="$(certmanager_public_fingerprint cert-manager "${ca_secret}")"
  if kubectl get secret mcp-platform-tls -n mcp-platform >/dev/null 2>&1; then
    public_before="$(certmanager_public_fingerprint mcp-platform mcp-platform-tls)"
  fi
  ./bin/mcp-runtime cluster cert apply
  jq -n --arg name "${probe}" --arg issuer "${issuer}" \
    '{apiVersion:"cert-manager.io/v1",kind:"Certificate",metadata:{name:$name,namespace:"mcp-servers"},spec:{secretName:$name,duration:"1h",renewBefore:"10m",dnsNames:["qa-issuance.invalid"],issuerRef:{name:$issuer,kind:"ClusterIssuer"},privateKey:{algorithm:"ECDSA",size:256,rotationPolicy:"Always"},usages:["digital signature","server auth"]}}' >"${manifest}"
  # This is an isolated acceptance fixture, not a platform provisioning path.
  kubectl apply -f "${manifest}" >/dev/null
  if ! kubectl wait --for=condition=Ready "certificate/${probe}" -n mcp-servers --timeout=180s; then
    kubectl delete certificate "${probe}" -n mcp-servers --ignore-not-found >/dev/null
    kubectl delete secret "${probe}" -n mcp-servers --ignore-not-found >/dev/null
    return 1
  fi
  kubectl get secret "${probe}" -n mcp-servers -o 'jsonpath={.data.tls\.crt}' \
    | decode_base64 >"${WORKDIR}/certmanager-probe-public.crt"
  openssl x509 -in "${WORKDIR}/certmanager-probe-public.crt" -noout -checkhost qa-issuance.invalid
  openssl x509 -in "${WORKDIR}/certmanager-probe-public.crt" -noout -checkend 120
  kubectl get secret "${ca_secret}" -n cert-manager -o 'jsonpath={.data.tls\.crt}' \
    | decode_base64 >"${WORKDIR}/certmanager-workload-public.crt"
  openssl verify -CAfile "${WORKDIR}/certmanager-workload-public.crt" "${WORKDIR}/certmanager-probe-public.crt"
  ca_after="$(certmanager_public_fingerprint cert-manager "${ca_secret}")"
  if [[ -n "${public_before}" ]]; then public_after="$(certmanager_public_fingerprint mcp-platform mcp-platform-tls)"; fi
  [[ "${ca_before}" == "${ca_after}" && "${public_before}" == "${public_after}" ]] || {
    echo '[cert-manager] CA or platform TLS fingerprint changed during acceptance' >&2; return 1;
  }
  kubectl delete certificate "${probe}" -n mcp-servers --ignore-not-found >/dev/null
  kubectl delete secret "${probe}" -n mcp-servers --ignore-not-found >/dev/null
  echo '[cert-manager] fresh issuance verified; workload CA and public TLS fingerprints preserved'
}
