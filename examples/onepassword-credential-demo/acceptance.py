#!/usr/bin/env python3
"""Loopback-only acceptance against real mcp-auth and gateway binaries.

Uses public fixture credentials, never a real vault or production service.
Tokens stay in memory and are never printed. No third-party Python packages.
"""

import argparse
import base64
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def client():
    return urllib.request.build_opener(NoRedirect, urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def request(opener, url, body=None, headers=None):
    assert urllib.parse.urlparse(url).hostname == "127.0.0.1", "acceptance refuses non-loopback targets"
    req = urllib.request.Request(url, data=body, headers=headers or {})
    try:
        response = opener.open(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read(65536), response.headers


def form(opener, url, values, origin=None):
    headers = {"Content-Type": "application/x-www-form-urlencoded"}
    if origin:
        headers["Origin"] = origin
    return request(opener, url, urllib.parse.urlencode(values).encode(), headers)


def field(body, name):
    match = re.search(r'name="' + name + r'" value="([^"]+)"', body.decode())
    assert match, "expected login field missing"
    return match.group(1)


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def wait_ready(url, process, startup_log):
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError("test service exited before readiness: " + startup_log.read_text()[:4096])
        try:
            if request(client(), url)[0] == 200:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise AssertionError("test service readiness timed out")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auth-binary", required=True)
    parser.add_argument("--gateway-binary", required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    auth = f"http://127.0.0.1:{port()}"
    gateway = f"http://127.0.0.1:{port()}"
    resource = gateway + "/demo/mcp"
    processes = []
    with tempfile.TemporaryDirectory(prefix="credential-login-test-") as directory:
        directory = Path(directory)
        connector = {
            "credential-gateway": {
                "issuer": resource + "/login",
                "authorization_endpoint": resource + "/login/authorize",
                "token_endpoint": resource + "/login/token",
                "userinfo_endpoint": resource + "/login/userinfo",
                "client_id": "mcp-auth-demo",
                "exchange_client_id": "unused",
                "token_endpoint_auth_method": "none",
                "scopes": ["profile"],
                "identity_claims": ["sub"],
                "allowed_upstream_callback_uris": [auth + "/identity/callback"],
            }
        }
        methods = {"methods": [{"id": "upstream-demo", "type": "oidc", "connector": "credential-gateway", "resources": [resource]}]}
        (directory / "connectors.json").write_text(json.dumps(connector))
        (directory / "methods.json").write_text(json.dumps(methods))
        environment = {key: value for key, value in os.environ.items() if not key.startswith("MCP_AUTH_")}
        environment.update({
            "MCP_AUTH_ISSUER": auth,
            "MCP_AUTH_RESOURCES": resource,
            "MCP_AUTH_LISTEN_ADDR": "127.0.0.1:" + str(urllib.parse.urlparse(auth).port),
            "MCP_AUTH_LOCAL_DEVELOPMENT": "true",
            "MCP_AUTH_REQUIRE_HTTPS": "false",
            "MCP_AUTH_ALLOW_INSECURE_CONNECTORS": "true",
            "MCP_AUTH_REGISTRATION_ENABLED": "true",
            "MCP_AUTH_LOGIN_METHODS_FILE": str(directory / "methods.json"),
            "MCP_AUTH_CONNECTORS_FILE": str(directory / "connectors.json"),
            "MCP_AUTH_STORE": "memory",
        })
        try:
            with (directory / "auth.log").open("wb") as log:
                process = subprocess.Popen([str(Path(args.auth_binary).resolve())], env=environment, stdout=log, stderr=log)
            processes.append(process)
            wait_ready(auth + "/healthz", process, directory / "auth.log")
            gateway_env = dict(os.environ, MCP_AUTH_ISSUER=auth, MCP_AUTH_RESOURCE=resource, PORT=str(urllib.parse.urlparse(gateway).port), DEMO_LOCAL_TEST="true", UPSTREAM_AUTH_CONFIG=str(root / ".mcp/upstream-auth.json"))
            with (directory / "gateway.log").open("wb") as log:
                process = subprocess.Popen([str(Path(args.gateway_binary).resolve())], env=gateway_env, stdout=log, stderr=log)
            processes.append(process)
            wait_ready(gateway + "/health", process, directory / "gateway.log")
            assert request(client(), resource)[0] == 401, "anonymous MCP request accepted"

            def sign_in(label):
                browser = client()
                callback = "http://127.0.0.1:9/callback"
                status, body, _ = request(browser, auth + "/register", json.dumps({"client_name": label, "redirect_uris": [callback], "token_endpoint_auth_method": "none", "grant_types": ["authorization_code", "refresh_token"], "response_types": ["code"]}).encode(), {"Content-Type": "application/json"})
                assert status == 201, "client registration failed"
                client_id = json.loads(body)["client_id"]
                verifier = base64.urlsafe_b64encode(os.urandom(32)).rstrip(b"=").decode()
                challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
                params = {"response_type": "code", "client_id": client_id, "redirect_uri": callback, "resource": resource, "scope": "tools:read", "state": "client-state", "code_challenge_method": "S256", "code_challenge": challenge}
                status, body, _ = request(browser, auth + "/authorize?" + urllib.parse.urlencode(params))
                assert status == 200, "consent page failed"
                status, _, headers = form(browser, auth + "/authorize/consent", {"consent_id": field(body, "consent_id"), "decision": "approve"})
                assert status in (302, 303), "consent did not start resource login"
                location = headers["Location"]
                assert location.startswith(resource + "/login/authorize?"), "resource login was bypassed"
                status, body, _ = request(browser, location)
                assert status == 200 and b'current-password' in body, "upstream credential form missing"
                login = {"request": field(body, "request"), "csrf": field(body, "csrf"), "username": "demo-user", "password": "wrong"}
                assert form(browser, auth + "/token", {"grant_type": "authorization_code", "client_id": client_id, "code": "not-issued", "redirect_uri": callback, "code_verifier": verifier, "resource": resource})[0] == 400, "token issued before upstream login"
                status, _, headers = form(browser, resource + "/login/approve", login, gateway)
                assert status == 401 and "Location" not in headers, "wrong upstream credentials produced a grant"
                login["password"] = "demo-password"
                status, _, headers = form(browser, resource + "/login/approve", login, gateway)
                assert status == 303, "validated upstream Login did not resume broker"
                status, _, headers = request(browser, headers["Location"])
                assert status in (302, 303), "broker callback did not issue client authorization code"
                query = urllib.parse.parse_qs(urllib.parse.urlparse(headers["Location"]).query)
                assert query.get("iss") == [auth] and query.get("state") == ["client-state"], "OAuth callback binding failed"
                status, body, _ = form(browser, auth + "/token", {"grant_type": "authorization_code", "client_id": client_id, "code": query["code"][0], "redirect_uri": callback, "code_verifier": verifier, "resource": resource})
                assert status == 200, "client token exchange failed"
                return browser, client_id, json.loads(body)

            browser, client_id, tokens = sign_in("first-test-client")
            print("PASS: wrong credentials denied; real mcp-auth issued OAuth only after upstream Login validation")

            def rpc(token, method, params, message_id=1):
                status, body, _ = request(client(), resource, json.dumps({"jsonrpc": "2.0", "id": message_id, "method": method, "params": params}).encode(), {"Authorization": "Bearer " + token, "Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
                assert status == 200, "OAuth-only client request did not reach header-authenticated upstream"
                assert b"demo-password" not in body and b"demo-user" not in body, "upstream credentials leaked to client"
                result = json.loads(body)
                assert "error" not in result, "upstream MCP protocol error"
                return result["result"]

            token = tokens["access_token"]
            rpc(token, "initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "loopback-acceptance", "version": "1"}})
            assert {tool["name"] for tool in rpc(token, "tools/list", {})["tools"]} == {"upstream_status", "echo"}, "post-login connection tool is still exposed"
            for _ in range(2):
                assert not rpc(token, "tools/call", {"name": "upstream_status", "arguments": {}}).get("isError"), "upstream authentication failed"
            print("PASS: initialize, tools/list and repeated calls use gateway headers; MCP client sends OAuth only")
            status, body, _ = form(browser, auth + "/token", {"grant_type": "refresh_token", "client_id": client_id, "refresh_token": tokens["refresh_token"], "resource": resource})
            assert status == 200, "OAuth refresh failed"
            refreshed = json.loads(body)["access_token"]
            assert not rpc(refreshed, "tools/call", {"name": "upstream_status", "arguments": {}}).get("isError"), "refreshed token lost the upstream credential connection"
            print("PASS: refreshed OAuth token reuses the gateway's retained upstream credentials")
            _, _, second = sign_in("second-test-client")
            def subject(value):
                part = value.split(".")[1]
                return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))["sub"]
            assert subject(second["access_token"]) != subject(token), "new clients unexpectedly share a credential login identity"
            print("PASS: a new client must complete a fresh upstream credential login")
        finally:
            for process in reversed(processes):
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()
