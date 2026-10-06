import base64
import json
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request

from _helpers_loader import load_e2e_helpers

_helpers = load_e2e_helpers()
check = _helpers.check
ok = _helpers.ok
fail = _helpers.fail

gateway_base = os.environ["PLATFORM_GATEWAY_BASE"]
api_base = os.environ["PLATFORM_API_BASE"]
api_metrics_url = os.environ["PLATFORM_API_METRICS_URL"]
ingest_base = os.environ["PLATFORM_INGEST_BASE"]
ingest_metrics_url = os.environ["PLATFORM_INGEST_METRICS_URL"]
processor_base = os.environ["PLATFORM_PROCESSOR_BASE"]
ui_base = os.environ["PLATFORM_UI_BASE"]
server_proxy_base = os.environ["SERVER_PROXY_BASE"]
server_upstream_base = os.environ["SERVER_UPSTREAM_BASE"]
oauth_proxy_base = os.environ["OAUTH_PROXY_BASE"]
oauth_upstream_base = os.environ["OAUTH_UPSTREAM_BASE"]
api_key = os.environ["API_KEY"]
ingest_api_key = os.environ["INGEST_API_KEY"]
server_name = os.environ["SERVER_NAME"]
server_host = os.environ["SERVER_HOST"]
session_id = os.environ["SESSION_ID"]
human_id = os.environ["HUMAN_ID"]
agent_id = os.environ["AGENT_ID"]
oauth_server_name = os.environ["OAUTH_SERVER_NAME"]
oauth_server_host = os.environ["OAUTH_SERVER_HOST"]
oauth_issuer_url = os.environ["OAUTH_ISSUER_URL"]
oauth_valid_token = os.environ["OAUTH_VALID_TOKEN"]
protocol = os.environ["MCP_PROTOCOL_VERSION"]
platform_mode = os.environ["E2E_PLATFORM_MODE"]
deep_request_flows = os.environ.get("E2E_DEEP_REQUEST_FLOWS", "").lower() in {
    "1",
    "true",
    "yes",
    "on",
}
platform_admin_email = os.environ["PLATFORM_ADMIN_EMAIL"]
platform_admin_password = os.environ["PLATFORM_ADMIN_PASSWORD"]
grant_name = f"{server_name}-grant"
oauth_public_base = f"http://{oauth_server_host}"
server_mcp_path = f"/{server_name}/mcp"
oauth_mcp_path = f"/{oauth_server_name}/mcp"
catalog_namespace = "mcp-servers"
test_user_password = "test-password-123"


def request(url, *, method="GET", headers=None, body=None):
    headers = dict(headers or {})
    data = None
    if body is not None:
        if isinstance(body, (bytes, bytearray)):
            data = bytes(body)
        else:
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
    payload = expect_status(url, status, method=method, headers=headers, body=body)
    return json.loads(payload)


def check_vite_assets(base, label, index_html):
    scripts = re.findall(r'<script[^>]+src="([^"]+)"', index_html)
    styles = re.findall(r'<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"', index_html)
    script_path = next((path for path in scripts if path.startswith("/assets/")), "")
    style_path = next((path for path in styles if path.startswith("/assets/")), "")
    check(script_path, f"{label} index references Vite JS asset", f"{label} index missing Vite JS asset: {index_html}")
    check(style_path, f"{label} index references Vite CSS asset", f"{label} index missing Vite CSS asset: {index_html}")
    expect_status(f"{base}{script_path}", 200, contains="/config.js")
    expect_status(f"{base}{style_path}", 200, contains="--canvas:")
    return script_path, style_path


def wait_for_json(
    url, predicate, *, headers=None, retries=60, delay=2, description="response"
):
    last = None
    for _ in range(retries):
        last = expect_json(url, headers=headers)
        if predicate(last):
            ok(f"waited for {description}")
            return last
        time.sleep(delay)
    fail(f"timed out waiting for {description}: {json.dumps(last, indent=2)}")


def expect_mcp_initialize(url, *, headers=None, status=200, contains=None):
    req_headers = {
        "accept": "application/json, text/event-stream",
        "content-type": "application/json",
        "Mcp-Protocol-Version": protocol,
    }
    req_headers.update(headers or {})
    got_status, got_headers, got_body = request(
        url,
        method="POST",
        headers=req_headers,
        body={
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": protocol,
                "capabilities": {},
                "clientInfo": {"name": "mcp-runtime-e2e", "version": "1.0.0"},
            },
        },
    )
    check(
        got_status == status,
        f"POST {url} initialize returned {status}",
        f"POST {url} initialize returned {got_status}: {got_body}",
    )
    if contains:
        check(
            contains in got_body,
            f"POST {url} initialize contained {contains!r}",
            f"POST {url} initialize missing {contains!r}: {got_body}",
        )
    if got_status == 200:
        doc = json.loads(got_body)
        check(
            "result" in doc,
            f"POST {url} initialize returned result",
            f"POST {url} initialize missing result: {doc}",
        )
        header_map = {k.lower(): v for k, v in got_headers.items()}
        check(
            "mcp-session-id" in header_map,
            f"POST {url} initialize returned Mcp-Session-Id",
            f"POST {url} initialize missing Mcp-Session-Id: {got_headers}",
        )
    return got_body


auth_headers = {"x-api-key": api_key}
ingest_headers = {"x-api-key": ingest_api_key}

# Gateway-routed UI, API, and example MCP routes.
gateway_summary = expect_json(
    f"{gateway_base}/api/v1/dashboard/summary", headers=auth_headers
)
for key in ("total_events", "active_servers", "active_grants", "active_sessions"):
    check(
        key in gateway_summary,
        f"gateway dashboard summary contains {key}",
        f"gateway dashboard summary missing {key}: {gateway_summary}",
    )
expect_status(f"{gateway_base}/ping", 200, contains="OK")
gateway_index = expect_status(
    f"{gateway_base}/", 200, contains="MCP Runtime Control Plane"
)
gateway_config = expect_status(
    f"{gateway_base}/config.js", 200, contains="window.MCP_API_BASE"
)
check(
    f'window.MCP_PLATFORM_MODE = "{platform_mode}"' in gateway_config,
    f"gateway config.js exposes platform mode {platform_mode}",
    f"gateway config.js missing platform mode {platform_mode}: {gateway_config}",
)
gateway_script_path, gateway_style_path = check_vite_assets(gateway_base, "gateway", gateway_index)
expect_status(f"{gateway_base}/grafana/api/health", 401)
expect_status(f"{gateway_base}/prometheus/-/healthy", 404)
expect_status(
    f"{gateway_base}/grafana/api/health", 200, headers=auth_headers, contains="database"
)
expect_status(f"{gateway_base}/prometheus/-/healthy", 404, headers=auth_headers)

# Direct UI service.
expect_status(f"{ui_base}/health", 200, contains='"ok":true')
ui_index = expect_status(f"{ui_base}/", 200, contains="MCP Runtime Control Plane")
ui_config = expect_status(f"{ui_base}/config.js", 200, contains="window.MCP_API_BASE")
check(
    f'window.MCP_PLATFORM_MODE = "{platform_mode}"' in ui_config,
    f"ui config.js exposes platform mode {platform_mode}",
    f"ui config.js missing platform mode {platform_mode}: {ui_config}",
)
ui_script_path, ui_style_path = check_vite_assets(ui_base, "ui", ui_index)

# Direct MCP proxy and upstream server surfaces.
expect_status(f"{server_proxy_base}/health", 200, contains="ok")
expect_status(f"{server_upstream_base}/health", 200, contains='"ok":true')
expect_mcp_initialize(f"{server_upstream_base}{server_mcp_path}")

expect_status(f"{oauth_proxy_base}/health", 200, contains="ok")
oauth_metadata = expect_json(f"{oauth_proxy_base}/.well-known/oauth-protected-resource")
check(
    oauth_metadata.get("authorization_servers") == [oauth_issuer_url],
    "oauth proxy metadata authorization_servers matched issuer",
    f"unexpected oauth metadata authorization servers: {oauth_metadata}",
)
check(
    oauth_metadata.get("bearer_methods_supported") == ["header"],
    "oauth proxy metadata bearer_methods_supported matched",
    f"unexpected oauth metadata bearer methods: {oauth_metadata}",
)
oauth_resource_url = oauth_metadata.get("resource", "")
oauth_resource_path = urllib.parse.urlsplit(oauth_resource_url).path or "/"
check(
    oauth_resource_path == "/",
    "oauth proxy metadata root resource path matched",
    f"unexpected oauth metadata resource URL: {oauth_metadata}",
)
oauth_metadata_path = expect_json(
    f"{oauth_proxy_base}/.well-known/oauth-protected-resource/{oauth_server_name}/mcp"
)
oauth_resource_path_url = oauth_metadata_path.get("resource", "")
oauth_resource_path_value = urllib.parse.urlsplit(oauth_resource_path_url).path
check(
    oauth_resource_path_value == f"/{oauth_server_name}/mcp",
    "oauth proxy metadata path resource matched",
    f"unexpected oauth metadata path resource URL: {oauth_metadata_path}",
)
expect_mcp_initialize(
    f"{oauth_proxy_base}{oauth_mcp_path}",
    headers={"Authorization": f"Bearer {oauth_valid_token}"},
)
expect_status(f"{oauth_upstream_base}/health", 200, contains='"ok":true')
expect_mcp_initialize(f"{oauth_upstream_base}{oauth_mcp_path}")

# API service surfaces.
expect_status(f"{api_base}/health", 200, contains='"ok":true')
expect_status(api_metrics_url, 200, contains="# HELP")
events = expect_json(f"{api_base}/api/v1/events?limit=5", headers=auth_headers)
check(
    bool(events.get("events")),
    "api /api/events returned events",
    f"expected /api/events to return events: {events}",
)
stats = expect_json(f"{api_base}/api/v1/stats", headers=auth_headers)
check(
    int(stats.get("events_total", 0)) >= 1,
    "api /api/stats events_total >= 1",
    f"expected /api/stats events_total >= 1: {stats}",
)
sources = expect_json(f"{api_base}/api/v1/sources", headers=auth_headers)
check(
    bool(sources.get("sources")),
    "api /api/sources returned sources",
    f"expected /api/sources to return sources: {sources}",
)
event_types = expect_json(f"{api_base}/api/v1/event-types", headers=auth_headers)
check(
    bool(event_types.get("event_types")),
    "api /api/event-types returned event types",
    f"expected /api/event-types to return event types: {event_types}",
)
filtered = wait_for_json(
    f"{api_base}/api/v1/events?server={urllib.parse.quote(server_name)}&limit=5",
    lambda doc: bool(doc.get("events")),
    headers=auth_headers,
    description="api /api/v1/events filtered events",
)
check(
    bool(filtered.get("events")),
    "api /api/v1/events returned filtered events",
    f"expected /api/v1/events to return events: {filtered}",
)
summary = expect_json(f"{api_base}/api/v1/dashboard/summary", headers=auth_headers)
for key in ("total_events", "active_servers", "active_grants", "active_sessions"):
    check(
        key in summary,
        f"api dashboard summary contains {key}",
        f"dashboard summary missing {key}: {summary}",
    )
servers = expect_json(
    f"{api_base}/api/v1/runtime/servers?namespace={urllib.parse.quote(catalog_namespace)}",
    headers=auth_headers,
)
server_names = {item.get("name") for item in servers.get("servers", [])}
check(
    server_name in server_names and oauth_server_name in server_names,
    "runtime servers contain expected entries",
    f"runtime servers missing expected entries: {servers}",
)
grants = expect_json(f"{api_base}/api/v1/runtime/grants", headers=auth_headers)
grant_names = {item.get("name") for item in grants.get("grants", [])}
check(
    grant_name in grant_names,
    f"runtime grants contain {grant_name}",
    f"runtime grants missing {grant_name}: {grants}",
)
sessions = expect_json(f"{api_base}/api/v1/runtime/sessions", headers=auth_headers)
session_names = {item.get("name") for item in sessions.get("sessions", [])}
check(
    session_id in session_names,
    f"runtime sessions contain {session_id}",
    f"runtime sessions missing {session_id}: {sessions}",
)
not_a_server = f"{server_name}-e2e-not-mcpserver"
bad_grant_body = expect_status(
    f"{api_base}/api/v1/runtime/grants",
    400,
    method="POST",
    headers=auth_headers,
    body={
        "name": f"{server_name}-e2e-bad-grant",
        "namespace": "mcp-servers",
        "serverRef": {"name": not_a_server, "namespace": "mcp-servers"},
        "subject": {"humanID": human_id, "agentID": agent_id},
        "maxTrust": "low",
        "allowedSideEffects": ["read"],
        "toolRules": [{"name": "add", "decision": "allow", "requiredTrust": "low"}],
    },
)
check(
    "unknown serverRef" in bad_grant_body,
    "POST /api/runtime/grants rejects unknown serverRef",
    f"body: {bad_grant_body}",
)
bad_grant_side_effect_body = expect_status(
    f"{api_base}/api/v1/runtime/grants",
    400,
    method="POST",
    headers=auth_headers,
    body={
        "name": f"{server_name}-e2e-bad-side-effect-grant",
        "namespace": "mcp-servers",
        "serverRef": {"name": server_name, "namespace": "mcp-servers"},
        "subject": {"humanID": human_id, "agentID": agent_id},
        "maxTrust": "low",
        "toolRules": [{"name": "add", "decision": "allow", "requiredTrust": "low"}],
    },
)
check(
    "allowed side effect" in bad_grant_side_effect_body,
    "POST /api/runtime/grants rejects missing allowed side effects",
    f"body: {bad_grant_side_effect_body}",
)
bad_session_body = expect_status(
    f"{api_base}/api/v1/runtime/sessions",
    400,
    method="POST",
    headers=auth_headers,
    body={
        "name": f"{server_name}-e2e-bad-session",
        "namespace": "mcp-servers",
        "serverRef": {"name": not_a_server, "namespace": "mcp-servers"},
        "subject": {"humanID": human_id, "agentID": agent_id},
        "consentedTrust": "low",
    },
)
check(
    "unknown serverRef" in bad_session_body,
    "POST /api/runtime/sessions rejects unknown serverRef",
    f"body: {bad_session_body}",
)
api_runtime_grant = f"{server_name}-e2e-api-grant"
api_runtime_session = f"{server_name}-e2e-api-session"
created_grant = expect_json(
    f"{api_base}/api/v1/runtime/grants",
    method="POST",
    headers=auth_headers,
    body={
        "name": api_runtime_grant,
        "namespace": "mcp-servers",
        "serverRef": {"name": server_name, "namespace": "mcp-servers"},
        "subject": {"humanID": human_id, "agentID": agent_id},
        "maxTrust": "low",
        "allowedSideEffects": ["read"],
        "toolRules": [{"name": "add", "decision": "allow", "requiredTrust": "low"}],
    },
)
check(
    created_grant.get("grant", {}).get("name") == api_runtime_grant,
    "POST /api/runtime/grants created grant",
    f"body: {created_grant}",
)
created_session = expect_json(
    f"{api_base}/api/v1/runtime/sessions",
    method="POST",
    headers=auth_headers,
    body={
        "name": api_runtime_session,
        "namespace": "mcp-servers",
        "serverRef": {"name": server_name, "namespace": "mcp-servers"},
        "subject": {"humanID": human_id, "agentID": agent_id},
        "consentedTrust": "low",
    },
)
check(
    created_session.get("session", {}).get("name") == api_runtime_session,
    "POST /api/runtime/sessions created session",
    f"body: {created_session}",
)
grants_after = expect_json(f"{api_base}/api/v1/runtime/grants", headers=auth_headers)
grant_names_after = {item.get("name") for item in grants_after.get("grants", [])}
check(
    api_runtime_grant in grant_names_after,
    "list grants after API create",
    f"missing {api_runtime_grant}: {grants_after}",
)
sessions_after = expect_json(
    f"{api_base}/api/v1/runtime/sessions", headers=auth_headers
)
session_names_after = {item.get("name") for item in sessions_after.get("sessions", [])}
check(
    api_runtime_session in session_names_after,
    "list sessions after API create",
    f"missing {api_runtime_session}: {sessions_after}",
)
components = expect_json(f"{api_base}/api/v1/runtime/components", headers=auth_headers)
component_keys = {item.get("key") for item in components.get("components", [])}
check(
    {"api", "gateway", "ui"}.issubset(component_keys),
    "runtime components contain api/gateway/ui",
    f"runtime components missing expected keys: {components}",
)
policy = expect_json(
    f"{api_base}/api/v1/runtime/policy?namespace=mcp-servers&server={urllib.parse.quote(server_name)}",
    headers=auth_headers,
)
check(
    policy.get("server", {}).get("name") == server_name,
    f"runtime policy resolved server {server_name}",
    f"runtime policy missing server {server_name}: {policy}",
)

# Runtime mutation paths through the API.
disable = expect_json(
    f"{api_base}/api/v1/runtime/grants/mcp-servers/{urllib.parse.quote(grant_name)}",
    method="PATCH",
    headers=auth_headers,
    body={"disabled": True},
)
check(
    disable.get("disabled") is True,
    "grant disable response marked disabled=true",
    f"grant disable response unexpected: {disable}",
)
enable = expect_json(
    f"{api_base}/api/v1/runtime/grants/mcp-servers/{urllib.parse.quote(grant_name)}",
    method="PATCH",
    headers=auth_headers,
    body={"disabled": False},
)
check(
    enable.get("disabled") is False,
    "grant enable response marked disabled=false",
    f"grant enable response unexpected: {enable}",
)
revoke = expect_json(
    f"{api_base}/api/v1/runtime/sessions/mcp-servers/{urllib.parse.quote(session_id)}",
    method="PATCH",
    headers=auth_headers,
    body={"revoked": True},
)
check(
    revoke.get("revoked") is True,
    "session revoke response marked revoked=true",
    f"session revoke response unexpected: {revoke}",
)
unrevoke = expect_json(
    f"{api_base}/api/v1/runtime/sessions/mcp-servers/{urllib.parse.quote(session_id)}",
    method="PATCH",
    headers=auth_headers,
    body={"revoked": False},
)
check(
    unrevoke.get("revoked") is False,
    "session unrevoke response marked revoked=false",
    f"session unrevoke response unexpected: {unrevoke}",
)
expect_json(
    f"{api_base}/api/v1/runtime/actions/restart",
    status=400,
    method="POST",
    headers=auth_headers,
    body={"component": "definitely-not-a-real-component"},
)

if deep_request_flows:

    def bearer_headers(token):
        return {"Authorization": f"Bearer {token}"}

    def merged_headers(*items):
        out = {}
        for item in items:
            out.update(item)
        return out

    def basic_headers(username, password):
        token = base64.b64encode(f"{username}:{password}".encode()).decode()
        return {"Authorization": f"Basic {token}"}

    def registry_forwarded_headers(path):
        # Traefik on the platform gateway overwrites X-Forwarded-Uri with the API
        # route (/api/v1/registry/authz). Keep the registry repository path on
        # X-Forwarded-URL so scope checks still see /v2/... through the gateway.
        return {"X-Forwarded-Uri": path, "X-Forwarded-URL": path}

    def registry_authz_headers(*header_groups, repo_path):
        return merged_headers(*header_groups, registry_forwarded_headers(repo_path))

    def quote_segment(value):
        return urllib.parse.quote(str(value), safe="")

    suffix = str(int(time.time()))
    expect_status(
        f"{api_base}/api/v1/auth/login",
        401,
        method="POST",
        body={
            "email": f"missing-{suffix}@mcpruntime.org",
            "password": "wrong-password",
        },
    )
    oidc_status, _, oidc_body = request(
        f"{api_base}/api/v1/auth/oidc",
        method="POST",
        body={},
    )
    check(
        oidc_status in (400, 503),
        "POST /api/auth/oidc reached missing-token or not-configured edge path",
        f"unexpected /api/auth/oidc status {oidc_status}: {oidc_body}",
    )
    expect_status(
        f"{api_base}/api/v1/auth/signup",
        400,
        method="POST",
        body={
            "email": f"bad-role-{suffix}@mcpruntime.org",
            "password": test_user_password,
            "role": "root",
        },
    )
    expect_status(
        f"{api_base}/api/v1/auth/signup",
        403,
        method="POST",
        body={
            "email": f"admin-denied-{suffix}@mcpruntime.org",
            "password": test_user_password,
            "role": "admin",
        },
    )

    admin_login = expect_json(
        f"{api_base}/api/v1/auth/login",
        method="POST",
        body={"email": platform_admin_email, "password": platform_admin_password},
    )
    admin_token = admin_login.get("access_token", "")
    check(
        bool(admin_token),
        "platform admin login returned access_token",
        f"admin login missing token: {admin_login}",
    )
    admin_headers = bearer_headers(admin_token)
    admin_me = expect_json(f"{api_base}/api/v1/auth/me", headers=admin_headers)
    check(
        admin_me.get("principal", {}).get("role") == "admin",
        "GET /api/auth/me returned admin principal",
        f"unexpected admin /api/auth/me response: {admin_me}",
    )

    signup_email = f"e2e-user-{suffix}@mcpruntime.org"
    signup = expect_json(
        f"{api_base}/api/v1/auth/signup",
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
    user_me = expect_json(f"{api_base}/api/v1/auth/me", headers=user_headers)
    check(
        user_me.get("principal", {}).get("email") == signup_email,
        "GET /api/auth/me returned signup user principal",
        f"unexpected signup /api/auth/me response: {user_me}",
    )

    expect_json(f"{api_base}/api/v1/user/api-keys", headers=user_headers)
    created_user_key = expect_json(
        f"{api_base}/api/v1/user/api-keys",
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
    user_key_me = expect_json(f"{api_base}/api/v1/auth/me", headers=user_key_headers)
    check(
        user_key_me.get("principal", {}).get("email") == signup_email,
        "user API key authenticated /api/auth/me",
        f"unexpected user API key principal: {user_key_me}",
    )

    expect_json(f"{api_base}/api/v1/user/registry-credentials", headers=user_headers)
    created_credential = expect_json(
        f"{api_base}/api/v1/user/registry-credentials",
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
        "POST /api/user/registry-credentials returned registry credential",
        f"registry credential response: {created_credential}",
    )

    team_slug = f"e2e-deep-{suffix}"
    team_created = expect_json(
        f"{api_base}/api/v1/runtime/teams",
        method="POST",
        headers=admin_headers,
        body={"slug": team_slug, "name": f"E2E Deep {suffix}"},
    )
    team = team_created.get("team", {})
    team_namespace = team.get("namespace", "")
    check(
        team.get("slug") == team_slug and bool(team_namespace),
        "POST /api/runtime/teams created managed team",
        f"team create response: {team_created}",
    )
    teams = expect_json(f"{api_base}/api/v1/runtime/teams", headers=admin_headers)
    check(
        team_slug in {item.get("slug") for item in teams.get("teams", [])},
        "GET /api/runtime/teams listed created team",
        f"created team missing from team list: {teams}",
    )
    expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}",
        headers=admin_headers,
    )
    expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}/members",
        headers=admin_headers,
    )
    membership = expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(user_id)}",
        method="PUT",
        headers=admin_headers,
        body={"role": "member"},
    )
    check(
        membership.get("membership", {}).get("user_id") == user_id,
        "PUT /api/runtime/teams/{slug}/members/{userID} added signup user",
        f"membership response: {membership}",
    )
    team_user_email = f"e2e-team-user-{suffix}@mcpruntime.org"
    team_user = expect_json(
        f"{api_base}/api/v1/users",
        status=201,
        method="POST",
        headers=admin_headers,
        body={"email": team_user_email, "password": test_user_password, "role": "user"},
    )
    team_user_id = team_user.get("user", {}).get("id", "")
    check(
        bool(team_user_id),
        "POST /api/users created team user",
        f"team user response: {team_user}",
    )
    expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(team_user_id)}",
        method="PUT",
        headers=admin_headers,
        body={"role": "owner"},
    )
    members_after = expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}/members",
        headers=admin_headers,
    )
    member_ids = {item.get("user_id") for item in members_after.get("members", [])}
    check(
        user_id in member_ids and team_user_id in member_ids,
        "GET /api/runtime/teams/{slug}/members listed created memberships",
        f"expected team members missing: {members_after}",
    )
    expect_json(
        f"{api_base}/api/v1/runtime/teams/{quote_segment(team_slug)}/members/{quote_segment(team_user_id)}",
        method="DELETE",
        headers=admin_headers,
    )

    namespaces = expect_json(
        f"{api_base}/api/v1/runtime/namespaces", headers=admin_headers
    )
    namespace_names = {
        item.get("namespace") for item in namespaces.get("namespaces", [])
    }
    check(
        team_namespace in namespace_names,
        "GET /api/runtime/namespaces listed created team namespace",
        f"team namespace missing from namespace list: {namespaces}",
    )
    namespace_item = expect_json(
        f"{api_base}/api/v1/runtime/namespaces/{quote_segment(team_namespace)}",
        headers=admin_headers,
    )
    check(
        namespace_item.get("namespace", {}).get("namespace") == team_namespace,
        "GET /api/runtime/namespaces/{namespace} returned created team namespace",
        f"namespace item response: {namespace_item}",
    )

    expect_status(
        f"{api_base}/api/v1/registry/authz",
        401,
        headers=registry_authz_headers(repo_path="/v2/_catalog"),
    )
    expect_status(
        f"{api_base}/api/v1/registry/authz",
        204,
        headers=registry_authz_headers(
            auth_headers,
            repo_path=f"/v2/{team_slug}/demo/manifests/latest",
        ),
    )
    personal_scope = user_namespace or registry_username
    if personal_scope:
        expect_status(
            f"{api_base}/api/v1/registry/authz",
            403,
            headers=registry_authz_headers(
                user_key_headers,
                repo_path=f"/v2/{personal_scope}/demo/manifests/latest",
            ),
        )
    registry_basic_headers = basic_headers(registry_username, registry_password)
    expect_status(
        f"{api_base}/api/v1/registry/authz",
        204,
        headers=registry_authz_headers(
            registry_basic_headers,
            repo_path=f"/v2/{team_slug}/demo/manifests/latest",
        ),
    )
    expect_status(
        f"{api_base}/api/v1/registry/authz",
        204,
        headers=registry_authz_headers(
            registry_basic_headers,
            repo_path=f"/v2/{team_namespace}/demo/manifests/latest",
        ),
    )

    expect_json(f"{api_base}/api/v1/analytics/usage?limit=3", headers=auth_headers)
    expect_json(f"{api_base}/api/v1/user/analytics/usage?limit=3", headers=user_headers)
    expect_json(
        f"{api_base}/api/v1/user/activity/image-publish",
        status=202,
        method="POST",
        headers=user_headers,
        body={
            "image_ref": f"registry.registry.svc.cluster.local:5000/{team_slug}/demo:{suffix}",
            "source_image": "docker.io/library/nginx:1.27-alpine",
            "mode": "pre-release",
        },
    )

    server_item = expect_json(
        f"{api_base}/api/v1/runtime/servers/mcp-servers/{quote_segment(server_name)}",
        headers=auth_headers,
    )
    check(
        server_item.get("server", {}).get("name") == server_name,
        "GET /api/runtime/servers/{namespace}/{name} returned policy server",
        f"server item response: {server_item}",
    )
    server_events = expect_json(
        f"{api_base}/api/v1/runtime/server-events?namespace=mcp-servers&server={urllib.parse.quote(server_name)}&limit=5",
        headers=auth_headers,
    )
    check(
        "events" in server_events,
        "GET /api/runtime/server-events returned events key",
        f"server events response: {server_events}",
    )

    temp_server_name = f"e2e-deep-server-{suffix}"
    temp_server = expect_json(
        f"{api_base}/api/v1/runtime/servers",
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
        f"{api_base}/api/v1/runtime/servers/mcp-servers/{quote_segment(temp_server_name)}",
        headers=admin_headers,
    )
    expect_json(
        f"{api_base}/api/v1/runtime/servers/mcp-servers/{quote_segment(temp_server_name)}",
        method="DELETE",
        headers=admin_headers,
    )

    expect_json(
        f"{api_base}/api/v1/runtime/grants/mcp-servers/{quote_segment(api_runtime_grant)}",
        headers=auth_headers,
    )
    expect_json(
        f"{api_base}/api/v1/runtime/sessions/mcp-servers/{quote_segment(api_runtime_session)}",
        headers=auth_headers,
    )
    expect_json(
        f"{api_base}/api/v1/runtime/sessions/mcp-servers/{quote_segment(api_runtime_session)}",
        method="DELETE",
        headers=auth_headers,
    )
    expect_json(
        f"{api_base}/api/v1/runtime/grants/mcp-servers/{quote_segment(api_runtime_grant)}",
        method="DELETE",
        headers=auth_headers,
    )

    deployment_name = f"e2e-deep-deploy-{suffix}"
    expect_json(
        f"{api_base}/api/v1/deployments?namespace=mcp-servers", headers=admin_headers
    )
    deployment = expect_json(
        f"{api_base}/api/v1/deployments",
        method="POST",
        headers=admin_headers,
        body={
            "name": deployment_name,
            "namespace": "mcp-servers",
            "image": "docker.io/library/nginx:1.27-alpine",
            "port": 8080,
            "replicas": 1,
        },
    )
    check(
        deployment.get("deployment", {}).get("name") == deployment_name,
        "POST /api/deployments created temporary deployment",
        f"deployment response: {deployment}",
    )
    expect_json(
        f"{api_base}/api/v1/admin/deployments?namespace=mcp-servers",
        headers=admin_headers,
    )
    expect_json(
        f"{api_base}/api/v1/deployments/mcp-servers/{quote_segment(deployment_name)}",
        method="DELETE",
        headers=admin_headers,
    )

    admin_namespaces = expect_json(
        f"{api_base}/api/v1/admin/namespaces", headers=admin_headers
    )
    check(
        "namespaces" in admin_namespaces,
        "GET /api/admin/namespaces returned namespaces",
        f"admin namespaces response: {admin_namespaces}",
    )
    admin_audit = expect_json(
        f"{api_base}/api/v1/admin/audit?limit=5", headers=admin_headers
    )
    check(
        "audit_logs" in admin_audit,
        "GET /api/admin/audit returned audit_logs",
        f"admin audit response: {admin_audit}",
    )
    admin_operations = expect_json(
        f"{api_base}/api/v1/admin/operations?limit=5", headers=admin_headers
    )
    check(
        "audit_logs" in admin_operations and "users" in admin_operations,
        "GET /api/admin/operations returned operations payload",
        f"admin operations response: {admin_operations}",
    )

    expect_json(
        f"{api_base}/api/v1/user/registry-credentials/{quote_segment(credential_id)}",
        method="DELETE",
        headers=user_headers,
    )
    expect_status(
        f"{api_base}/api/v1/registry/authz",
        401,
        headers=registry_authz_headers(
            registry_basic_headers,
            repo_path=f"/v2/{team_slug}/demo/manifests/latest",
        ),
    )
    expect_json(
        f"{api_base}/api/v1/user/api-keys/{quote_segment(user_key_id)}",
        method="DELETE",
        headers=user_headers,
    )
    expect_status(f"{api_base}/api/v1/auth/me", 401, headers=user_key_headers)

    expect_status(f"{ui_base}/auth/status", 200, contains='"authenticated":false')
    expect_status(
        f"{ui_base}/auth/login", 401, method="POST", body={"api_key": "wrong-api-key"}
    )
    ui_login_status, ui_login_headers, ui_login_body = request(
        f"{ui_base}/auth/login",
        method="POST",
        body={"api_key": api_key},
    )
    check(
        ui_login_status == 200,
        "POST /auth/login accepted UI API key",
        f"UI login failed: {ui_login_status} {ui_login_body}",
    )
    set_cookie = (
        ui_login_headers.get("Set-Cookie") or ui_login_headers.get("set-cookie") or ""
    )
    ui_cookie = set_cookie.split(";", 1)[0]
    check(
        ui_cookie.startswith("mcp_ui_session="),
        "POST /auth/login returned session cookie",
        f"missing UI session cookie: {ui_login_headers}",
    )
    ui_cookie_headers = {"Cookie": ui_cookie}
    ui_status = expect_json(f"{ui_base}/auth/status", headers=ui_cookie_headers)
    check(
        ui_status.get("authenticated") is True,
        "GET /auth/status returned authenticated UI session",
        f"UI status response: {ui_status}",
    )
    ui_admin_status, _, ui_admin_body = request(
        f"{ui_base}/auth/admin-check", headers=ui_cookie_headers
    )
    check(
        ui_admin_status == 204,
        "GET /auth/admin-check allowed admin UI session",
        f"admin-check failed: {ui_admin_status} {ui_admin_body}",
    )
    expect_status(f"{ui_base}/api/ui/v1/runtime/servers", 401, contains='"error":"unauthorized"')
    proxy_payload = expect_json(
        f"{ui_base}/api/ui/v1/runtime/servers", headers=ui_cookie_headers
    )
    check(
        isinstance(proxy_payload.get("servers"), list),
        "GET /api/ui/v1/runtime/servers used UI session",
        f"session proxy returned an invalid payload: {proxy_payload}",
    )
    ui_logout = expect_json(
        f"{ui_base}/auth/logout", method="POST", headers=ui_cookie_headers
    )
    check(
        ui_logout.get("authenticated") is False,
        "POST /auth/logout cleared UI session",
        f"UI logout response: {ui_logout}",
    )
    expect_status(
        f"{ui_base}/auth/status",
        200,
        headers=ui_cookie_headers,
        contains='"authenticated":false',
    )

    expect_status(f"{gateway_base}/auth/status", 200, contains='"authenticated":false')
    gateway_login_status, gateway_login_headers, gateway_login_body = request(
        f"{gateway_base}/auth/login",
        method="POST",
        body={"api_key": api_key},
    )
    check(
        gateway_login_status == 200,
        "gateway POST /auth/login accepted UI API key",
        f"gateway UI login failed: {gateway_login_status} {gateway_login_body}",
    )
    gateway_set_cookie = (
        gateway_login_headers.get("Set-Cookie")
        or gateway_login_headers.get("set-cookie")
        or ""
    )
    gateway_cookie = gateway_set_cookie.split(";", 1)[0]
    check(
        gateway_cookie.startswith("mcp_ui_session="),
        "gateway POST /auth/login returned session cookie",
        f"missing gateway UI session cookie: {gateway_login_headers}",
    )
    gateway_cookie_headers = {"Cookie": gateway_cookie}
    gateway_status = expect_json(
        f"{gateway_base}/auth/status", headers=gateway_cookie_headers
    )
    check(
        gateway_status.get("authenticated") is True,
        "gateway GET /auth/status returned authenticated UI session",
        f"gateway UI status response: {gateway_status}",
    )
    gateway_admin_status, _, gateway_admin_body = request(
        f"{gateway_base}/auth/admin-check", headers=gateway_cookie_headers
    )
    check(
        gateway_admin_status == 204,
        "gateway GET /auth/admin-check allowed admin UI session",
        f"gateway admin-check failed: {gateway_admin_status} {gateway_admin_body}",
    )
    expect_status(
        f"{gateway_base}/grafana/api/health",
        200,
        headers=gateway_cookie_headers,
        contains="database",
    )
    expect_status(
        f"{gateway_base}/prometheus/-/healthy", 404, headers=gateway_cookie_headers
    )
    gateway_logout = expect_json(
        f"{gateway_base}/auth/logout", method="POST", headers=gateway_cookie_headers
    )
    check(
        gateway_logout.get("authenticated") is False,
        "gateway POST /auth/logout cleared UI session",
        f"gateway UI logout response: {gateway_logout}",
    )

    print("deep request routes:")
    for route in (
        "gateway:/auth/login",
        "gateway:/auth/status",
        "gateway:/auth/admin-check",
        "gateway:/auth/logout",
        "gateway:/grafana/api/health (UI cookie)",
        "gateway:/prometheus/-/healthy (hidden)",
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
        "ui:/auth/login",
        "ui:/auth/status",
        "ui:/auth/admin-check",
        "ui:/auth/logout",
    ):
        print(f"  {route}")

# Ingest and processor service surfaces.
expect_status(f"{ingest_base}/health", 200, contains='"ok":true')
expect_status(f"{ingest_base}/live", 200, contains='"ok":true')
expect_status(f"{ingest_base}/ready", 200, contains='"ok":true')
expect_status(ingest_metrics_url, 200, contains="# HELP")
ingest_event = expect_json(
    f"{ingest_base}/events",
    status=202,
    method="POST",
    headers=ingest_headers,
    body={
        "timestamp": "2026-03-29T00:00:00Z",
        "source": "e2e-direct-ingest",
        "event_type": "service.route.check",
        "payload": {"service": "ingest", "route": "/events"},
    },
)
check(
    ingest_event.get("ok") is True,
    "ingest /events returned ok=true",
    f"ingest /events response unexpected: {ingest_event}",
)
expect_status(f"{processor_base}/health", 200, contains="ok")
expect_status(f"{processor_base}/metrics", 200, contains="# HELP")

print("service routes:")
for route in (
    "gateway:/",
    "gateway:/api/dashboard/summary",
    "gateway:/ping",
    "gateway:/config.js",
    f"gateway:{gateway_script_path}",
    f"gateway:{gateway_style_path}",
    "gateway:/grafana/api/health",
    "gateway:/prometheus/-/healthy (hidden)",
    "ingress:{server-host}:/{server}/mcp",
    "ingress:{oauth-host}:/{oauth-server}/mcp",
    "ingress:{oauth-host}:/.well-known/oauth-protected-resource/{oauth-server}/mcp",
    "ui:/health",
    "ui:/",
    "ui:/config.js",
    f"ui:{ui_script_path}",
    f"ui:{ui_style_path}",
    "mcp-gateway:/health",
    "mcp-gateway:/",
    "mcp-server:/health",
    "mcp-server:/",
    "oauth-proxy:/health",
    "oauth-proxy:/",
    "oauth-proxy:/.well-known/oauth-protected-resource",
    "oauth-proxy:/.well-known/oauth-protected-resource/{server}/mcp",
    "oauth-server:/health",
    "oauth-server:/",
    "api:/health",
    "api:/metrics",
    "api:/api/events",
    "api:/api/stats",
    "api:/api/sources",
    "api:/api/event-types",
    "api:/api/v1/events",
    "api:/api/dashboard/summary",
    "api:/api/runtime/servers",
    "api:/api/runtime/grants",
    "api:/api/runtime/sessions",
    "api:/api/runtime/components",
    "api:/api/runtime/policy",
    "api:/api/runtime/grants/{namespace}/{name}",
    "api:/api/runtime/sessions/{namespace}/{name}",
    "api:/api/runtime/actions/restart",
    "ingest:/health",
    "ingest:/live",
    "ingest:/ready",
    "ingest:/events",
    "ingest:/metrics",
    "processor:/health",
    "processor:/metrics",
):
    print(f"  {route}")
