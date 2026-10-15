"""Assertions for the three-team certificate delegation journey."""

import argparse
import json
import sys

from session_revocation import wait_for_revocation as wait_for_session_revocation


def assert_attribution(payload, server, session, subject_team, resource_team):
    events = payload.get("events", [])
    matching = [event for event in events if event.get("server") == server and event.get("session_id") == session and event.get("tool_name") in {"aaa-ping", "echo"}]
    outcomes = set()
    for event in matching:
        if event.get("subject_team_id") != subject_team or event.get("resource_team_id") != resource_team:
            raise AssertionError("gateway event lost subject/resource team attribution")
        outcomes.add((event.get("tool_name"), event.get("decision")))
    if not {("aaa-ping", "allow"), ("echo", "deny")}.issubset(outcomes):
        raise AssertionError("waiting for the granted and ungranted cross-team tool decisions")


def wait_for_revocation(url, credentials, host, protocol, mcp_session, budget=1):
    payload = '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"aaa-ping","arguments":{}}}'
    return wait_for_session_revocation(url, credentials, host, protocol, mcp_session, budget, payload=payload)


def main():
    parser = argparse.ArgumentParser()
    modes = parser.add_subparsers(dest="mode", required=True)
    attribution = modes.add_parser("attribution")
    for field in ("server", "session", "subject-team", "resource-team"):
        attribution.add_argument("--" + field, required=True)
    revoked = modes.add_parser("revoked")
    for field in ("url", "credentials", "host", "protocol", "mcp-session"):
        revoked.add_argument("--" + field, required=True)
    args = parser.parse_args()
    if args.mode == "attribution":
        assert_attribution(json.load(sys.stdin), args.server, args.session, args.subject_team, args.resource_team)
    else:
        elapsed = wait_for_revocation(args.url, args.credentials, args.host, args.protocol, args.mcp_session)
        print(f"cross-team certificate revoked in {elapsed:.3f}s")


if __name__ == "__main__":
    main()
