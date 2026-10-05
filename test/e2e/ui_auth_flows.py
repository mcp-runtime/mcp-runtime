import json
import os
import re
import urllib.error
import urllib.request

from _helpers_loader import load_e2e_helpers

_helpers = load_e2e_helpers()
check = _helpers.check

ui_base = os.environ["UI_BASE"]
gateway_base = os.environ["GATEWAY_BASE"]
api_key = os.environ["API_KEY"]
platform_mode = os.environ["E2E_PLATFORM_MODE"]


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
    check(got_status == status, f"{method} {url} returned {status}", f"{method} {url} returned {got_status}: {got_body}")
    if contains:
        check(contains in got_body, f"{method} {url} contained {contains!r}", f"{method} {url} missing {contains!r}: {got_body}")
    return got_body


def expect_json(url, status=200, *, method="GET", headers=None, body=None):
    return json.loads(expect_status(url, status, method=method, headers=headers, body=body))


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


def check_ui_auth(base, label, *, include_observability=False):
    expect_status(f"{base}/auth/status", 200, contains='"authenticated":false')
    expect_status(f"{base}/api/ui/v1/runtime/servers", 401, contains='"error":"unauthorized"')
    expect_status(f"{base}/api/ui/v1/runtime/adapter/sessions", 404, contains='"error":"not_found"')
    expect_status(f"{base}/auth/login", 401, method="POST", body={"api_key": "wrong-api-key"})
    login_status, login_headers, login_body = request(f"{base}/auth/login", method="POST", body={"api_key": api_key})
    check(login_status == 200, f"{label} POST /auth/login accepted UI API key", f"{label} login failed: {login_status} {login_body}")
    set_cookie = login_headers.get("Set-Cookie") or login_headers.get("set-cookie") or ""
    cookie = set_cookie.split(";", 1)[0]
    check(cookie.startswith("mcp_ui_session="), f"{label} POST /auth/login returned session cookie", f"missing cookie: {login_headers}")
    cookie_headers = {"Cookie": cookie}
    status = expect_json(f"{base}/auth/status", headers=cookie_headers)
    check(status.get("authenticated") is True, f"{label} GET /auth/status returned authenticated session", f"{label} status response: {status}")
    admin_status, _, admin_body = request(f"{base}/auth/admin-check", headers=cookie_headers)
    check(admin_status == 204, f"{label} GET /auth/admin-check allowed admin session", f"{label} admin-check failed: {admin_status} {admin_body}")
    proxy_payload = expect_json(f"{base}/api/ui/v1/runtime/servers", headers=cookie_headers)
    check(
        isinstance(proxy_payload.get("servers"), list),
        f"{label} GET /api/ui/v1/runtime/servers used UI session",
        f"{label} session proxy returned an invalid payload: {proxy_payload}",
    )
    proxy_body = json.dumps(proxy_payload)
    check(api_key not in proxy_body, f"{label} session proxy omitted API key", f"{label} session proxy leaked API key")
    check("Bearer " not in proxy_body, f"{label} session proxy omitted bearer token", f"{label} session proxy leaked bearer")
    if include_observability:
        expect_status(f"{base}/grafana/api/health", 200, headers=cookie_headers, contains="database")
        expect_status(f"{base}/prometheus/-/healthy", 404, headers=cookie_headers)
    logout = expect_json(f"{base}/auth/logout", method="POST", headers=cookie_headers)
    check(logout.get("authenticated") is False, f"{label} POST /auth/logout cleared session", f"{label} logout response: {logout}")
    expect_status(f"{base}/auth/status", 200, headers=cookie_headers, contains='"authenticated":false')


expect_status(f"{ui_base}/health", 200, contains='"ok":true')
ui_index = expect_status(f"{ui_base}/", 200, contains="MCP Runtime Control Plane")
ui_config = expect_status(f"{ui_base}/config.js", 200, contains="window.MCP_API_BASE")
check(f'window.MCP_PLATFORM_MODE = "{platform_mode}"' in ui_config, "ui config.js exposes platform mode", f"ui config missing platform mode {platform_mode}: {ui_config}")
ui_script_path, ui_style_path = check_vite_assets(ui_base, "ui", ui_index)
gateway_index = expect_status(f"{gateway_base}/", 200, contains="MCP Runtime Control Plane")
expect_status(f"{gateway_base}/config.js", 200, contains="window.MCP_API_BASE")
gateway_script_path, gateway_style_path = check_vite_assets(gateway_base, "gateway", gateway_index)
expect_status(f"{gateway_base}/grafana/api/health", 401)
expect_status(f"{gateway_base}/prometheus/-/healthy", 404)

check_ui_auth(ui_base, "ui")
check_ui_auth(gateway_base, "gateway", include_observability=True)
adapter_status, _, adapter_body = request(f"{gateway_base}/api/v1/runtime/adapter/sessions")
check(
    adapter_status == 401,
    "gateway GET /api/v1/runtime/adapter/sessions stays on runtime-api",
    f"adapter sessions status={adapter_status} body={adapter_body}",
)
check(
    "authentication required" in adapter_body,
    "gateway adapter sessions still require runtime-api credentials",
    f"adapter sessions body={adapter_body}",
)

print("ui-auth request routes:")
for route in (
    "ui:/health",
    "ui:/",
    "ui:/config.js",
    f"ui:{ui_script_path}",
    f"ui:{ui_style_path}",
    "ui:/auth/login",
    "ui:/auth/status",
    "ui:/auth/admin-check",
    "ui:/auth/logout",
    "ui:/api/ui/v1/runtime/servers",
    "ui:/api/ui/v1/runtime/adapter/sessions",
    "gateway:/",
    "gateway:/config.js",
    f"gateway:{gateway_script_path}",
    f"gateway:{gateway_style_path}",
    "gateway:/auth/login",
    "gateway:/auth/status",
    "gateway:/auth/admin-check",
    "gateway:/auth/logout",
    "gateway:/api/ui/v1/runtime/servers",
    "gateway:/api/v1/runtime/adapter/sessions",
    "gateway:/grafana/api/health",
    "gateway:/prometheus/-/healthy (hidden)",
):
    print(f"  {route}")
