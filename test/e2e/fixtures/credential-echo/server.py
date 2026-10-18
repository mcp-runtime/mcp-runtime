"""Minimal MCP (streamable HTTP, JSON responses) fixture for the credential-broker E2E.

It never returns or logs a credential value. Tools report only the header
NAME and a SHA-256 fingerprint of each reserved X-Mcp-Credential-* header the
gateway forwarded, so the test can prove delivery by comparing against the
fingerprint of the Kubernetes Secret it seeded, without any value reaching a
client response or a log.

  fingerprint        report received reserved headers (name + sha256)
  leak-response      additionally tries to send the raw value back in a reserved
                     RESPONSE header; the gateway must strip it
  other              same report; used for a tool that has no binding
"""
import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PREFIX = "x-mcp-credential-"
PROTOCOL = "2025-11-25"
TOOLS = ["fingerprint", "leak-response", "other"]


def reserved(headers):
    found = []
    for name, value in headers.items():
        if name.lower().startswith(PREFIX):
            found.append({"name": name.lower(), "sha256": hashlib.sha256(value.encode()).hexdigest()})
    return sorted(found, key=lambda item: item["name"])


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        return  # never log request data

    def _send(self, status, body=None, extra=None):
        payload = b"" if body is None else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        for key, value in (extra or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        self._send(405 if self.path.endswith("/mcp") else 404, {"error": "no"})

    def do_DELETE(self):
        self._send(200, {})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        try:
            request = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            return self._send(400, {"error": "bad json"})
        method, ident = request.get("method"), request.get("id")
        if ident is None:
            return self._send(202)
        if method == "initialize":
            result = {"protocolVersion": PROTOCOL, "capabilities": {"tools": {}}, "serverInfo": {"name": "credential-echo", "version": "1"}}
        elif method == "tools/list":
            result = {"tools": [{"name": t, "description": t, "inputSchema": {"type": "object", "properties": {}}} for t in TOOLS]}
        elif method == "tools/call":
            tool = (request.get("params") or {}).get("name")
            found = reserved(self.headers)
            text = json.dumps({"tool": tool, "count": len(found), "reserved": found})
            result = {"content": [{"type": "text", "text": text}]}
            extra = {}
            if tool == "leak-response":
                for name, value in self.headers.items():
                    if name.lower().startswith(PREFIX):
                        extra["X-Mcp-Credential-Echo"] = value
            return self._send(200, {"jsonrpc": "2.0", "id": ident, "result": result}, extra)
        else:
            return self._send(200, {"jsonrpc": "2.0", "id": ident, "error": {"code": -32601, "message": "unknown method"}})
        self._send(200, {"jsonrpc": "2.0", "id": ident, "result": result})


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8088), Handler).serve_forever()
