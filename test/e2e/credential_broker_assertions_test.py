#!/usr/bin/env python3
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import credential_broker_assertions as a

SECRET = "s3cret-fixture-value"


def report(count, secret=SECRET, name="x-mcp-credential-token"):
    reserved = [{"name": name, "sha256": hashlib.sha256(secret.encode()).hexdigest()}] if count else []
    text = json.dumps({"tool": "fingerprint", "count": count, "reserved": reserved})
    return json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": text}]}})


class Assertions(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.secret = self.dir / "secret"
        self.secret.write_text(SECRET)

    def f(self, name, text):
        path = self.dir / name
        path.write_text(text)
        return str(path)

    def run_cmd(self, *argv):
        return a.main(list(argv))

    def delivered(self, body, expect, headers="HTTP/1.1 200 OK\r\n"):
        return self.run_cmd("delivered", "--body", self.f("b", body), "--headers", self.f("h", headers),
                            "--secret-file", str(self.secret), "--expect", expect)

    def test_bound_delivery_matches_fingerprint(self):
        self.assertEqual(self.delivered(report(1), "bound"), 0)

    def test_bound_rejects_forged_value(self):
        with self.assertRaises(SystemExit):
            self.delivered(report(1, secret="attacker"), "bound")

    def test_bound_rejects_missing(self):
        with self.assertRaises(SystemExit):
            self.delivered(report(0), "bound")

    def test_unbound_rejects_any_reserved_header(self):
        self.assertEqual(self.delivered(report(0), "none"), 0)
        with self.assertRaises(SystemExit):
            self.delivered(report(1, secret="attacker"), "none")

    def test_value_in_response_is_a_leak(self):
        with self.assertRaises(SystemExit):
            self.delivered(report(1) + SECRET, "bound")

    def test_reserved_response_header_is_a_leak(self):
        with self.assertRaises(SystemExit):
            self.run_cmd("no-reserved-headers", "--headers", self.f("h", "HTTP/1.1 200\r\nX-Mcp-Credential-Echo: v\r\n"))
        self.assertEqual(self.run_cmd("no-reserved-headers", "--headers", self.f("h", "HTTP/1.1 200\r\nContent-Type: a\r\n")), 0)

    def test_denied(self):
        args = ["denied", "--body", self.f("b", '{"error":"x"}'), "--headers", self.f("h", ""), "--secret-file", str(self.secret)]
        self.assertEqual(self.run_cmd(*args, "--status", "403"), 0)
        with self.assertRaises(SystemExit):
            self.run_cmd(*args, "--status", "200")

    def test_audit(self):
        rows = {"audit_logs": [{"namespace": "ns", "action": "credential_source_write", "status": "success"},
                               {"namespace": "ns", "action": "credential_access", "status": "denied"}]}
        base = ["audit", "--file", self.f("a", json.dumps(rows)), "--namespace", "ns", "--secret-file", str(self.secret)]
        self.assertEqual(self.run_cmd(*base, "--require", "credential_source_write", "--require", "credential_access:denied"), 0)
        with self.assertRaises(SystemExit):
            self.run_cmd(*base, "--require", "credential_binding_delete")
        rows["audit_logs"][0]["message"] = SECRET
        with self.assertRaises(SystemExit):
            self.run_cmd("audit", "--file", self.f("a", json.dumps(rows)), "--namespace", "ns",
                         "--secret-file", str(self.secret))


if __name__ == "__main__":
    unittest.main()
