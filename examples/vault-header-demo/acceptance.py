#!/usr/bin/env python3
"""Loopback-only acceptance: real bundle/upstream, explicit fake desktop CLI.

This checks the wire flow, not a real vault, native prompt or production install.
It starts no container, cluster or deployment and prints no tokens/credentials.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
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


HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(url, data=None, token=None, form=False):
    headers = {}
    if data is not None:
        if form:
            data = urllib.parse.urlencode(data).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            data = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"
            headers["Accept"] = "application/json, text/event-stream"
            headers["Mcp-Protocol-Version"] = "2025-11-25"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(url, data=data, headers=headers)
    try:
        response = HTTP.open(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, response.read()


def wait(url, process):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("fixture process exited during startup")
        try:
            if request(url)[0] == 204 or request(url)[0] == 200:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise RuntimeError("fixture process did not become ready")


def write_json(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)


def run(args):
    auth_port, upstream_port, gateway_port = port(), port(), port()
    issuer = f"http://127.0.0.1:{auth_port}"
    upstream = f"http://127.0.0.1:{upstream_port}"
    gateway = f"http://127.0.0.1:{gateway_port}"
    resource = gateway + "/vault-header-demo/mcp"
    processes = []
    with tempfile.TemporaryDirectory(prefix="vault-header-acceptance-") as tmp:
        root = Path(tmp)
        try:
            key = root / "client.pem"
            subprocess.run(
                [
                    "openssl",
                    "genpkey",
                    "-algorithm",
                    "RSA",
                    "-pkeyopt",
                    "rsa_keygen_bits:2048",
                    "-out",
                    str(key),
                ],
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            key.chmod(0o600)
            binding = root / "binding"
            binding.write_text(secrets.token_hex(32))
            binding.chmod(0o600)
            password = root / "fixture-password"
            password.write_text("wrong")
            password.chmod(0o600)
            # Deliberately fake op: avoids accessing anyone's actual vault.
            op = root / "op"
            op.write_text(
                '#!/bin/sh\ncase "$5" in\n'
                "op://TestVault/MCPDemo/username) printf demo-user;;\n"
                f"op://TestVault/MCPDemo/password) cat {shlex.quote(str(password))};;\n"
                "*) exit 7;;\nesac\n"
            )
            op.chmod(0o700)
            here = Path(__file__).resolve().parent
            config = json.loads(
                (here / ".mcp/credential-gateway.example.json").read_text()
            )
            config.update(
                {
                    "listen": {"host": "127.0.0.1", "port": gateway_port},
                    "allow_insecure_http": True,
                    "authorization_server": {"issuer": issuer},
                    "session_binding_key_file": str(binding),
                    "mappings_file": str(root / "mappings.json"),
                    "provider": {
                        "type": "onepassword",
                        "options": {"mode": "desktop", "account": "test-account"},
                    },
                }
            )
            service = config["services"][0]
            service.update(
                {
                    "resource_url": resource,
                    "upstream_url": upstream + "/mcp",
                    "validation_url": upstream + "/mcp/auth/check",
                }
            )
            service["resource_client"]["private_key_file"] = str(key)
            write_json(root / "gateway.json", config)
            write_json(
                root / "mappings.json",
                {
                    "mappings": [
                        {
                            "issuer": issuer,
                            "subject": "alice",
                            "service": "vault-header-demo",
                            "fields": {
                                "username": "op://TestVault/MCPDemo/username",
                                "password": "op://TestVault/MCPDemo/password",
                            },
                        }
                    ]
                },
            )
            env = os.environ.copy()
            # Keep local fixtures independent of the caller's OAuth settings.
            for name in list(env):
                if name.startswith("MCP_AUTH_"):
                    del env[name]
            env.update(
                {
                    "PORT": str(upstream_port),
                    "LISTEN_HOST": "127.0.0.1",
                    "MCP_PATH": "/mcp",
                }
            )
            processes.append(
                subprocess.Popen(
                    [args.upstream_binary],
                    env=env,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            )
            wait(upstream + "/healthz", processes[-1])
            env.update(
                {
                    "PATH": str(root) + os.pathsep + os.environ.get("PATH", ""),
                    "MCP_AUTH_ISSUER": issuer,
                    "MCP_AUTH_LISTEN_ADDR": f"127.0.0.1:{auth_port}",
                    "MCP_AUTH_RESOURCES": resource,
                    "MCP_AUTH_ALLOWED_SCOPES": "tools:read",
                    "MCP_AUTH_LOCAL_DEVELOPMENT": "true",
                    "MCP_AUTH_LOCAL_SUBJECT": "alice",
                    "MCP_AUTH_REGISTRATION_ENABLED": "true",
                    "MCP_AUTH_REQUIRE_HTTPS": "false",
                    "MCP_AUTH_CREDENTIAL_GATEWAY_CONFIG": str(root / "gateway.json"),
                    "MCP_AUTH_LOG_LEVEL": "silent",
                    "MCP_AUTH_STORE_BACKEND": "memory",
                }
            )
            processes.append(
                subprocess.Popen(
                    [args.auth_binary],
                    env=env,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            )
            wait(issuer + "/healthz", processes[-1])
            callback = "http://127.0.0.1:39001/callback"
            status, _, body = request(
                issuer + "/register",
                {
                    "client_name": "vault acceptance",
                    "redirect_uris": [callback],
                    "token_endpoint_auth_method": "none",
                },
            )
            assert status == 201, "client registration failed"
            client = json.loads(body)["client_id"]
            verifier = secrets.token_urlsafe(48)
            challenge = (
                base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest())
                .rstrip(b"=")
                .decode()
            )
            query = urllib.parse.urlencode(
                {
                    "client_id": client,
                    "redirect_uri": callback,
                    "response_type": "code",
                    "code_challenge_method": "S256",
                    "code_challenge": challenge,
                    "resource": resource,
                    "scope": "tools:read",
                    "state": "acceptance",
                    "approve": "true",
                }
            )
            status, headers, _ = request(issuer + "/authorize?" + query)
            params = urllib.parse.parse_qs(
                urllib.parse.urlparse(headers.get("Location", "")).query
            )
            assert (
                status == 302
                and params.get("error") == ["access_denied"]
                and "code" not in params
            ), "bad vault credentials issued a grant"
            print("PASS: wrong vault credentials denied before OAuth code")
            password.write_text("demo-password")
            status, headers, _ = request(issuer + "/authorize?" + query)
            params = urllib.parse.parse_qs(
                urllib.parse.urlparse(headers.get("Location", "")).query
            )
            assert status == 302 and "code" in params, (
                "valid saved credentials did not authorize"
            )
            status, _, body = request(
                issuer + "/token",
                {
                    "grant_type": "authorization_code",
                    "client_id": client,
                    "redirect_uri": callback,
                    "code": params["code"][0],
                    "code_verifier": verifier,
                    "resource": resource,
                },
                form=True,
            )
            assert status == 200, "code exchange failed"
            tokens = json.loads(body)
            token = tokens["access_token"]

            def rpc(identifier, method, params):
                status, _, body = request(
                    resource,
                    {
                        "jsonrpc": "2.0",
                        "id": identifier,
                        "method": method,
                        "params": params,
                    },
                    token=token,
                )
                assert status == 200, "authenticated MCP request failed"
                value = json.loads(body)
                assert "error" not in value and "demo-password" not in body.decode(), (
                    "MCP response failed or exposed credentials"
                )
                return value["result"]

            rpc(
                1,
                "initialize",
                {
                    "protocolVersion": "2025-11-25",
                    "capabilities": {},
                    "clientInfo": {"name": "acceptance", "version": "1"},
                },
            )
            tools = rpc(2, "tools/list", {})["tools"]
            assert {tool["name"] for tool in tools} == {"upstream_status", "echo"}, (
                "unexpected upstream tools"
            )
            for identifier in (3, 4):
                result = rpc(
                    identifier,
                    "tools/call",
                    {"name": "upstream_status", "arguments": {}},
                )
                assert "upstream authenticated" in json.dumps(result), (
                    "upstream did not authenticate"
                )
            print(
                "PASS: real MCP initialize/list/repeated tool calls use OAuth-only client requests"
            )
            status, _, body = request(
                issuer + "/token",
                {
                    "grant_type": "refresh_token",
                    "client_id": client,
                    "refresh_token": tokens["refresh_token"],
                    "resource": resource,
                },
                form=True,
            )
            assert status == 200, "refresh failed"
            tokens = json.loads(body)
            token = tokens["access_token"]
            rpc(
                5, "tools/call", {"name": "echo", "arguments": {"message": "refreshed"}}
            )
            status, _, _ = request(
                issuer + "/revoke",
                {"client_id": client, "token": tokens["refresh_token"]},
                form=True,
            )
            assert status == 200, "revocation failed"
            assert (
                request(
                    resource,
                    {"jsonrpc": "2.0", "id": 6, "method": "tools/list"},
                    token=token,
                )[0]
                == 401
            ), "revoked session used cached credentials"
            print(
                "PASS: refresh retains upstream access; revocation blocks cached headers"
            )
            print(
                "Evidence uses a fake local op executable; no real vault or native prompt tested."
            )
        finally:
            for process in reversed(processes):
                process.terminate()
            for process in processes:
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auth-binary", required=True)
    parser.add_argument("--upstream-binary", required=True)
    run(parser.parse_args())
