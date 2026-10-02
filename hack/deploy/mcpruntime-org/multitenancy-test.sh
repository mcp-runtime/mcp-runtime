#!/usr/bin/env bash
set -euo pipefail

# End-to-end multi-tenant demo setup and verification (platform API only).
#
# The default flow does not use kubectl or KUBECONFIG. Admin credentials
# (ADMIN_TOKEN_INPUT or admin email/password) are only used to create teams and
# team users. Each team owner/member logs in with email/password — the same flow
# a normal user follows after signing in through the UI (API keys from the
# dashboard are optional; password login is enough for the CLI). Set
# VERIFY_DEPLOY_PULL_SECRET=1 with KUBECONFIG to additionally verify the
# namespace-local pull secret created by platform-backed server deploy.
#
# Default flow:
#   1. admin: create/update Acme, Globex, and TechCorp teams + users
#   2. team users: build, push, and deploy tenant MCP servers via platform API
#   3. acme owner: apply bounded cross-team grants for managed team agents
#   4. globex/techcorp: adapter MCP calls, event checks, no-kubeconfig smoke
#
# Required environment (no production defaults — set explicitly for your cluster):
#   PLATFORM_URL   platform API base (test-mode port-forward: http://localhost:18080)
#   MCP_URL        public MCP ingress base (test-mode port-forward: http://localhost:18080)
#   REGISTRY_HOST  registry hostname for image build tagging and push target resolution
#
# Optional test-mode admin bootstrap (matches setup --test-mode seed accounts):
#   ADMIN_EMAIL=admin@mcpruntime.org ADMIN_PASSWORD=admin@123
#
# Useful options:
#   RESET=1 hack/deploy/mcpruntime-org/multitenancy-test.sh       # delete demo resources via platform API
#   SKIP_SETUP=1 hack/deploy/mcpruntime-org/multitenancy-test.sh  # only run verification

# The Staging E2E VM receives the repo as a tarball packaged with
# `tar --exclude=.git`, so git metadata is absent there and `rev-parse` exits
# 128. Prefer an explicitly supplied root, then git, then this script's own
# location, which is always <root>/hack/deploy/mcpruntime-org.
ROOT_DIR="${MCPRUNTIME_ORG_ROOT:-}"
if [[ -z "$ROOT_DIR" ]]; then
  ROOT_DIR="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel 2>/dev/null || true)"
fi
if [[ -z "$ROOT_DIR" ]]; then
  ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
fi
BIN="${BIN:-$ROOT_DIR/bin/mcp-runtime}"
# shellcheck source=hack/deploy/mcpruntime-org/lib/adapter-readiness.sh
source "$ROOT_DIR/hack/deploy/mcpruntime-org/lib/adapter-readiness.sh"

# A dotenv is a convenience for local runs, not an override. Sourcing it under
# `set -a` replaced values the caller had already exported, so a run explicitly
# aimed at one environment was silently redirected to whichever one .env named
# -- a caller passing PLATFORM_URL for a disposable E2E host had it swapped for
# the production platform, and KUBECONFIG along with it. Fill gaps only.
if [[ -f "$ROOT_DIR/.env" ]]; then
  while IFS= read -r dotenv_line || [[ -n "$dotenv_line" ]]; do
    dotenv_line="${dotenv_line#"${dotenv_line%%[![:space:]]*}"}"
    case "$dotenv_line" in
      '' | '#'*) continue ;;
    esac
    dotenv_line="${dotenv_line#export }"
    [[ "$dotenv_line" == *=* ]] || continue
    dotenv_key="${dotenv_line%%=*}"
    [[ "$dotenv_key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    dotenv_value="${dotenv_line#*=}"
    case "$dotenv_value" in
      \"*\") dotenv_value="${dotenv_value:1:${#dotenv_value}-2}" ;;
      \'*\') dotenv_value="${dotenv_value:1:${#dotenv_value}-2}" ;;
    esac
    if [[ -z "${!dotenv_key:-}" ]]; then
      export "$dotenv_key=$dotenv_value"
    fi
  done <"$ROOT_DIR/.env"
  unset dotenv_line dotenv_key dotenv_value
fi

require_env() {
  local name="$1"
  local hint="$2"
  if [[ -z "${!name:-}" ]]; then
    echo "${name} is required (${hint})" >&2
    exit 1
  fi
}

require_env PLATFORM_URL "e.g. http://localhost:18080 for test-mode port-forward"
require_env MCP_URL "e.g. http://localhost:18080 for test-mode port-forward"
require_env REGISTRY_HOST "registry hostname for build tagging and tenant registry push target"

PLATFORM_URL="${PLATFORM_URL%/}"
MCP_URL="${MCP_URL%/}"
MCP_HOST="${MCP_URL#https://}"
MCP_HOST="${MCP_HOST#http://}"
MCP_HOST="${MCP_HOST%%/*}"
MCP_RUNTIME_CONFIG_DIR="${MCP_RUNTIME_CONFIG_DIR:-$HOME/.mcpruntime}"
CREDS="${MCP_RUNTIME_CONFIG_DIR}/config.json"
TMP_ROOT="${TMPDIR:-/tmp}"
TMP_ROOT="${TMP_ROOT%/}"
RUN_ID="${RUN_ID:-mt$(date +%m%d%H%M%S)-$((RANDOM % 9000 + 1000))}"
WORK_DIR="${WORK_DIR:-$TMP_ROOT/mcp-runtime-multitenancy-${RUN_ID}}"
TAG="${TAG:-v0.1.0}"
ADAPTER_LISTEN="${ADAPTER_LISTEN:-127.0.0.1:8299}"
# In-cluster Sentinel ingest endpoint for gateway analytics; set empty to skip.
ANALYTICS_INGEST_URL="${ANALYTICS_INGEST_URL-http://mcp-ingest.mcp-observability.svc.cluster.local:8081/events}"

SERVER_CONTEXT="${SERVER_CONTEXT:-$ROOT_DIR/examples/oauth-example-go-2025-11-25}"
SERVER_DOCKERFILE="${SERVER_DOCKERFILE:-$SERVER_CONTEXT/Dockerfile}"

ADMIN_EMAIL="${ADMIN_EMAIL:-admin@mcpruntime.org}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin@123}"
ADMIN_TOKEN_INPUT="${ADMIN_TOKEN_INPUT:-}"
ACME_EMAIL="${ACME_EMAIL:-acme-owner-${RUN_ID}@example.com}"
ACME_PASSWORD="${ACME_PASSWORD:-acme-owner-123}"
GLOBEX_EMAIL="${GLOBEX_EMAIL:-globex-user-${RUN_ID}@example.com}"
GLOBEX_PASSWORD="${GLOBEX_PASSWORD:-globex-user-123}"
TECHCORP_EMAIL="${TECHCORP_EMAIL:-techcorp-dev-${RUN_ID}@example.com}"
TECHCORP_PASSWORD="${TECHCORP_PASSWORD:-techcorp-dev-123}"

ADMIN_PROFILE="${ADMIN_PROFILE:-admin}"
ACME_PROFILE="${ACME_PROFILE:-acme-owner-${RUN_ID}}"
GLOBEX_PROFILE="${GLOBEX_PROFILE:-globex-user-${RUN_ID}}"
TECHCORP_PROFILE="${TECHCORP_PROFILE:-techcorp-dev-${RUN_ID}}"

ACME_SLUG="${ACME_SLUG:-acme-${RUN_ID}}"
GLOBEX_SLUG="${GLOBEX_SLUG:-globex-${RUN_ID}}"
TECHCORP_SLUG="${TECHCORP_SLUG:-techcorp-${RUN_ID}}"
ACME_NS="${ACME_NS:-mcp-team-${ACME_SLUG}}"
GLOBEX_NS="${GLOBEX_NS:-mcp-team-${GLOBEX_SLUG}}"
TECHCORP_NS="${TECHCORP_NS:-mcp-team-${TECHCORP_SLUG}}"
ACME_SERVER="${ACME_SERVER:-acme-tools-${RUN_ID}}"
GLOBEX_SERVER="${GLOBEX_SERVER:-globex-tools-${RUN_ID}}"
TECHCORP_SERVER="${TECHCORP_SERVER:-techcorp-tools-${RUN_ID}}"
GLOBEX_AGENT_ID="${GLOBEX_AGENT_ID:-}"
TECHCORP_AGENT_ID="${TECHCORP_AGENT_ID:-}"
GLOBEX_DENIED_AGENT_ID="${GLOBEX_DENIED_AGENT_ID:-}"

# Quickstart parity: by default tenant users run with only their saved auth
# profile (no MCP_* registry/domain env), which is how a real user runs the
# CLI. A platform on localhost (test-mode port-forward) has no public registry
# to record at login, so it keeps the explicit REGISTRY_HOST override.
USER_FLOW_UNSET_ENV=(
  MCP_REGISTRY_INGRESS_HOST
  MCP_REGISTRY_HOST
  MCP_REGISTRY_ENDPOINT
  MCP_REGISTRY_PULL_HOST
  MCP_PLATFORM_DOMAIN
  MCP_PLATFORM_INGRESS_HOST
  MCP_MCP_INGRESS_HOST
  MCP_DEFAULT_INGRESS_HOST
  MCP_RUNTIME_TEST_MODE
)
if [[ -z "${PROFILE_ONLY_REGISTRY:-}" ]]; then
  case "$(printf '%s' "$PLATFORM_URL" | sed -E 's#^[a-zA-Z]+://##; s#[/:].*$##')" in
    localhost | 127.0.0.1) PROFILE_ONLY_REGISTRY=0 ;;
    *) PROFILE_ONLY_REGISTRY=1 ;;
  esac
fi

# Force platform-API paths; never touch the local kubeconfig.
VERIFY_PULL_SECRET_KUBECONFIG="${KUBECONFIG:-}"
unset KUBECONFIG
export MCP_RUNTIME_CONFIG_DIR

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

stop_listen_port() {
  local listen="$1"
  local port="${listen##*:}"
  if [[ -z "$port" || "$port" == "$listen" ]]; then
    return 0
  fi
  if ! command -v lsof >/dev/null 2>&1; then
    return 0
  fi
  local pids
  pids="$(lsof -nP -iTCP:"${port}" -sTCP:LISTEN -t 2>/dev/null || true)"
  if [[ -n "$pids" ]]; then
    kill $pids >/dev/null 2>&1 || true
    sleep 0.5
  fi
}

wait_for_adapter_proxy() {
  local listen="$1"
  local log_file="$2"
  for _ in {1..60}; do
    if [[ -f "$log_file" ]] && grep -q "listening on ${listen}" "$log_file"; then
      return 0
    fi
    sleep 0.25
  done
  echo "adapter proxy did not start on ${listen}" >&2
  [[ -f "$log_file" ]] && cat "$log_file" >&2
  return 1
}

# Run the CLI as a signed-in platform user. With PROFILE_ONLY_REGISTRY=1 the
# registry/domain env this script's caller may export (Staging E2E exports
# MCP_REGISTRY_INGRESS_HOST, MCP_REGISTRY_ENDPOINT, ...) is removed, so the CLI
# resolves the registry from the saved auth profile exactly like a quickstart
# user on a laptop with no MCP_* env and no kubeconfig.
run_as() {
  local profile="$1"
  shift
  local env_args=()
  local name
  if [[ "$PROFILE_ONLY_REGISTRY" == "1" ]]; then
    for name in "${USER_FLOW_UNSET_ENV[@]}"; do
      env_args+=(-u "$name")
    done
  fi
  env_args+=(KUBECONFIG="" MCP_PLATFORM_API_TOKEN="" MCP_PLATFORM_API_URL="$PLATFORM_URL" MCP_PLATFORM_API_PROFILE="$profile")
  if [[ "$PROFILE_ONLY_REGISTRY" != "1" ]]; then
    env_args+=(MCP_REGISTRY_INGRESS_HOST="$REGISTRY_HOST")
  fi
  env "${env_args[@]}" "$BIN" "$@"
}

json_escape() {
  jq -Rn --arg v "$1" '$v'
}

profile_token() {
  local profile="$1"
  jq -er --arg profile "$profile" '.accounts[$profile].token // (select(.current == $profile) | .token) // empty' "$CREDS"
}

profile_role() {
  local profile="$1"
  jq -r --arg profile "$profile" '.accounts[$profile].role // (select(.current == $profile) | .role) // empty' "$CREDS"
}

require_tenant_publish_profile() {
  local profile="$1"
  local role
  if [[ "$profile" == "$ADMIN_PROFILE" ]]; then
    echo "refusing tenant publish/deploy with admin profile ${profile}; use a team owner/member profile" >&2
    exit 1
  fi
  role="$(profile_role "$profile")"
  if [[ "$role" == "admin" ]]; then
    echo "refusing tenant publish/deploy with admin role in profile ${profile}; use a team owner/member profile" >&2
    exit 1
  fi
}

admin_login() {
  if [[ -n "${ADMIN_TOKEN_INPUT}" ]]; then
    KUBECONFIG="" "$BIN" auth login --api-url "$PLATFORM_URL" --token "$ADMIN_TOKEN_INPUT" --profile "$ADMIN_PROFILE"
  else
    KUBECONFIG="" "$BIN" auth login --api-url "$PLATFORM_URL" --username "$ADMIN_EMAIL" --password "$ADMIN_PASSWORD" --profile "$ADMIN_PROFILE"
  fi
}

team_user_login() {
  local profile="$1"
  local email="$2"
  local password="$3"
  KUBECONFIG="" "$BIN" auth login --api-url "$PLATFORM_URL" --username "$email" --password "$password" --profile "$profile"
}

team_id() {
  local slug="$1"
  local token="$2"
  curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    "${PLATFORM_URL}/api/v1/runtime/teams/${slug}" | jq -er '.team.id'
}

ensure_managed_agent() {
  local team_slug="$1"
  local agent_name="$2"
  local requested_id="${3:-}"
  local listing matches agent_id status listed_team

  listing="$(run_as "$ADMIN_PROFILE" agent list "$team_slug" --limit 200)"
  if [[ -n "$requested_id" ]]; then
    matches="$(awk -v id="$requested_id" '$1 == id { print $1, $3, $4 }' <<<"$listing")"
  else
    matches="$(awk -v name="$agent_name" '$2 == name { print $1, $3, $4 }' <<<"$listing")"
  fi
  if [[ -z "$matches" && -z "$requested_id" ]]; then
    run_as "$ADMIN_PROFILE" agent create "$team_slug" --name "$agent_name" >/dev/null
    listing="$(run_as "$ADMIN_PROFILE" agent list "$team_slug" --limit 200)"
    matches="$(awk -v name="$agent_name" '$2 == name { print $1, $3, $4 }' <<<"$listing")"
  fi
  [[ "$(wc -l <<<"$matches" | tr -d '[:space:]')" == 1 ]] || {
    echo "expected one managed agent ${requested_id:-$agent_name} in team ${team_slug}; got: ${matches:-none}" >&2
    return 1
  }
  read -r agent_id status listed_team <<<"$matches"
  [[ "$status" == "active" && "$listed_team" == "$team_slug" ]] || {
    echo "managed agent ${agent_id} must be active in team ${team_slug}; got status=${status} team=${listed_team}" >&2
    return 1
  }
  printf '%s\n' "$agent_id"
}

setup_managed_agents() {
  local agent_run_suffix
  agent_run_suffix="$(printf '%s' "$RUN_ID" | tr -cd '[:alnum:]' | tail -c 12)"
  GLOBEX_AGENT_ID="$(ensure_managed_agent "$GLOBEX_SLUG" "e2e-${agent_run_suffix}-globex" "$GLOBEX_AGENT_ID")"
  TECHCORP_AGENT_ID="$(ensure_managed_agent "$TECHCORP_SLUG" "e2e-${agent_run_suffix}-techcorp" "$TECHCORP_AGENT_ID")"
  GLOBEX_DENIED_AGENT_ID="$(ensure_managed_agent "$GLOBEX_SLUG" "e2e-${agent_run_suffix}-denied" "$GLOBEX_DENIED_AGENT_ID")"
  mkdir -p "$WORK_DIR"
  jq -n \
    --arg acmeTeamID "$ACME_TEAM_ID" \
    --arg globexTeamID "$GLOBEX_TEAM_ID" \
    --arg globexAgentID "$GLOBEX_AGENT_ID" \
    --arg globexDeniedAgentID "$GLOBEX_DENIED_AGENT_ID" \
    --arg techcorpTeamID "$TECHCORP_TEAM_ID" \
    --arg techcorpAgentID "$TECHCORP_AGENT_ID" \
    '{acme:{teamID:$acmeTeamID},globex:{teamID:$globexTeamID,agentID:$globexAgentID,deniedAgentID:$globexDeniedAgentID},techcorp:{teamID:$techcorpTeamID,agentID:$techcorpAgentID}}' \
    >"$WORK_DIR/managed-agents.json"
}

team_exists() {
  local slug="$1"
  local token="$2"
  curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    "${PLATFORM_URL}/api/v1/runtime/teams/${slug}" >/dev/null 2>&1
}

team_user_exists() {
  local slug="$1"
  local email="$2"
  local token="$3"
  local body
  body="$(curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    "${PLATFORM_URL}/api/v1/runtime/teams/${slug}/members" 2>/dev/null || echo '{"members":[]}')"
  jq -e --arg email "$email" 'any(.members[]?; (.email // "") == $email)' <<<"$body" >/dev/null
}

server_exists() {
  local profile="$1"
  local namespace="$2"
  local server="$3"
  local token
  token="$(profile_token "$profile")"
  curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    "${PLATFORM_URL}/api/v1/runtime/servers/${namespace}/${server}" >/dev/null 2>&1
}

create_or_update_team() {
  local slug="$1"
  local name="$2"
  local token
  token="$(profile_token "$ADMIN_PROFILE")"
  if team_exists "$slug" "$token"; then
    echo "team ${slug} exists; ensuring namespace/RBAC via platform API"
    if ! run_as "$ADMIN_PROFILE" team create "$slug" --name "$name"; then
      echo "team ${slug} exists but platform API did not accept idempotent create; continuing with existing team" >&2
    fi
    return 0
  else
    echo "creating team ${slug}"
  fi
  run_as "$ADMIN_PROFILE" team create "$slug" --name "$name"
}

ensure_team_user() {
  local slug="$1"
  local email="$2"
  local password="$3"
  local role="$4"
  local token
  token="$(profile_token "$ADMIN_PROFILE")"
  if team_user_exists "$slug" "$email" "$token"; then
    echo "team user ${email} already exists in ${slug}; ensuring role ${role}"
  else
    echo "creating team user ${email} in ${slug} as ${role}"
  fi
  run_as "$ADMIN_PROFILE" team user create "$slug" --username "$email" --password "$password" --role "$role"
}

init_metadata() {
  local profile="$1"
  local metadata_dir="$2"
  local server="$3"

  mkdir -p "$metadata_dir"
  run_as "$profile" server init "$server" \
    --metadata-dir "$metadata_dir" \
    --scope tenant \
    --tag "$TAG" \
    --port 8088 \
    --tool aaa-ping \
    --tool add \
    --tool upper \
    --tool lower \
    --tool echo \
    --tool-spec slugify:medium:read \
    --force
  # Gateway analytics are opt-in per server (spec.analytics); without it the
  # sidecar emits nothing and the event checks below have nothing to find. The
  # platform deploy then provisions the namespace-local ingest key Secret.
  if [[ -n "${ANALYTICS_INGEST_URL}" ]]; then
    printf '      analytics:\n        ingestURL: "%s"\n' "${ANALYTICS_INGEST_URL}" >>"${metadata_dir}/servers.yaml"
  fi
}

verify_metadata_governance() {
  local metadata_dir="$1"
  local server="$2"
  local file="${metadata_dir}/servers.yaml"

  for pattern in \
    "sideEffect: read" \
    "requiredTrust: low" \
    "defaultDecision: deny" \
    "required: true" \
    "enabled: true"; do
    if ! grep -q "$pattern" "$file"; then
      echo "metadata for ${server} missing expected governed default: ${pattern}" >&2
      cat "$file" >&2
      exit 1
    fi
  done
}

image_ref_from_metadata() {
  awk '$1=="image:"{i=$2} $1=="imageTag:"{t=$2} END{if(i=="" || t=="") exit 1; print i ":" t}' "$1/servers.yaml"
}

publish_server() {
  local profile="$1"
  local server="$2"
  local namespace="$3"
  local metadata_dir="$4"

  require_tenant_publish_profile "$profile"
  local deploy_update=0
  if server_exists "$profile" "$namespace" "$server"; then
    echo "server ${server} already exists for ${profile}; publishing a new image tag and updating it"
    deploy_update=1
  else
    echo "server ${server} does not exist for ${profile}; publishing and deploying it"
  fi

  run_as "$profile" server build image "$server" \
    --metadata-dir "$metadata_dir" \
    --dockerfile "$SERVER_DOCKERFILE" \
    --context "$SERVER_CONTEXT" \
    --tag "$TAG"

  local image_ref
  image_ref="$(image_ref_from_metadata "$metadata_dir")"
  # The saved profile names the public registry; a ref on any other host (for
  # example the in-cluster registry.registry.svc.cluster.local:5000) would push
  # fine but deploy into ImagePullBackOff on a remote platform.
  if [[ "$PROFILE_ONLY_REGISTRY" == "1" && "$image_ref" != "${REGISTRY_HOST}/"* ]]; then
    echo "server build image tagged ${image_ref} for ${profile}; expected the saved profile registry ${REGISTRY_HOST}" >&2
    cat "${metadata_dir}/servers.yaml" >&2
    exit 1
  fi

  echo "Pushing image ${image_ref} as ${profile} via server push (platform API)..."
  run_as "$profile" server push --scope tenant --image "$image_ref"

  verify_metadata_governance "$metadata_dir" "$server"

  local deploy_args=(server deploy "$server" --scope tenant --metadata-dir "$metadata_dir")
  if [[ "$deploy_update" == "1" ]]; then
    deploy_args+=(--update)
  fi
  run_as "$profile" "${deploy_args[@]}"

  if [[ "${VERIFY_DEPLOY_PULL_SECRET:-0}" == "1" ]]; then
    if [[ -z "$VERIFY_PULL_SECRET_KUBECONFIG" ]]; then
      echo "VERIFY_DEPLOY_PULL_SECRET=1 requires KUBECONFIG for the post-deploy assertion" >&2
      exit 1
    fi
    verify_namespace_registry_pull_secret "$namespace"
  fi
}

verify_namespace_registry_pull_secret() {
  local namespace="$1"
  local secret_type pull_refs
  secret_type="$(kubectl --kubeconfig "$VERIFY_PULL_SECRET_KUBECONFIG" get secret mcp-runtime-registry-pull -n "$namespace" -o jsonpath='{.type}')"
  if [[ "$secret_type" != "kubernetes.io/dockerconfigjson" ]]; then
    echo "server deploy did not create a docker registry pull secret in ${namespace}" >&2
    exit 1
  fi
  pull_refs="$(kubectl --kubeconfig "$VERIFY_PULL_SECRET_KUBECONFIG" get serviceaccount mcp-workload -n "$namespace" -o jsonpath='{.imagePullSecrets[*].name}')"
  if [[ " $pull_refs " != *" mcp-runtime-registry-pull "* ]]; then
    echo "mcp-runtime-registry-pull is not attached to mcp-workload in ${namespace}" >&2
    exit 1
  fi
  echo "=== server deploy registry pull secret in ${namespace}: OK ==="
}

init_grant() {
  local file="$1"
  local grant_name="$2"
  local server_name="$3"
  local server_ns="$4"
  local subject_team_id="$5"
  local subject_agent_id="$6"

  run_as "$ACME_PROFILE" access grant init "$grant_name" \
    --namespace "$server_ns" \
    --server "$server_name" \
    --server-namespace "$server_ns" \
    --team-id "$subject_team_id" \
    --agent-id "$subject_agent_id" \
    --expires-in 1h \
    --trust low \
    --side-effect read \
    --tool-rule aaa-ping:allow:low \
    --tool-rule add:allow:low \
    --tool-rule upper:allow:low \
    --tool-rule lower:allow:low \
    --tool-rule echo:allow:low \
    --tool-rule slugify:allow:medium \
    --output "$file" \
    --force
}

init_session() {
  local file="$1"
  local session_name="$2"
  local server_name="$3"
  local server_ns="$4"
  local subject_team_id="$5"
  local subject_agent_id="$6"

  run_as "$ACME_PROFILE" access session init "$session_name" \
    --namespace "$server_ns" \
    --server "$server_name" \
    --server-namespace "$server_ns" \
    --team-id "$subject_team_id" \
    --agent-id "$subject_agent_id" \
    --trust low \
    --policy-version v1 \
    --expires-in 1h \
    --output "$file" \
    --force
}

wait_for_rollout() {
  local profile="$1"
  local namespace="$2"
  local server="$3"
  local token
  token="$(profile_token "$profile")"
  echo "=== waiting for rollout: ${server} in ${namespace} (profile ${profile}) ==="
  local deadline=$(( $(date +%s) + 300 ))
  while true; do
    local body
    body="$(curl -fsS \
      -H "x-api-key: ${token}" \
      -H "authorization: Bearer ${token}" \
      "${PLATFORM_URL}/api/v1/runtime/servers/${namespace}/${server}" 2>/dev/null || echo '{}')"
    local ready_str status status_lc
    ready_str="$(echo "$body" | jq -r '.server.ready // "0/0"' 2>/dev/null || echo "0/0")"
    status="$(echo "$body" | jq -r '.server.status // empty' 2>/dev/null || true)"
    status_lc="$(printf '%s' "$status" | tr '[:upper:]' '[:lower:]')"
    local ready_count total_count
    ready_count="${ready_str%%/*}"
    total_count="${ready_str##*/}"
    # Require both replica readiness and operator Status=Ready. After gateway
    # default-on, pods can be 1/1 while Traefik mTLS Secrets are still issuing.
    if [[ "$ready_count" =~ ^[0-9]+$ && "$total_count" =~ ^[0-9]+$ && "$total_count" -ge 1 && "$ready_count" -ge "$total_count" && "$status_lc" == "ready" ]]; then
      echo "rollout complete: ${server}"
      return 0
    fi
    if [[ $(date +%s) -gt $deadline ]]; then
      echo "timeout waiting for rollout: ${server} (ready=${ready_str} status=${status})" >&2
      echo "last response: $body" >&2
      return 1
    fi
    sleep 5
  done
}

delete_all_sessions() {
  local profile="$1"
  local ns="$2"
  local token
  token="$(profile_token "$profile")"
  local sessions_body
  sessions_body="$(curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    "${PLATFORM_URL}/api/v1/runtime/sessions?namespace=${ns}" 2>/dev/null || echo '{"sessions":[]}')"
  local names
  names="$(echo "$sessions_body" | jq -r '(.sessions // .) | .[].name' 2>/dev/null || true)"
  for name in $names; do
    [[ -z "$name" ]] && continue
    curl -fsS -X DELETE \
      -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
      "${PLATFORM_URL}/api/v1/runtime/sessions/${ns}/${name}" >/dev/null 2>&1 || true
  done
}

verify_grant_exists() {
  local profile="$1"
  local name="$2"
  local ns="$3"
  local token
  token="$(profile_token "$profile")"
  local deadline=$(( $(date +%s) + 90 ))
  echo "waiting for grant ${ns}/${name} to be visible via platform API"
  while true; do
    if curl -fsS \
      -H "x-api-key: ${token}" \
      -H "authorization: Bearer ${token}" \
      "${PLATFORM_URL}/api/v1/runtime/grants/${ns}/${name}" >/dev/null 2>&1; then
      echo "grant visible: ${ns}/${name}"
      return 0
    fi
    if [[ $(date +%s) -gt $deadline ]]; then
      echo "timeout waiting for grant ${ns}/${name} to be visible via platform API" >&2
      return 1
    fi
    sleep 3
  done
}

setup_demo() {
  if [[ ! -f "$SERVER_DOCKERFILE" ]]; then
    echo "server Dockerfile not found: $SERVER_DOCKERFILE" >&2
    exit 1
  fi

  admin_login

  if [[ "${RESET:-0}" == "1" ]]; then
    local _token
    _token="$(profile_token "$ADMIN_PROFILE")"
    delete_all_sessions "$ADMIN_PROFILE" "$ACME_NS"
    for _ns_name in "${ACME_NS}/${ACME_SERVER}-${GLOBEX_SLUG}-grant" "${ACME_NS}/${ACME_SERVER}-${TECHCORP_SLUG}-grant"; do
      local _ns="${_ns_name%%/*}" _name="${_ns_name##*/}"
      curl -fsS -X DELETE -H "x-api-key: ${_token}" -H "authorization: Bearer ${_token}" "${PLATFORM_URL}/api/v1/runtime/grants/${_ns}/${_name}" >/dev/null 2>&1 || true
    done
    for _ns_name in "${ACME_NS}/${ACME_SERVER}" "${GLOBEX_NS}/${GLOBEX_SERVER}" "${TECHCORP_NS}/${TECHCORP_SERVER}"; do
      local _ns="${_ns_name%%/*}" _name="${_ns_name##*/}"
      curl -fsS -X DELETE -H "x-api-key: ${_token}" -H "authorization: Bearer ${_token}" "${PLATFORM_URL}/api/v1/runtime/servers/${_ns}/${_name}" >/dev/null 2>&1 || true
    done
  fi

  create_or_update_team "$ACME_SLUG" "Acme ${RUN_ID}"
  create_or_update_team "$GLOBEX_SLUG" "Globex ${RUN_ID}"
  create_or_update_team "$TECHCORP_SLUG" "TechCorp ${RUN_ID}"
  ensure_team_user "$ACME_SLUG" "$ACME_EMAIL" "$ACME_PASSWORD" owner
  ensure_team_user "$GLOBEX_SLUG" "$GLOBEX_EMAIL" "$GLOBEX_PASSWORD" member
  ensure_team_user "$TECHCORP_SLUG" "$TECHCORP_EMAIL" "$TECHCORP_PASSWORD" member

  ADMIN_TOKEN="$(profile_token "$ADMIN_PROFILE")"
  ACME_TEAM_ID="$(team_id "$ACME_SLUG" "$ADMIN_TOKEN")"
  GLOBEX_TEAM_ID="$(team_id "$GLOBEX_SLUG" "$ADMIN_TOKEN")"
  TECHCORP_TEAM_ID="$(team_id "$TECHCORP_SLUG" "$ADMIN_TOKEN")"
  printf "acme=%s\nglobex=%s\ntechcorp=%s\n" "$ACME_TEAM_ID" "$GLOBEX_TEAM_ID" "$TECHCORP_TEAM_ID"

  team_user_login "$ACME_PROFILE" "$ACME_EMAIL" "$ACME_PASSWORD"
  team_user_login "$GLOBEX_PROFILE" "$GLOBEX_EMAIL" "$GLOBEX_PASSWORD"
  team_user_login "$TECHCORP_PROFILE" "$TECHCORP_EMAIL" "$TECHCORP_PASSWORD"

  setup_managed_agents

  local acme_metadata_dir="$WORK_DIR/${ACME_SERVER}/.mcp"
  local globex_metadata_dir="$WORK_DIR/${GLOBEX_SERVER}/.mcp"
  local techcorp_metadata_dir="$WORK_DIR/${TECHCORP_SERVER}/.mcp"
  local acme_globex_grant="$WORK_DIR/${ACME_SERVER}-${GLOBEX_SLUG}-grant.yaml"
  local acme_techcorp_grant="$WORK_DIR/${ACME_SERVER}-${TECHCORP_SLUG}-grant.yaml"
  local acme_globex_session="$WORK_DIR/${ACME_SERVER}-${GLOBEX_SLUG}-manual-session.yaml"

  init_metadata "$ACME_PROFILE" "$acme_metadata_dir" "$ACME_SERVER"
  init_metadata "$GLOBEX_PROFILE" "$globex_metadata_dir" "$GLOBEX_SERVER"
  init_metadata "$TECHCORP_PROFILE" "$techcorp_metadata_dir" "$TECHCORP_SERVER"

  publish_server "$ACME_PROFILE" "$ACME_SERVER" "$ACME_NS" "$acme_metadata_dir"
  publish_server "$GLOBEX_PROFILE" "$GLOBEX_SERVER" "$GLOBEX_NS" "$globex_metadata_dir"
  publish_server "$TECHCORP_PROFILE" "$TECHCORP_SERVER" "$TECHCORP_NS" "$techcorp_metadata_dir"

  wait_for_rollout "$ACME_PROFILE" "$ACME_NS" "$ACME_SERVER"
  wait_for_rollout "$GLOBEX_PROFILE" "$GLOBEX_NS" "$GLOBEX_SERVER"
  wait_for_rollout "$TECHCORP_PROFILE" "$TECHCORP_NS" "$TECHCORP_SERVER"

  init_grant "$acme_globex_grant" "${ACME_SERVER}-${GLOBEX_SLUG}-grant" "$ACME_SERVER" "$ACME_NS" "$GLOBEX_TEAM_ID" "$GLOBEX_AGENT_ID"
  init_grant "$acme_techcorp_grant" "${ACME_SERVER}-${TECHCORP_SLUG}-grant" "$ACME_SERVER" "$ACME_NS" "$TECHCORP_TEAM_ID" "$TECHCORP_AGENT_ID"
  init_session "$acme_globex_session" "${ACME_SERVER}-${GLOBEX_SLUG}-manual-session" "$ACME_SERVER" "$ACME_NS" "$GLOBEX_TEAM_ID" "$GLOBEX_AGENT_ID"
  run_as "$ACME_PROFILE" access grant apply --file "$acme_globex_grant"
  run_as "$ACME_PROFILE" access grant apply --file "$acme_techcorp_grant"
  run_as "$ADMIN_PROFILE" access session apply --file "$acme_globex_session"
  run_as "$ADMIN_PROFILE" access session get "${ACME_SERVER}-${GLOBEX_SLUG}-manual-session" --namespace "$ACME_NS" >/dev/null
}

verify_no_kubeconfig_ops() {
  echo "=== verify: binary works without KUBECONFIG for non-cluster operations ==="
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local tmp_creds="$tmp_dir/config.json"

  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" auth --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" auth login --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" team --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" registry --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" server push --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" server deploy --help >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" adapter --help >/dev/null

  local login_ok=0
  if [[ -n "${ADMIN_TOKEN_INPUT}" ]]; then
    KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" \
      "$BIN" auth login --api-url "$PLATFORM_URL" --token "$ADMIN_TOKEN_INPUT" --profile "nokubeconfig-admin" && login_ok=1
  else
    KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" \
      "$BIN" auth login --api-url "$PLATFORM_URL" --username "$ADMIN_EMAIL" --password "$ADMIN_PASSWORD" \
      --profile "nokubeconfig-admin" && login_ok=1
  fi
  if [[ "$login_ok" != "1" ]]; then
    echo "auth login without KUBECONFIG failed" >&2
    exit 1
  fi

  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" auth status >/dev/null
  KUBECONFIG="" MCP_RUNTIME_CONFIG_DIR="$tmp_dir" "$BIN" auth logout >/dev/null

  rm -rf "$tmp_dir"
  echo "=== no-kubeconfig ops: OK ==="
}

precreate_adapter_session() {
  local profile="$1"
  local agent_id="$2"
  local token body
  token="$(profile_token "$profile")"
  body="$(curl -fsS \
    -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
    -H "content-type: application/json" \
    --data "{\"serverName\":\"${ACME_SERVER}\",\"namespace\":\"${ACME_NS}\",\"agentID\":\"${agent_id}\"}" \
    "${PLATFORM_URL}/api/v1/runtime/adapter/sessions")"
  jq -er '.name' <<<"$body" >/dev/null
}

# Wait until the public MCP route serves gateway identity denials instead of
# Traefik 502/404 while mTLS backend Secrets and IngressRoute are still forming.
wait_for_public_mcp_route() {
  local body="$WORK_DIR/public-route-wait.body"
  local status=""
  local deadline=$(( $(date +%s) + 240 ))
  echo "=== waiting for public MCP route: ${MCP_URL}/${ACME_SERVER}/mcp ==="
  while true; do
    : >"$body"
    status="$(
      curl -ksS -o "$body" -w '%{http_code}' \
        -H "content-type: application/json" \
        --data '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aaa-ping","arguments":{"note":"public-route-ready-check"}}}' \
        "${MCP_URL}/${ACME_SERVER}/mcp" 2>/dev/null || true
    )"
    [[ "$status" =~ ^[0-9]{3}$ ]] || status="000"
    if [[ "$status" == "401" ]] && jq -e '.error == "missing_identity"' "$body" >/dev/null 2>&1; then
      echo "public MCP route ready (missing_identity)"
      return 0
    fi
    if [[ $(date +%s) -gt $deadline ]]; then
      echo "timeout waiting for public MCP route to return missing_identity (401); last HTTP ${status}" >&2
      cat "$body" >&2 || true
      return 1
    fi
    sleep 3
  done
}

verify_direct_public_denied() {
  local body="$WORK_DIR/direct-public.body"
  local status
  status="$(
    curl -ksS -o "$body" -w '%{http_code}' \
      -H "content-type: application/json" \
      --data '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"aaa-ping","arguments":{"note":"direct-public-deny-check"}}}' \
      "${MCP_URL}/${ACME_SERVER}/mcp"
  )"
  # After the cert-first identity model (#494), unauthenticated public calls
  # fail the identity rung with missing_identity before any OAuth bearer check.
  if [[ "$status" != "401" ]] || ! jq -e '.error == "missing_identity"' "$body" >/dev/null; then
    echo "expected direct public call without identity to fail with missing_identity (401), got HTTP ${status}" >&2
    cat "$body" >&2
    exit 1
  fi
  echo "=== direct public call denied without identity: OK ==="
}

adapter_call_add_for() {
  local profile="$1"
  local listen="$2"
  local log_file="$3"
  local headers_file="$4"
  local init_body="$5"
  local notify_body="$6"
  local result_body="$7"
  local arg_a="${8}"
  local arg_b="${9}"
  local expected="${10}"
  local agent_id="${11}"
  local adapter_url="http://${listen}"

  stop_listen_port "$listen"
  : >"$log_file"
  MCP_PLATFORM_API_PROFILE="$profile" KUBECONFIG="" "$BIN" adapter proxy \
    --platform-url "$PLATFORM_URL" \
    --runtime-url "${MCP_URL}/${ACME_SERVER}/mcp" \
    --server "$ACME_SERVER" \
    --namespace "$ACME_NS" \
    --agent "$agent_id" \
    --listen "$listen" \
    --auto-refresh >>"$log_file" 2>&1 &
  local proxy_pid=$!

  wait_for_adapter_proxy "$listen" "$log_file"

  local init_deadline=$(( $(date +%s) + 240 ))
  local init_status=""
  while true; do
    : >"$headers_file"
    : >"$init_body"
    init_status="$(
      curl -sS -D "$headers_file" -o "$init_body" -w '%{http_code}' \
        -H "content-type: application/json" \
        --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"multitenancytest","version":"0.1"}}}' \
        "$adapter_url" 2>/dev/null || true
    )"
    [[ "$init_status" =~ ^[0-9]{3}$ ]] || init_status="000"
    if [[ "$init_status" == "200" ]]; then
      break
    fi
    # Enrollment precedes operator reconciliation and the gateway's policy
    # reload. Wait for this new session, while other auth failures fail fast.
    if ! mcpruntime_adapter_initialize_pending "$init_status" "$init_body"; then
      echo "initialize returned HTTP ${init_status} for profile ${profile}" >&2
      cat "$init_body" >&2
      kill "$proxy_pid" >/dev/null 2>&1 || true
      stop_listen_port "$listen"
      return 1
    fi
    if [[ $(date +%s) -gt $init_deadline ]]; then
      echo "initialize timed out waiting for gateway route for profile ${profile}; last HTTP ${init_status}" >&2
      cat "$init_body" >&2
      kill "$proxy_pid" >/dev/null 2>&1 || true
      stop_listen_port "$listen"
      return 1
    fi
    echo "waiting for adapter route/session policy (HTTP ${init_status}) for profile ${profile}"
    sleep 3
  done

  local mcp_session_id
  mcp_session_id="$(awk 'BEGIN{IGNORECASE=1} /^Mcp-Session-Id:/ {gsub(/\r/,"",$2); print $2}' "$headers_file")"
  if [[ -z "$mcp_session_id" ]]; then
    echo "initialize did not return Mcp-Session-Id for profile ${profile}" >&2
    cat "$init_body" >&2
    kill "$proxy_pid" >/dev/null 2>&1 || true
    stop_listen_port "$listen"
    return 1
  fi

  sleep 10

  local status deadline wait_reason last_wait_log
  wait_reason=""
  last_wait_log=0
  status="$(
    curl -sS -o "$notify_body" -w '%{http_code}' \
      -H "content-type: application/json" \
      -H "Mcp-Session-Id: ${mcp_session_id}" \
      --data '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
      "$adapter_url"
  )"
  if [[ "$status" != "200" && "$status" != "202" && "$status" != "204" ]]; then
    echo "notifications/initialized returned HTTP ${status} for profile ${profile}" >&2
    cat "$notify_body" >&2
    kill "$proxy_pid" >/dev/null 2>&1 || true
    stop_listen_port "$listen"
    return 1
  fi

  deadline=$(( $(date +%s) + 180 ))
  while true; do
    status="$(
      curl -sS -o "$result_body" -w '%{http_code}' \
        -H "content-type: application/json" \
        -H "Mcp-Session-Id: ${mcp_session_id}" \
        --data "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":${arg_a},\"b\":${arg_b}}}}" \
        "$adapter_url"
    )"
    if [[ "$status" == "200" ]]; then
      break
    fi
    if grep -qE 'session_not_found|session_expired|no_matching_grant' "$result_body" 2>/dev/null; then
      local current_reason
      current_reason="$(jq -r '.error // empty' "$result_body" 2>/dev/null || true)"
      local now
      now="$(date +%s)"
      if [[ "$current_reason" != "$wait_reason" || $((now - last_wait_log)) -ge 15 ]]; then
        wait_reason="$current_reason"
        last_wait_log="$now"
        echo "waiting for gateway access policy for profile ${profile}: ${wait_reason:-pending}"
      fi
      if [[ "$now" -gt $deadline ]]; then
        echo "tools/call timed out waiting for gateway access policy for profile ${profile}" >&2
        cat "$result_body" >&2
        kill "$proxy_pid" >/dev/null 2>&1 || true
        stop_listen_port "$listen"
        return 1
      fi
      sleep 3
      continue
    fi
    echo "tools/call returned HTTP ${status} for profile ${profile}" >&2
    cat "$result_body" >&2
    kill "$proxy_pid" >/dev/null 2>&1 || true
    stop_listen_port "$listen"
    return 1
  done

  local result
  result="$(jq -er '.result.content[0].text' "$result_body")"
  if [[ "$result" != "$expected" ]]; then
    echo "adapter add result was ${result}, expected ${expected} for profile ${profile}" >&2
    cat "$result_body" >&2
    kill "$proxy_pid" >/dev/null 2>&1 || true
    stop_listen_port "$listen"
    return 1
  fi

  kill "$proxy_pid" >/dev/null 2>&1 || true
  stop_listen_port "$listen"
}

adapter_call_add() {
  adapter_call_add_for "$GLOBEX_PROFILE" "$ADAPTER_LISTEN" \
    "$WORK_DIR/adapter.log" "$WORK_DIR/adapter.headers" \
    "$WORK_DIR/adapter-init.body" "$WORK_DIR/adapter-notify.body" "$WORK_DIR/adapter-add.body" \
    7 9 16 "$GLOBEX_AGENT_ID"
}

verify_events() {
  local token
  token="$(profile_token "$ACME_PROFILE")"
  local body="$WORK_DIR/events.body"
  local deadline=$(( $(date +%s) + 60 ))
  while true; do
    curl -fsS -o "$body" \
      -H "x-api-key: ${token}" \
    -H "authorization: Bearer ${token}" \
      "${PLATFORM_URL}/api/v1/runtime/server-events?namespace=${ACME_NS}&server=${ACME_SERVER}&limit=10"
    if jq -e --arg globex "$GLOBEX_TEAM_ID" '
      (.events // .) as $events
      | ($events | length) > 0
      and any($events[]; (.tool_name // .ToolName // "") == "add" and
          ((.payload.subject_team_id // .subject_team_id // .team_id // "") == $globex)
          and (.decision // .Decision // "allow") == "allow")
    ' "$body" >/dev/null 2>&1; then
      return 0
    fi
    if [[ $(date +%s) -gt $deadline ]]; then
      echo "expected allow add event for globex team in server events" >&2
      cat "$body" >&2
      return 1
    fi
    sleep 3
  done
}

print_cursor_config() {
  cat <<JSON

HTTP adapter config:
MCP_PLATFORM_API_PROFILE=${GLOBEX_PROFILE} ${BIN} adapter proxy \\
  --platform-url ${PLATFORM_URL} \\
  --runtime-url ${MCP_URL}/${ACME_SERVER}/mcp \\
  --server ${ACME_SERVER} \\
  --namespace ${ACME_NS} \\
  --agent ${GLOBEX_AGENT_ID} \\
  --listen ${ADAPTER_LISTEN} \\
  --auto-refresh

{
  "mcpServers": {
    "${ACME_SERVER}": {
      "type": "http",
      "url": "http://${ADAPTER_LISTEN}"
    }
  }
}
JSON
}

need curl
need docker
need jq

if [[ ! -x "$BIN" ]]; then
  echo "missing binary: $BIN" >&2
  echo "build it first with: go build -o bin/mcp-runtime ./cmd/mcp-runtime" >&2
  exit 1
fi

mkdir -p "$MCP_RUNTIME_CONFIG_DIR" "$WORK_DIR"
stop_listen_port "$ADAPTER_LISTEN"
stop_listen_port "${TECHCORP_ADAPTER_LISTEN:-127.0.0.1:8300}"

if [[ "${SKIP_SETUP:-0}" != "1" ]]; then
  setup_demo
else
  admin_login
  team_user_login "$ACME_PROFILE" "$ACME_EMAIL" "$ACME_PASSWORD"
  team_user_login "$GLOBEX_PROFILE" "$GLOBEX_EMAIL" "$GLOBEX_PASSWORD"
  team_user_login "$TECHCORP_PROFILE" "$TECHCORP_EMAIL" "$TECHCORP_PASSWORD"
  ADMIN_TOKEN="$(profile_token "$ADMIN_PROFILE")"
  ACME_TEAM_ID="$(team_id "$ACME_SLUG" "$ADMIN_TOKEN")"
  GLOBEX_TEAM_ID="$(team_id "$GLOBEX_SLUG" "$ADMIN_TOKEN")"
  TECHCORP_TEAM_ID="$(team_id "$TECHCORP_SLUG" "$ADMIN_TOKEN")"
  setup_managed_agents
fi

wait_for_rollout "$ACME_PROFILE" "$ACME_NS" "$ACME_SERVER"
wait_for_rollout "$GLOBEX_PROFILE" "$GLOBEX_NS" "$GLOBEX_SERVER"
wait_for_rollout "$TECHCORP_PROFILE" "$TECHCORP_NS" "$TECHCORP_SERVER"
verify_grant_exists "$ACME_PROFILE" "${ACME_SERVER}-${GLOBEX_SLUG}-grant" "$ACME_NS"
verify_grant_exists "$ACME_PROFILE" "${ACME_SERVER}-${TECHCORP_SLUG}-grant" "$ACME_NS"

verify_no_kubeconfig_ops

delete_all_sessions "$ACME_PROFILE" "$ACME_NS"

wait_for_public_mcp_route
verify_direct_public_denied

echo "=== verify: ${GLOBEX_SLUG} adapter call to ${ACME_SLUG}/${ACME_SERVER} ==="
precreate_adapter_session "$GLOBEX_PROFILE" "$GLOBEX_AGENT_ID"
adapter_call_add
verify_events

echo "=== verify: ${TECHCORP_SLUG} adapter call to ${ACME_SLUG}/${ACME_SERVER} ==="
precreate_adapter_session "$TECHCORP_PROFILE" "$TECHCORP_AGENT_ID"
sleep 10
TECHCORP_ADAPTER_LISTEN="${TECHCORP_ADAPTER_LISTEN:-127.0.0.1:8300}"
adapter_call_add_for "$TECHCORP_PROFILE" "$TECHCORP_ADAPTER_LISTEN" \
  "$WORK_DIR/techcorp-adapter.log" "$WORK_DIR/techcorp-adapter.headers" \
  "$WORK_DIR/techcorp-init.body" "$WORK_DIR/techcorp-notify.body" "$WORK_DIR/techcorp-add.body" \
  11 22 33 "$TECHCORP_AGENT_ID"
echo "=== techcorp adapter call: OK (11+22=33) ==="

print_cursor_config
echo
echo "multi-tenant flow passed (platform API only, no kubectl/kubeconfig):"
echo "  - tenant image publish via server push platform API (not admin direct push)"
echo "  - ${GLOBEX_SLUG}/${GLOBEX_AGENT_ID} called ${ACME_SLUG}/${ACME_SERVER} add(7,9)=16 via adapter"
echo "  - ${TECHCORP_SLUG}/${TECHCORP_AGENT_ID} called ${ACME_SLUG}/${ACME_SERVER} add(11,22)=33 via adapter"
echo "  - admin credentials used only for team bootstrap; tenant flows used email/password login"
