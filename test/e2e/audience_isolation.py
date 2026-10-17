"""Headless #325 acceptance against real split services and their gateway.

Only login and read operations are used. Direct URLs are read-only test
port-forwards; management operations continue through the supported CLI/UI.
No credentials or token-bearing responses are printed or written to artifacts.
"""

import base64
import json
import os
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def check(condition, label):
    if not condition:
        raise AssertionError(label)
    print(f"[audience-isolation] {label}")


def request(url, method="GET", headers=None, body=None):
    headers = dict(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    cafile = os.environ.get("SSL_CERT_FILE") or os.environ.get("CURL_CA_BUNDLE")
    context = ssl.create_default_context(cafile=cafile)
    try:
        with urllib.request.urlopen(req, timeout=15, context=context) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


def claims(token):
    payload = token.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))


def main():
    gateway = os.environ["PLATFORM_URL"].rstrip("/")
    platform = os.environ["DIRECT_PLATFORM_URL"].rstrip("/")
    runtime = os.environ["DIRECT_RUNTIME_URL"].rstrip("/")
    analytics = os.environ["DIRECT_ANALYTICS_URL"].rstrip("/")
    email = os.environ["PLATFORM_ADMIN_EMAIL"]
    password = os.environ["PLATFORM_ADMIN_PASSWORD"]
    status, _, body = request(gateway + "/api/v1/auth/login", "POST", body={"email": email, "password": password})
    check(status == 200, "password login succeeds through gateway")
    source = json.loads(body)["access_token"]
    source_claims = claims(source)
    check(source_claims["aud"] == ["platform-api"], "login token has only platform audience")
    authorization = {"Authorization": "Bearer " + source}
    targets = {
        "runtime": (runtime, "/api/v1/runtime/servers", "runtime-api"),
        "analytics": (analytics, "/api/v1/events?limit=1", "analytics-api"),
    }
    check(request(platform + "/api/v1/auth/me", headers=authorization)[0] == 200, "platform accepts source token")
    for name, (direct, path, audience) in targets.items():
        check(request(direct + path, headers=authorization)[0] == 401, f"direct {name} rejects platform token")
        check(request(gateway + path, headers=authorization)[0] == 200, f"gateway exchanges token for {name}")
        status, headers, _ = request(platform + "/internal/auth/exchange/" + name, headers=authorization)
        exchanged = headers.get("Authorization", "").removeprefix("Bearer ")
        check(status == 200 and bool(exchanged), f"{name} internal exchange returns credential")
        exchanged_claims = claims(exchanged)
        check(exchanged_claims["aud"] == [audience], f"{name} exchange has one fixed audience")
        check(0 < exchanged_claims["exp"] - exchanged_claims["iat"] <= 60 and exchanged_claims["exp"] <= source_claims["exp"], f"{name} expiry is bounded by 60 seconds and source expiry")
        scoped_headers = {"Authorization": "Bearer " + exchanged}
        check(request(direct + path, headers=scoped_headers)[0] == 200, f"direct {name} accepts its exchanged token")
        check(request(platform + "/api/v1/auth/me", headers=scoped_headers)[0] == 401, f"{name} token cannot replay against platform")
        other, other_path, _ = targets["analytics" if name == "runtime" else "runtime"]
        check(request(other + other_path, headers=scoped_headers)[0] == 401, f"{name} token cannot replay against other backend")
        check(request(platform + "/internal/auth/exchange/" + name, headers=scoped_headers)[0] == 401, f"{name} token cannot be exchanged again")
        _, public_headers, _ = request(gateway + "/internal/auth/exchange/" + name, headers=authorization)
        check(not public_headers.get("Authorization"), f"internal {name} exchange is not published by ingress")

    # Exercise supported CLI routing with the same single login token, without
    # selecting an audience in the client or touching the caller's profile.
    with tempfile.TemporaryDirectory(prefix="mcp-audience-cli-") as config:
        env = dict(os.environ, MCP_RUNTIME_CONFIG_DIR=config, MCP_PLATFORM_API_URL=gateway, MCP_PLATFORM_API_TOKEN=source)
        result = subprocess.run([os.environ["BIN"], "team", "list"], env=env, capture_output=True, timeout=30)
        check(result.returncode == 0, "CLI team list works through gateway with unchanged login token")
        check(b"SLUG" in result.stdout, "CLI returned the team table")

    # Password login uses the real cookie-backed UI path, rather than the
    # static-service-key path covered by ui_auth_flows.py.
    status, headers, body = request(gateway + "/auth/login", "POST", body={"email": email, "password": password})
    check(status == 200, "UI password login succeeds")
    cookie = headers.get("Set-Cookie", "").split(";", 1)[0]
    check(cookie.startswith("mcp_ui_session="), "UI returns opaque session cookie")
    cookie_headers = {"Cookie": cookie}
    try:
        for path in ["/api/ui/v1/runtime/servers", "/api/ui/v1/events?limit=1"]:
            status, headers, body = request(gateway + path, headers=cookie_headers)
            check(status == 200, "UI session reaches " + path.split("?")[0])
            check(not headers.get("Authorization") and source.encode() not in body, "UI response hides upstream tokens")
        status, _, _ = request(gateway + "/api/ui/v1/runtime/teams", "POST", headers=cookie_headers, body={})
        check(status == 403, "UI write without CSRF is denied before exchange")
    finally:
        request(gateway + "/auth/logout", "POST", headers=cookie_headers)
    # No token material is retained after this read-only acceptance journey.
    check(source_claims["exp"] > time.time(), "acceptance completed within source token lifetime")


if __name__ == "__main__":
    main()
