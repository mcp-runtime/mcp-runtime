#!/usr/bin/env python3
"""Assertions for the credential-broker QA scenario (scenarios/credential-broker.sh).

The secret value is read from a 0600 file and is never accepted on argv or
printed. Failures name the check, never the value.
"""
import argparse
import hashlib
import json
import sys
from pathlib import Path


def secret_of(path):
    value = Path(path).read_text(encoding="utf-8").strip()
    if not value:
        raise SystemExit("empty secret file")
    return value


def read(path):
    return Path(path).read_text(encoding="utf-8", errors="replace")


def tool_report(body_path):
    envelope = json.loads(read(body_path))
    text = envelope["result"]["content"][0]["text"]
    return json.loads(text)


def cmd_delivered(args):
    secret = secret_of(args.secret_file)
    absent_in([args.body, args.headers], secret)
    report = tool_report(args.body)
    digest = hashlib.sha256(secret.encode()).hexdigest()
    if args.expect == "bound":
        if report["count"] != 1 or report["reserved"][0]["sha256"] != digest:
            raise SystemExit("bound server did not receive exactly the brokered credential")
        if report["reserved"][0]["name"] != args.header.lower():
            raise SystemExit("credential arrived under an unexpected header name")
    else:
        if report["count"] != 0:
            raise SystemExit("credential or forged reserved header reached a server/tool that is not bound")


def absent_in(paths, secret):
    for path in paths:
        if secret in read(path):
            raise SystemExit("secret value found in " + Path(path).name)


def cmd_absent(args):
    secret = secret_of(args.secret_file)
    absent_in(args.files, secret)


def cmd_no_reserved_headers(args):
    for line in read(args.headers).splitlines():
        name = line.split(":", 1)[0].strip().lower()
        if name.startswith("x-mcp-credential-") or name.startswith("trailer") and "x-mcp-credential-" in line.lower():
            raise SystemExit("reserved credential header reached the client: " + name)


def cmd_denied(args):
    secret = secret_of(args.secret_file)
    if int(args.status) == 200:
        raise SystemExit("call was allowed but must be denied")
    body = read(args.body)
    if '"result"' in body:
        raise SystemExit("denied call returned a tool result")
    absent_in([args.body, args.headers], secret)


def cmd_audit(args):
    secret = secret_of(args.secret_file)
    raw = read(args.file)
    if secret in raw:
        raise SystemExit("secret value found in audit rows")
    rows = [r for r in json.loads(raw).get("audit_logs", []) if r.get("namespace") == args.namespace]
    seen = {(r.get("action"), r.get("status")) for r in rows}
    for want in args.require:
        action, _, status = want.partition(":")
        if not any(a == action and (not status or s == status) for a, s in seen):
            raise SystemExit("missing audit row " + want)
    for row in rows:
        for field in ("value", "secret", "payload"):
            if field in row and row[field]:
                raise SystemExit("audit row carries a value-like field: " + field)


def main(argv=None):
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    d = sub.add_parser("delivered")
    d.add_argument("--body", required=True)
    d.add_argument("--headers", required=True)
    d.add_argument("--secret-file", required=True)
    d.add_argument("--expect", choices=["bound", "none"], required=True)
    d.add_argument("--header", default="X-Mcp-Credential-Token")
    d.set_defaults(fn=cmd_delivered)
    a = sub.add_parser("absent")
    a.add_argument("--secret-file", required=True)
    a.add_argument("files", nargs="+")
    a.set_defaults(fn=cmd_absent)
    n = sub.add_parser("no-reserved-headers")
    n.add_argument("--headers", required=True)
    n.set_defaults(fn=cmd_no_reserved_headers)
    x = sub.add_parser("denied")
    x.add_argument("--status", required=True)
    x.add_argument("--body", required=True)
    x.add_argument("--headers", required=True)
    x.add_argument("--secret-file", required=True)
    x.set_defaults(fn=cmd_denied)
    u = sub.add_parser("audit")
    u.add_argument("--file", required=True)
    u.add_argument("--namespace", required=True)
    u.add_argument("--secret-file", required=True)
    u.add_argument("--require", action="append", default=[])
    u.set_defaults(fn=cmd_audit)
    args = p.parse_args(argv)
    args.fn(args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
