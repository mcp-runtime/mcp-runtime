#!/usr/bin/env bash

# Only a freshly enrolled session's missing policy is transient. Other auth
# failures remain fatal; the caller bounds retries with its initialize deadline.
mcpruntime_adapter_initialize_pending() {
  local status="$1" body="$2"
  case "${status}" in
    000 | 404 | 502 | 503) return 0 ;;
    401) jq -e '.error == "session_not_found"' "${body}" >/dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

# Reject an existing listener without mistaking closed HTTP connections for one.
mcpruntime_adapter_require_free_port() {
  python3 - "${1##*:}" <<'PORT'
import socket, sys
with socket.socket() as listener:
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        listener.bind(("127.0.0.1", int(sys.argv[1])))
        listener.listen(1)
    except OSError:
        raise SystemExit("Adapter port is occupied; choose a free port. Existing processes are never killed.")
PORT
}
