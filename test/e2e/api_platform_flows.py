import base64
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

from _helpers_loader import load_e2e_helpers

_helpers = load_e2e_helpers()
check = _helpers.check
ok = _helpers.ok
fail = _helpers.fail

api_base = os.environ["API_BASE"]
gateway_api_base = os.environ["GATEWAY_API_BASE"]
api_key = os.environ["API_KEY"]
server_name = os.environ["SERVER_NAME"]
session_id = os.environ["SESSION_ID"]
human_id = os.environ["HUMAN_ID"]
agent_id = os.environ["AGENT_ID"]
platform_admin_email = os.environ["PLATFORM_ADMIN_EMAIL"]
platform_admin_password = os.environ["PLATFORM_ADMIN_PASSWORD"]
grant_name = f"{server_name}-grant"
test_user_password = "test-password-123"
check_multitenancy = os.environ.get("E2E_MULTITENANCY") == "1"


def request(url, *, method="GET", headers=None, body=None):
    headers = dict(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers.setdefault("content-type", "application/json")
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, dict(resp.headers.items()), resp.read().decode()
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers.items()), exc.read().decode()


def expect_status(url, status, *, method="GET", headers=None, body=None, contains=None):
    got_status, _, got_body = request(url, method=method, headers=headers, body=body)
    check(  
        got_status == status,
        f"{method} {url} returned {status}",
        f"{method} {url} returned {got_status}: {got_body}",
    )
    if contains:
        check(
            contains in got_body,
            f"{method} {url} contained {contains!r}",
            f"{method} {url} missing {contains!r}: {got_body}",
        )
    return got_body


def expect_json(url, status=200, *, method="GET", headers=None, body=None):
    return json.loads(
        expect_status(url, status, method=method, headers=headers, body=body)
    )


def expect_json_retry(
    url, status=200, *, method="GET", headers=None, body=None, tries=5, delay_s=2
):
    last_err = None
    for attempt in range(1, tries + 1):
        try:
            return expect_json(url, status, method=method, headers=headers, body=body)
        except AssertionError as exc:
            last_err = exc
            if attempt < tries:
                time.sleep(delay_s)
    if last_err is not None:
        raise last_err
    raise AssertionError(f"{method} {url} failed after {tries} attempts")


def bearer_headers(token):
    return {"Authorization": f"Bearer {token}"}


def basic_headers(username, password):
    token = base64.b64encode(f"{username}:{password}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def merged_headers(*items):
    out = {}
    for item in items:
        out.update(item)
    return out


def registry_forwarded_headers(path):
    # Traefik on the sentinel gateway overwrites X-Forwarded-Uri with the API
    # route (/api/v1/registry/authz). Keep the registry repository path on
    # X-Forwarded-URL so scope checks still see /v2/... through the gateway.
    return {"X-Forwarded-Uri": path, "X-Forwarded-URL": path}


def registry_authz_headers(*header_groups, repo_path):
    return merged_headers(*header_groups, registry_forwarded_headers(repo_path))


def quote_segment(value):
    return urllib.parse.quote(str(value), safe="")


suffix = str(int(time.time()))
admin_key_headers = {"x-api-key": api_key}

expect_status(
    f"{api_base}/auth/login",
    401,
    method="POST",
    body={"email": f"missing-{suffix}@mcpruntime.org", "password": "wrong-password"},
)
oidc_status, _, oidc_body = request(f"{api_base}/auth/oidc", method="POST", body={})
check(
    oidc_status in (400, 503),
    "POST /api/auth/oidc reached edge path",
    f"unexpected /api/auth/oidc status {oidc_status}: {oidc_body}",
)
expect_status(
    f"{api_base}/auth/signup",
    400,
    method="POST",
    body={
        "email": f"bad-role-{suffix}@mcpruntime.org",
        "password": test_user_password,
        "role": "root",
    },
)
expect_status(
    f"{api_base}/auth/signup",
    403,
    method="POST",
    body={
        "email": f"admin-denied-{suffix}@mcpruntime.org",
        "password": test_user_password,
        "role": "admin",
    },
)

admin_login = expect_json(
    f"{api_base}/auth/login",
    method="POST",
    body={"email": platform_admin_email, "password": platform_admin_password},
)
admin_token = admin_login.get("access_token", "")
check(
    bool(admin_token),
    "platform admin login returned access token",
    f"admin login response: {admin_login}",
)
admin_headers = bearer_headers(admin_token)
admin_me = expect_json(f"{api_base}/auth/me", headers=admin_headers)
check(
    admin_me.get("principal", {}).get("role") == "admin",
    "GET /api/auth/me returned admin principal",
    f"admin me response: {admin_me}",
)

signup_email = f"e2e-api-user-{suffix}@mcpruntime.org"
signup = expect_json(
    f"{api_base}/auth/signup",
    status=201,
    method="POST",
    body={"email": signup_email, "password": test_user_password},
)
user_token = signup.get("access_token", "")
user = signup.get("user", {})
user_id = user.get("id", "")
user_namespace = user.get("namespace", "")
check(
    bool(user_token and user_id),
    "POST /api/auth/signup created user token and id",
    f"signup response: {signup}",
)
user_headers = bearer_headers(user_token)
user_me = expect_json(f"{api_base}/auth/me", headers=user_headers)
check(
    user_me.get("principal", {}).get("email") == signup_email,
    "GET /api/auth/me returned signup user",
    f"user me response: {user_me}",
)

expect_json(f"{api_base}/user/api-keys", headers=user_headers)
created_user_key = expect_json(
    f"{api_base}/user/api-keys",
    method="POST",
    headers=user_headers,
    body={"name": f"e2e-key-{suffix}"},
)
user_key_id = created_user_key.get("key", {}).get("id", "")
user_api_key = created_user_key.get("api_key", "")
check(
    bool(user_key_id and user_api_key),
    "POST /api/user/api-keys created one-time key",
    f"user key response: {created_user_key}",
)
user_key_headers = {"x-api-key": user_api_key}
user_key_me = expect_json(f"{api_base}/auth/me", headers=user_key_headers)
check(
    user_key_me.get("principal", {}).get("email") == signup_email,
    "user API key authenticated /api/auth/me",
    f"user key me response: {user_key_me}",
)

expect_json(f"{api_base}/user/registry-credentials", headers=user_headers)
created_credential = expect_json(
    f"{api_base}/user/registry-credentials",
    status=201,
    method="POST",
    headers=user_headers,
    body={"name": f"e2e-registry-{suffix}"},
)
credential_id = created_credential.get("credential", {}).get("id", "")
registry_username = created_credential.get("username", "")
registry_password = created_credential.get("password", "")
check(
    bool(credential_id and registry_username and registry_password),
    "POST /api/user/registry-credentials returned credential",
    f"registry credential response: {created_credential}",
)

team_slug = f"e2e-api-{suffix}"
team_created = expect_json(
    f"{api_base}/runtime/teams",
    method="POST",
    headers=admin_headers,
    body={"slug": team_slug, "name": f"E2E API {suffix}"},
)
team = team_created.get("team", {})
team_namespace = team.get("namespace", "")
check(
    team.get("slug") == team_slug and bool(team_namespace),
    "POST /api/runtime/teams created managed team",
    f"team create response: {team_created}",
)
teams = expect_json(f"{api_base}/runtime/teams", headers=admin_headers)
check(
    team_slug in {item.get("slug") for item in teams.get("teams", [])},
    "GET /api/runtime/teams listed created team",
    f"created team missing: {teams}",
)
expect_json(
    f"{api_base}/runtime/teams/{quote_segment(team_slug)}", headers=admin_headers
)
expect_json(
    f"{api_base}/runtime/teams/{quote_segment(team_slug)}/members",
    headers=admin_headers,
)
membership = expect_json(
    f"{api_base}/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(user_id)}",
    method="PUT",
    headers=admin_headers,
    body={"role": "member"},
)
check(
    membership.get("membership", {}).get("user_id") == user_id,
    "PUT /api/runtime/teams/{slug}/members/{userID} added user",
    f"membership response: {membership}",
)
team_user_email = f"e2e-api-team-user-{suffix}@mcpruntime.org"
team_user = expect_json(
    f"{api_base}/users",
    status=201,
    method="POST",
    headers=admin_headers,
    body={"email": team_user_email, "password": test_user_password, "role": "user"},
)
team_user_id = team_user.get("user", {}).get("id", "")
check(
    bool(team_user_id),
    "POST /api/users created user",
    f"team user response: {team_user}",
)
expect_json(
    f"{api_base}/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(team_user_id)}",
    method="PUT",
    headers=admin_headers,
    body={"role": "owner"},
)
expect_json(
    f"{api_base}/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(team_user_id)}",
    method="DELETE",
    headers=admin_headers,
)

if check_multitenancy:
    other_team_slug = f"e2e-api-other-{suffix}"
    other_team = expect_json(
        f"{api_base}/runtime/teams",
        method="POST",
        headers=admin_headers,
        body={"slug": other_team_slug, "name": f"E2E API Other {suffix}"},
    ).get("team", {})
    other_team_namespace = other_team.get("namespace", "")
    check(
        other_team.get("slug") == other_team_slug and bool(other_team_namespace),
        "created a second managed team for isolation checks",
        f"second team response: {other_team}",
    )
    expect_json(
        f"{api_base}/runtime/teams/{quote_segment(other_team_slug)}/members/{quote_segment(team_user_id)}",
        method="PUT",
        headers=admin_headers,
        body={"role": "owner"},
    )
    first_login = expect_json(
        f"{api_base}/auth/login",
        method="POST",
        body={"email": signup_email, "password": test_user_password},
    )
    other_login = expect_json(
        f"{api_base}/auth/login",
        method="POST",
        body={"email": team_user_email, "password": test_user_password},
    )
    first_team_headers = bearer_headers(first_login["access_token"])
    other_team_headers = bearer_headers(other_login["access_token"])
    for own_slug, own_namespace, own_headers, foreign_slug, foreign_namespace in (
        (team_slug, team_namespace, first_team_headers, other_team_slug, other_team_namespace),
        (other_team_slug, other_team_namespace, other_team_headers, team_slug, team_namespace),
    ):
        listed_teams = expect_json(f"{api_base}/runtime/teams", headers=own_headers)
        visible_slugs = {item.get("slug") for item in listed_teams.get("teams", [])}
        check(
            own_slug in visible_slugs and foreign_slug not in visible_slugs,
            f"{own_slug} sees only its own team",
            f"visible teams: {listed_teams}",
        )
        expect_json(
            f"{api_base}/runtime/teams/{quote_segment(own_slug)}",
            headers=own_headers,
        )
        expect_status(
            f"{api_base}/runtime/teams/{quote_segment(foreign_slug)}",
            403,
            headers=own_headers,
        )
        expect_json(
            f"{api_base}/runtime/namespaces/{quote_segment(own_namespace)}",
            headers=own_headers,
        )
        expect_status(
            f"{api_base}/runtime/namespaces/{quote_segment(foreign_namespace)}",
            403,
            headers=own_headers,
        )

namespaces = expect_json(f"{api_base}/runtime/namespaces", headers=admin_headers)
check(
    team_namespace
    in {item.get("namespace") for item in namespaces.get("namespaces", [])},
    "GET /api/runtime/namespaces listed team namespace",
    f"team namespace missing: {namespaces}",
)
namespace_item = expect_json(
    f"{api_base}/runtime/namespaces/{quote_segment(team_namespace)}",
    headers=admin_headers,
)
check(
    namespace_item.get("namespace", {}).get("namespace") == team_namespace,
    "GET /api/runtime/namespaces/{namespace} returned team namespace",
    f"namespace item response: {namespace_item}",
)

expect_status(
    f"{api_base}/registry/authz",
    401,
    headers=registry_authz_headers(repo_path="/v2/_catalog"),
)
expect_status(
    f"{api_base}/registry/authz",
    204,
    headers=registry_authz_headers(
        admin_key_headers, repo_path=f"/v2/{team_slug}/demo/manifests/latest"
    ),
)
personal_scope = user_namespace or registry_username
if personal_scope:
    expect_status(
        f"{api_base}/registry/authz",
        403,
        headers=registry_authz_headers(
            user_key_headers,
            repo_path=f"/v2/{personal_scope}/demo/manifests/latest",
        ),
    )
registry_basic_headers = basic_headers(registry_username, registry_password)
expect_status(
    f"{api_base}/registry/authz",
    204,
    headers=registry_authz_headers(
        registry_basic_headers,
        repo_path=f"/v2/{team_slug}/demo/manifests/latest",
    ),
)
expect_status(
    f"{api_base}/registry/authz",
    204,
    headers=registry_authz_headers(
        registry_basic_headers,
        repo_path=f"/v2/{team_namespace}/demo/manifests/latest",
    ),
)
if check_multitenancy:
    other_credential = expect_json(
        f"{api_base}/user/registry-credentials",
        status=201,
        method="POST",
        headers=other_team_headers,
        body={"name": f"e2e-registry-other-{suffix}"},
    )
    other_registry_headers = basic_headers(
        other_credential["username"], other_credential["password"]
    )
    for own_slug, own_headers, foreign_slug in (
        (team_slug, registry_basic_headers, other_team_slug),
        (other_team_slug, other_registry_headers, team_slug),
    ):
        expect_status(
            f"{api_base}/registry/authz",
            204,
            headers=registry_authz_headers(
                own_headers, repo_path=f"/v2/{own_slug}/demo/manifests/latest"
            ),
        )
        expect_status(
            f"{api_base}/registry/authz",
            403,
            headers=registry_authz_headers(
                own_headers, repo_path=f"/v2/{foreign_slug}/demo/manifests/latest"
            ),
        )

expect_json(f"{api_base}/analytics/usage?limit=3", headers=admin_key_headers)
expect_json(f"{api_base}/user/analytics/usage?limit=3", headers=user_headers)
expect_json(
    f"{api_base}/user/activity/image-publish",
    status=202,
    method="POST",
    headers=user_headers,
    body={
        "image_ref": f"registry.registry.svc.cluster.local:5000/{team_slug}/demo:{suffix}",
        "source_image": "docker.io/library/nginx:1.27-alpine",
        "mode": "pr-api-platform",
    },
)

server_item = expect_json(
    f"{api_base}/runtime/servers/mcp-servers/{quote_segment(server_name)}",
    headers=admin_key_headers,
)
check(
    server_item.get("server", {}).get("name") == server_name,
    "GET /api/runtime/servers/{namespace}/{name} returned server",
    f"server item response: {server_item}",
)
server_events = expect_json(
    f"{api_base}/runtime/server-events?namespace=mcp-servers&server={urllib.parse.quote(server_name)}&limit=5",
    headers=admin_key_headers,
)
check(
    "events" in server_events,
    "GET /api/runtime/server-events returned events key",
    f"server events response: {server_events}",
)

api_runtime_grant = f"{server_name}-e2e-api-grant-{suffix}"
api_runtime_session = f"{server_name}-e2e-api-session-{suffix}"
runtime_namespace = expect_json(
    f"{api_base}/runtime/namespaces/mcp-servers", headers=admin_key_headers
).get("namespace", {})
api_runtime_subject = {"humanID": human_id}
runtime_team_slug = runtime_namespace.get("team_slug", "")
runtime_team_id = runtime_namespace.get("team_id", "")
if runtime_team_slug and runtime_team_id:
    directory_agent = expect_json(
        f"{api_base}/runtime/teams/{quote_segment(runtime_team_slug)}/agents",
        status=201,
        method="POST",
        headers=admin_headers,
        body={"name": f"API flow agent {suffix}"},
    ).get("agent", {})
    check(
        bool(directory_agent.get("id"))
        and directory_agent.get("team_id") == runtime_team_id,
        "created runtime API test agent in the server namespace team",
        f"agent response: {directory_agent}",
    )
    api_runtime_subject.update(
        {
            "agentID": directory_agent["id"],
            "teamID": runtime_team_id,
        }
    )
expect_json(
    f"{api_base}/runtime/grants",
    method="POST",
    headers=admin_key_headers,
    body={
        "name": api_runtime_grant,
        "namespace": "mcp-servers",
        "serverRef": {"name": server_name, "namespace": "mcp-servers"},
        "subject": api_runtime_subject,
        "maxTrust": "low",
        "allowedSideEffects": ["read"],
        "toolRules": [{"name": "add", "decision": "allow", "requiredTrust": "low"}],
    },
)
expect_json(
    f"{api_base}/runtime/sessions",
    method="POST",
    headers=admin_key_headers,
    body={
        "name": api_runtime_session,
        "namespace": "mcp-servers",
        "serverRef": {"name": server_name, "namespace": "mcp-servers"},
        "subject": api_runtime_subject,
        "consentedTrust": "low",
    },
)
expect_json(
    f"{api_base}/runtime/grants/mcp-servers/{quote_segment(api_runtime_grant)}",
    headers=admin_key_headers,
)
expect_json(
    f"{api_base}/runtime/sessions/mcp-servers/{quote_segment(api_runtime_session)}",
    headers=admin_key_headers,
)
expect_json(
    f"{api_base}/runtime/sessions/mcp-servers/{quote_segment(api_runtime_session)}",
    method="DELETE",
    headers=admin_key_headers,
)
expect_json(
    f"{api_base}/runtime/grants/mcp-servers/{quote_segment(api_runtime_grant)}",
    method="DELETE",
    headers=admin_key_headers,
)

temp_server_name = f"e2e-api-server-{suffix}"
temp_server = expect_json(
    f"{api_base}/runtime/servers",
    method="POST",
    headers=admin_headers,
    body={
        "name": temp_server_name,
        "namespace": "mcp-servers",
        "spec": {
            "image": "docker.io/library/nginx:1.27-alpine",
            "port": 8080,
            "servicePort": 80,
            "publicPathPrefix": temp_server_name,
            "ingressPath": f"/{temp_server_name}/mcp",
            "gateway": {"enabled": False},
            "analytics": {"disabled": True},
        },
    },
)
check(
    temp_server.get("server", {}).get("name") == temp_server_name,
    "POST /api/runtime/servers created temporary server",
    f"temporary server response: {temp_server}",
)
expect_json(
    f"{api_base}/runtime/servers/mcp-servers/{quote_segment(temp_server_name)}",
    headers=admin_headers,
)
expect_json(
    f"{api_base}/runtime/servers/mcp-servers/{quote_segment(temp_server_name)}",
    method="DELETE",
    headers=admin_headers,
)

deployment_name = f"e2e-api-deploy-{suffix}"
expect_json(
    f"{api_base}/deployments?namespace={quote_segment(team_namespace)}",
    headers=admin_headers,
)
deployment = expect_json_retry(
    f"{api_base}/deployments",
    method="POST",
    headers=admin_headers,
    body={
        "name": deployment_name,
        "namespace": team_namespace,
        "image": "docker.io/library/nginx:1.27-alpine",
        "port": 8080,
        "replicas": 1,
    },
)
check(
    deployment.get("deployment", {}).get("name") == deployment_name,
    "POST /api/deployments created deployment",
    f"deployment response: {deployment}",
)
expect_json(
    f"{api_base}/admin/deployments?namespace={quote_segment(team_namespace)}",
    headers=admin_headers,
)
expect_json(
    f"{api_base}/deployments/{quote_segment(team_namespace)}/{quote_segment(deployment_name)}",
    method="DELETE",
    headers=admin_headers,
)

admin_namespaces = expect_json(f"{api_base}/admin/namespaces", headers=admin_headers)
check(
    "namespaces" in admin_namespaces,
    "GET /api/admin/namespaces returned namespaces",
    f"admin namespaces response: {admin_namespaces}",
)
admin_audit = expect_json(f"{api_base}/admin/audit?limit=5", headers=admin_headers)
check(
    "audit_logs" in admin_audit,
    "GET /api/admin/audit returned audit logs",
    f"admin audit response: {admin_audit}",
)
admin_operations = expect_json(
    f"{api_base}/admin/operations?limit=5", headers=admin_headers
)
check(
    "audit_logs" in admin_operations and "users" in admin_operations,
    "GET /api/admin/operations returned operations payload",
    f"admin operations response: {admin_operations}",
)

gateway_summary = expect_json(
    f"{gateway_api_base}/dashboard/summary", headers=admin_key_headers
)
check(
    "total_events" in gateway_summary,
    "gateway /api/dashboard/summary returned summary",
    f"gateway summary response: {gateway_summary}",
)

expect_json(
    f"{api_base}/user/registry-credentials/{quote_segment(credential_id)}",
    method="DELETE",
    headers=user_headers,
)
expect_status(
    f"{api_base}/registry/authz",
    401,
    headers=registry_authz_headers(
        registry_basic_headers,
        repo_path=f"/v2/{team_slug}/demo/manifests/latest",
    ),
)
expect_json(
    f"{api_base}/user/api-keys/{quote_segment(user_key_id)}",
    method="DELETE",
    headers=user_headers,
)
expect_status(f"{api_base}/auth/me", 401, headers=user_key_headers)

print("api-platform request routes:")
for route in (
    "api:/api/auth/login",
    "api:/api/auth/oidc",
    "api:/api/auth/signup",
    "api:/api/auth/me",
    "api:/api/registry/authz",
    "api:/api/analytics/usage",
    "api:/api/user/analytics/usage",
    "api:/api/user/api-keys",
    "api:/api/user/api-keys/{id}",
    "api:/api/user/registry-credentials",
    "api:/api/user/registry-credentials/{id}",
    "api:/api/user/activity/image-publish",
    "api:/api/runtime/servers/{namespace}/{name}",
    "api:/api/runtime/server-events",
    "api:/api/runtime/teams",
    "api:/api/runtime/teams/{slug}",
    "api:/api/runtime/teams/{slug}/members/{userID}",
    "api:/api/users",
    "api:/api/runtime/namespaces",
    "api:/api/runtime/namespaces/{namespace}",
    "api:/api/runtime/grants/{namespace}/{name}",
    "api:/api/runtime/sessions/{namespace}/{name}",
    "api:/api/deployments",
    "api:/api/deployments/{namespace}/{name}",
    "api:/api/admin/namespaces",
    "api:/api/admin/audit",
    "api:/api/admin/operations",
    "api:/api/admin/deployments",
):
    print(f"  {route}")
