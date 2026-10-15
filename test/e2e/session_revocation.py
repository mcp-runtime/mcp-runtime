"""Measure certificate revocation without shorter per-request deadlines."""

import json
import os
from pathlib import Path
import subprocess
import time


def wait_for_revocation(url, credentials, host, protocol, mcp_session="", budget=1, *, payload=None, oauth_token=""):
    if payload is None:
        payload = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
    started = time.monotonic()
    attempts = 0
    last_response = "no response"
    while (elapsed := time.monotonic() - started) < budget:
        # A denial taking 300ms still meets a one-second target. A fixed 200ms
        # timeout would discard it and could make every observation fail.
        # curl treats a timeout rounded to zero as unlimited.
        command = ["curl", "-ksS", "--max-time", f"{max(0.001, budget - elapsed):.6f}", "--cert", str(Path(credentials) / "client.crt"), "--key", str(Path(credentials) / "client.key"), "-H", "Host: " + host, "-H", "content-type: application/json", "-H", "accept: application/json, text/event-stream", "-H", "Mcp-Protocol-Version: " + protocol]
        if mcp_session:
            command += ["-H", "Mcp-Session-Id: " + mcp_session]
        if oauth_token:
            command += ["-H", "Authorization: Bearer " + oauth_token]
        command += ["--data", payload, "-w", "\n%{http_code}", url]
        response = subprocess.run(command, capture_output=True, text=True)
        attempts += 1
        code = "unknown"
        revoked = False
        if response.returncode == 0:
            try:
                body, code = response.stdout.rsplit("\n", 1)
                revoked = code == "401" and json.loads(body).get("error") == "session_revoked"
            except (ValueError, TypeError, AttributeError):
                # A malformed/intermediate response is not proof of revocation.
                # Keep probing within the deadline; diagnostics retain its status.
                pass
        # Report transport/status metadata, never bearer values or raw payloads.
        last_response = f"curl exit {response.returncode}, HTTP {code}"
        elapsed = time.monotonic() - started
        if revoked and elapsed <= budget:
            return elapsed
        time.sleep(0.025)
    limit = "one second" if budget == 1 else f"{budget:g} seconds"
    raise AssertionError(f"session revocation did not reach the gateway within {limit}; attempts={attempts}, last {last_response}")


if __name__ == "__main__":
    try:
        elapsed = wait_for_revocation(os.environ["SESSION_CHECK_URL"], os.environ["SESSION_CHECK_CERT"], os.environ["SESSION_CHECK_HOST"], os.environ["SESSION_CHECK_PROTOCOL"], oauth_token=os.environ["SESSION_CHECK_TOKEN"])
    except AssertionError as error:
        raise SystemExit(str(error)) from None
    print(f"session revocation reached the gateway in {elapsed:.3f}s")
