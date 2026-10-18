# shellcheck shell=bash
# Internal fixture for staging-vm.sh, after its disposable-target guard. Never
# source this as a production provisioning path. Keys stay in the private
# WORK_DIR and Kubernetes Secret, outside uploaded artifacts.

staging_issuer_policies() {
  local issuer="${E2E_MTLS_CLUSTER_ISSUER:-}" domain="${MCP_TRUST_DOMAIN:-}"
  [[ "$issuer" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ && "$issuer" != mcp-runtime-ca ]] || return 1
  [[ "$domain" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || return 1
  # Keep registry/analytics approval separate from the dedicated workload root.
  # The additional policy set has distinct RBAC resource names as well.
  sed "s/mcpruntime\.org/$domain/g" "$ROOT_DIR/config/cert-manager/approval-policies.yaml"
  printf '\n---\n'
  sed -e "s/mcpruntime\.org/$domain/g" \
    -e "s/mcp-runtime-ca/$issuer/g" \
    -e 's/mcp-session-certificates/staging-session-certificates/g' \
    -e 's/mcp-workload-tls/staging-workload-tls/g' \
    -e 's/mcp-public-acme/staging-public-acme/g' \
    -e 's/mcp-use-/staging-use-/g' \
    -e 's/letsencrypt-prod/letsencrypt-staging/g' \
    "$ROOT_DIR/config/cert-manager/approval-policies.yaml"
}

staging_install_certificate_controller() {
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.21.2/cert-manager.yaml >/dev/null || return 1
  local deployment
  for deployment in cert-manager cert-manager-webhook cert-manager-cainjector; do
    kubectl -n cert-manager rollout status "deploy/$deployment" --timeout=240s || return 1
  done
}

staging_prepare_workload_issuer() {
  unset MCP_WORKLOAD_ISSUER_APPROVAL_ACK
  [[ -n "${E2E_MTLS_CLUSTER_ISSUER:-}" ]] || return 0
  local fixture="$WORK_DIR/workload-issuer"
  mkdir -p "$fixture" || return 1
  chmod 700 "$fixture" || return 1
  staging_issuer_policies >"$fixture/policies.yaml" || return 1
  # A fresh independent one-year CA; never copy/reuse the registry signing key.
  kubectl get namespace cert-manager >/dev/null || return 1
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "$fixture/tls.key" -out "$fixture/tls.crt" -days 365 \
    -subj '/CN=disposable-staging-workload-root' \
    -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    >/dev/null 2>&1 || return 1
  chmod 600 "$fixture/tls.key" || return 1
  kubectl -n cert-manager create secret tls mcp-e2e-workload-root \
    --cert="$fixture/tls.crt" --key="$fixture/tls.key" --dry-run=client -o yaml |
    kubectl apply -f - >/dev/null || return 1
  kubectl apply -f - >/dev/null <<YAML || return 1
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: ${E2E_MTLS_CLUSTER_ISSUER}}
spec: {ca: {secretName: mcp-e2e-workload-root}}
YAML
  kubectl wait "clusterissuer/$E2E_MTLS_CLUSTER_ISSUER" --for=condition=Ready --timeout=120s || return 1
  "${WORK_DIR}/linux-amd64/helm" upgrade --install approver-policy oci://quay.io/jetstack/charts/cert-manager-approver-policy \
    --version v0.28.0 -n cert-manager --wait --timeout 10m >/dev/null || return 1
  kubectl apply -f "$fixture/policies.yaml" >/dev/null || return 1
  local policy
  for policy in mcp-session-certificates mcp-workload-tls mcp-public-acme staging-session-certificates staging-workload-tls staging-public-acme; do
    kubectl wait "certificaterequestpolicy/$policy" --for=condition=Ready --timeout=120s || return 1
  done
  kubectl -n cert-manager patch deploy cert-manager --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--controllers=*,-certificaterequests-approver"}]' >/dev/null || return 1
  kubectl -n cert-manager rollout status deploy/cert-manager --timeout=240s || return 1
  staging_verify_workload_approval "$fixture" || return 1
  staging_state_set MCP_WORKLOAD_ISSUER_APPROVAL_ACK true
}

staging_verify_workload_approval() {
  local fixture="$1" namespace=staging-issuer-probe request uri actor expected _attempt state
  kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  kubectl create namespace mcp-platform --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  kubectl -n mcp-platform create serviceaccount mcp-runtime-api --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  kubectl -n "$namespace" create serviceaccount intruder --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  kubectl -n "$namespace" create role request-probe --verb=create,get --resource=certificaterequests.cert-manager.io --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  kubectl -n "$namespace" create rolebinding request-probe --role=request-probe \
    --serviceaccount=mcp-platform:mcp-runtime-api --serviceaccount="$namespace:intruder" --dry-run=client -o yaml | kubectl apply -f - >/dev/null || return 1
  for request in allowed foreign-domain wrong-requester; do
    uri="spiffe://$MCP_TRUST_DOMAIN/ns/$namespace/session/probe"
    actor=system:serviceaccount:mcp-platform:mcp-runtime-api
    expected=Ready
    if [[ "$request" == foreign-domain ]]; then uri=spiffe://foreign.invalid/ns/test/session/forged; expected=Denied; fi
    if [[ "$request" == wrong-requester ]]; then actor="system:serviceaccount:$namespace:intruder"; expected=Unapproved; fi
    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
      -keyout "$fixture/probe.key" -out "$fixture/probe.csr" -subj / -addext "subjectAltName=URI:$uri" >/dev/null 2>&1 || return 1
    kubectl --as="$actor" apply -f - >/dev/null <<YAML || return 1
apiVersion: cert-manager.io/v1
kind: CertificateRequest
metadata: {name: $request, namespace: $namespace}
spec:
  request: $(base64 <"$fixture/probe.csr" | tr -d '\n')
  duration: 30m
  usages: [digital signature, client auth]
  issuerRef: {name: ${E2E_MTLS_CLUSTER_ISSUER}, kind: ClusterIssuer, group: cert-manager.io}
YAML
    for _attempt in $(seq 1 20); do
      state="$(kubectl -n "$namespace" get certificaterequest "$request" -o json)" || return 1
      if jq -e --arg expected "$expected" 'any(.status.conditions[]?; .type == $expected and .status == "True")' <<<"$state" >/dev/null; then break; fi
      if jq -e 'any(.status.conditions[]?; (.type == "Approved" or .type == "Ready") and .status == "True")' <<<"$state" >/dev/null && [[ "$expected" != Ready ]]; then return 1; fi
      sleep 2
    done
    if [[ "$expected" == Unapproved ]]; then
      jq -e '(.status.certificate // "") == "" and (any(.status.conditions[]?; (.type == "Approved" or .type == "Ready") and .status == "True") | not)' <<<"$state" >/dev/null || return 1
    else
      jq -e --arg expected "$expected" 'any(.status.conditions[]?; .type == $expected and .status == "True")' <<<"$state" >/dev/null || return 1
      if [[ "$expected" == Ready ]]; then
        jq -e '(.status.certificate // "") != ""' <<<"$state" >/dev/null || return 1
      else
        jq -e '(.status.certificate // "") == ""' <<<"$state" >/dev/null || return 1
      fi
    fi
    printf 'workload issuer approval probe: %s -> %s\n' "$request" "$expected"
  done
  kubectl delete namespace "$namespace" --wait=false >/dev/null || return 1
}
