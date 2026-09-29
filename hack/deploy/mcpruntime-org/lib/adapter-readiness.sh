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
