#!/usr/bin/env bash
# Rehearse the workload-issuer approval policies on a throwaway Kind cluster.
#
# Purpose: prove config/cert-manager/approval-policies.yaml before cert-manager's
# default approver is turned off anywhere that matters. It checks that only the
# runtime API can obtain a session-bound SPIFFE identity certificate, that every
# certificate the platform itself needs still issues and renews, and that
# forged identities, foreign domains and wrong requesters are never issued.
#
# Prerequisites: docker, kind, kubectl, helm, jq, go. It never touches an existing
# kubeconfig or context: it creates its own Kind cluster and kubeconfig file and
# removes them on exit (KEEP_CLUSTER=1 keeps them for inspection).
#
#   hack/qa/approval-policy-rehearsal.sh
#
# Inputs: CERT_MANAGER_VERSION (default v1.16.2), APPROVER_POLICY_VERSION
# (default v0.17.0), CLUSTER_NAME (default mcp-approval-rehearsal).
# Verification: exits 0 and prints "ALL REHEARSAL CHECKS PASSED".
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
APPROVER_POLICY_VERSION="${APPROVER_POLICY_VERSION:-v0.17.0}"
CLUSTER_NAME="${CLUSTER_NAME:-mcp-approval-rehearsal}"
POLICIES="$ROOT/config/cert-manager/approval-policies.yaml"

for tool in docker kind kubectl helm jq go; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing required tool: $tool" >&2; exit 2; }
done
[[ -f "$POLICIES" ]] || { echo "missing $POLICIES" >&2; exit 2; }

WORK="$(mktemp -d)"
export KUBECONFIG="$WORK/kubeconfig"
FAILED=0

# shellcheck disable=SC2317,SC2329  # invoked by the EXIT trap
cleanup() {
  if [[ "${KEEP_CLUSTER:-0}" == 1 ]]; then
    echo "keeping cluster $CLUSTER_NAME; KUBECONFIG=$KUBECONFIG"
    return
  fi
  kind delete cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

step() { echo; echo "== $*"; }

setup_cluster() {
  step "creating Kind cluster $CLUSTER_NAME"
  kind create cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG" --wait 180s >/dev/null || return 1
  # The rehearsal must only ever run against the cluster it just created.
  [[ "$(kubectl config current-context)" == "kind-$CLUSTER_NAME" ]] || { echo "unexpected context" >&2; return 1; }

  step "installing cert-manager $CERT_MANAGER_VERSION"
  kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null || return 1
  kubectl -n cert-manager rollout status deploy/cert-manager --timeout=240s >/dev/null || return 1
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=240s >/dev/null || return 1

  step "creating the issuers the platform uses (mcp-runtime-ca, letsencrypt-prod stand-in)"
  kubectl apply -f - >/dev/null <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: selfsigned-bootstrap}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: rehearsal-ca, namespace: cert-manager}
spec:
  isCA: true
  commonName: rehearsal-ca
  secretName: rehearsal-ca
  privateKey: {algorithm: ECDSA, size: 256}
  issuerRef: {name: selfsigned-bootstrap, kind: ClusterIssuer}
EOF
  kubectl -n cert-manager wait certificate/rehearsal-ca --for=condition=Ready --timeout=120s >/dev/null || return 1
  kubectl apply -f - >/dev/null <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: mcp-runtime-ca}
spec: {ca: {secretName: rehearsal-ca}}
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: letsencrypt-prod}
spec: {ca: {secretName: rehearsal-ca}}
EOF
  kubectl create namespace mcp-platform --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n mcp-platform create serviceaccount mcp-runtime-api --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n default create serviceaccount intruder --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # Both may create CertificateRequests, like the runtime API's real ClusterRole.
  kubectl apply -f - >/dev/null <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: can-create-certificaterequests}
rules:
  - {apiGroups: [cert-manager.io], resources: [certificaterequests], verbs: [create, get]}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: can-create-certificaterequests}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: can-create-certificaterequests}
subjects:
  - {kind: ServiceAccount, name: mcp-runtime-api, namespace: mcp-platform}
  - {kind: ServiceAccount, name: intruder, namespace: default}
EOF

  step "installing approver-policy $APPROVER_POLICY_VERSION and the policies"
  helm repo add jetstack https://charts.jetstack.io >/dev/null || return 1
  helm repo update >/dev/null || return 1
  helm upgrade --install approver-policy jetstack/cert-manager-approver-policy --version "$APPROVER_POLICY_VERSION" \
    -n cert-manager --wait --timeout 10m >/dev/null || return 1
  kubectl apply -f "$POLICIES" >/dev/null || return 1

  step "disabling cert-manager's default approver (only now, as on a real install)"
  kubectl -n cert-manager patch deploy cert-manager --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--controllers=*,-certificaterequests-approver"}]' >/dev/null || return 1
  kubectl -n cert-manager rollout status deploy/cert-manager --timeout=240s >/dev/null || return 1
}

# cr <name> <as-sa> <issuer> <usages-json> <uri> <dns> [isCA]; prints the outcome.
cr() {
  local name="$1" as="$2" issuer="$3" usages="$4" uri="$5" dns="$6" isca="${7:-false}" req state
  req="$(cd "$ROOT" && go run ./hack/qa/approvalpolicy/csrgen --uri "$uri" --dns "$dns")" || { echo "csr-failed"; return; }
  if ! kubectl --as="$as" apply -f - >/dev/null 2>"$WORK/cr-err.txt" <<EOF
apiVersion: cert-manager.io/v1
kind: CertificateRequest
metadata: {name: $name, namespace: default}
spec:
  request: $req
  isCA: $isca
  usages: $usages
  duration: 30m
  issuerRef: {name: $issuer, kind: ClusterIssuer, group: cert-manager.io}
EOF
  then
    echo "submit-failed: $(tr '\n' ' ' <"$WORK/cr-err.txt" | cut -c1-140)"
    return
  fi
  for _ in $(seq 1 25); do
    state="$(kubectl -n default get certificaterequest "$name" -o json | jq -r '(.status.conditions//[])|map(select(.status=="True")|.type)|join(",")')"
    [[ "$state" == *Denied* || "$state" == *Ready* || "$state" == *Failed* ]] && break
    sleep 2
  done
  # A request no policy applies to is left unapproved: it never gets a certificate.
  [[ -n "$state" ]] || state="Pending(never approved, no certificate issued)"
  echo "$state"
}

# cert <name> <issuer> <dns-csv> <uri> <usages-yaml>; prints Ready or NotReady.
cert() {
  local dns_line="" uri_line="" st
  [[ -n "$3" ]] && dns_line="dnsNames: [$3]"
  [[ -n "$4" ]] && uri_line="uris: [$4]"
  kubectl apply -f - >/dev/null <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: $1, namespace: default}
spec:
  secretName: $1-tls
  duration: 24h
  renewBefore: 8h
  issuerRef: {name: $2, kind: ClusterIssuer, group: cert-manager.io}
  usages: $5
  $dns_line
  $uri_line
EOF
  for _ in $(seq 1 30); do
    st="$(kubectl -n default get certificate "$1" -o json | jq -r '(.status.conditions//[])|map(select(.type=="Ready")|.status)|first // ""')"
    [[ "$st" == True ]] && { echo Ready; return; }
    sleep 2
  done
  echo "NotReady(never approved)"
}

check() { # name expected actual
  if [[ "$3" == *"$2"* ]]; then echo "PASS  $1  ($3)"; else echo "FAIL  $1  expected '$2' got '$3'"; FAILED=1; fi
}

run_matrix() {
  local RA=system:serviceaccount:mcp-platform:mcp-runtime-api
  local CM=system:serviceaccount:cert-manager:cert-manager
  local IN=system:serviceaccount:default:intruder
  local SESS=spiffe://mcpruntime.org/ns/team-a/session/adapter-1
  local TRAEFIK=spiffe://mcpruntime.org/ns/kube-system/sa/traefik
  local SESSION_USAGES='["digital signature","client auth"]'
  local SERVER_USAGES='["digital signature","key encipherment","server auth"]'

  step "direct CertificateRequests by requester"
  check "T1 runtime-api gets a session certificate"             Ready   "$(cr t1 $RA mcp-runtime-ca "$SESSION_USAGES" "$SESS" '')"
  check "T2 another principal cannot get a session certificate" Pending "$(cr t2 $IN mcp-runtime-ca "$SESSION_USAGES" "$SESS" '')"
  check "T3 cert-manager cannot forge a session certificate"    Denied  "$(cr t3 $CM mcp-runtime-ca "$SESSION_USAGES" "$SESS" '')"
  check "T4 session certs cannot carry DNS names"               Denied  "$(cr t4 $RA mcp-runtime-ca "$SESSION_USAGES" "$SESS" 'evil.example')"
  check "T5 session certs cannot be CA certificates"            Denied  "$(cr t5 $RA mcp-runtime-ca "$SESSION_USAGES" "$SESS" '' true)"
  check "T6 session certs cannot be server certificates"        Denied  "$(cr t6 $RA mcp-runtime-ca '["digital signature","server auth"]' "$SESS" '')"
  check "T7 session certs cannot use a non-session URI"         Denied  "$(cr t7 $RA mcp-runtime-ca "$SESSION_USAGES" "$TRAEFIK" '')"
  check "T8 session certs cannot use another trust domain"      Denied  "$(cr t8 $RA mcp-runtime-ca "$SESSION_USAGES" 'spiffe://evil.example/ns/x/session/y' '')"
  check "T9 cert-manager gets the Traefik client identity"      Ready   "$(cr t9 $CM mcp-runtime-ca '["digital signature","key encipherment","client auth"]' "$TRAEFIK" '')"
  check "T10 cert-manager gets a gateway server certificate"    Ready   "$(cr t10 $CM mcp-runtime-ca "$SERVER_USAGES" '' 'cully,cully.mcp-servers,cully.mcp-servers.svc,cully.mcp-servers.svc.cluster.local,mcp.mcpruntime.org')"
  check "T11 cert-manager gets the registry-internal cert"      Ready   "$(cr t11 $CM mcp-runtime-ca "$SERVER_USAGES" '' 'registry.local,registry.registry.svc,registry.registry.svc.cluster.local,registry.mcpruntime.org')"
  check "T12 cert-manager gets public platform certificates"    Ready   "$(cr t12 $CM letsencrypt-prod "$SERVER_USAGES" '' 'platform.mcpruntime.org')"
  check "T13 cert-manager gets the apex and www certificate"    Ready   "$(cr t13 $CM letsencrypt-prod "$SERVER_USAGES" '' 'mcpruntime.org,www.mcpruntime.org')"
  check "T14 the public issuer refuses foreign domains"         Denied  "$(cr t14 $CM letsencrypt-prod "$SERVER_USAGES" '' 'evil.example')"
  check "T15 runtime-api cannot use the public issuer"          Pending "$(cr t15 $RA letsencrypt-prod "$SERVER_USAGES" '' 'platform.mcpruntime.org')"
  check "T16 another principal cannot use the public issuer"    Pending "$(cr t16 $IN letsencrypt-prod "$SERVER_USAGES" '' 'platform.mcpruntime.org')"

  step "Certificate resources, as the operator creates them"
  check "T17 gateway server certificate is issued"              Ready    "$(cert gw mcp-runtime-ca 'cully, cully.mcp-servers, cully.mcp-servers.svc, mcp.mcpruntime.org' '' '[digital signature, key encipherment, server auth]')"
  check "T18 Traefik client identity is issued"                 Ready    "$(cert tc mcp-runtime-ca '' "$TRAEFIK" '[digital signature, key encipherment, client auth]')"
  check "T19 a forged session identity is NOT issued"           NotReady "$(cert forged mcp-runtime-ca '' "$SESS" '[digital signature, client auth]')"
  check "T20 a public host is issued"                           Ready    "$(cert pub letsencrypt-prod 'docs.mcpruntime.org' '' '[digital signature, key encipherment]')"
  check "T21 a foreign domain is NOT issued"                    NotReady "$(cert evil letsencrypt-prod 'evil.example' '' '[digital signature, key encipherment]')"

  step "renewal"
  kubectl -n default delete secret gw-tls >/dev/null
  local renewed=NotReady
  for _ in $(seq 1 30); do
    [[ -n "$(kubectl -n default get secret gw-tls -o jsonpath='{.data.tls\.crt}' 2>/dev/null)" ]] && { renewed=Ready; break; }
    sleep 2
  done
  check "T22 reissue still works under the policies"            Ready "$renewed"
}

setup_cluster || { echo "REHEARSAL SETUP FAILED"; exit 1; }
run_matrix
echo
if [[ "$FAILED" -eq 0 ]]; then echo "ALL REHEARSAL CHECKS PASSED"; else echo "REHEARSAL HAS FAILURES"; fi
exit "$FAILED"
