#!/usr/bin/env python3
"""Probe the explicit service URLs in the repository authorization matrix."""
import json
import os
import sys
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import Request, build_opener, HTTPRedirectHandler


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("matrix redirects are rejected to protect test credentials")


def expected(row, status):
    if row.get("expect_authenticated"):
        return 200 <= status < 300 or status in (400, 422)
    return status == row["expect"]


def run(matrix):
    bases = {s: os.environ[f"AUTHZ_{s.upper()}_URL"].rstrip("/")
             for s in ("platform", "runtime", "analytics")}
    for base in bases.values():
        parsed = urlsplit(base)
        if parsed.scheme not in ("http", "https") or parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
            raise ValueError("matrix URLs must be explicit loopback port-forwards")
    keys = {"anon": "", "admin-key": os.environ["AUTHZ_ADMIN_KEY"],
            "user-key": os.environ["AUTHZ_USER_KEY"], "ingest-key": os.environ["AUTHZ_INGEST_KEY"]}
    if any(not keys[k] for k in ("admin-key", "user-key", "ingest-key")):
        raise ValueError("required matrix credentials are empty")
    if keys["user-key"] == keys["admin-key"]:
        raise ValueError("user and admin credentials must differ")
    with open(matrix, encoding="utf-8") as source:
        rows = json.load(source)
    if not rows:
        raise ValueError("matrix has no checks")
    failures = 0
    for row in rows:
        base = bases[row["service"].removesuffix("-api")]
        headers = {"Content-Type": "application/json"}
        if keys[row["role"]]:
            headers["x-api-key"] = keys[row["role"]]
        data = row.get("body")
        if data is not None and not isinstance(data, str):
            data = json.dumps(data)
        request = Request(base + row["path"], headers=headers, method=row["method"],
                          data=data.encode() if data is not None else None)
        try:
            with build_opener(NoRedirect()).open(request, timeout=10) as response:
                status = response.status
        except HTTPError as error:
            status = error.code
        passed = expected(row, status)
        failures += not passed
        print(f'{"PASS" if passed else "FAIL"} {row["service"]} {row["method"]} {row["path"]} role={row["role"]} status={status}')
    print(f"Authz matrix: {len(rows)} checked, {failures} failed")
    return bool(failures)


if __name__ == "__main__":
    sys.exit(run(sys.argv[1]))
