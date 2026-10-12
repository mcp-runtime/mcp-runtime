#!/usr/bin/env bash
# Shared guard for live contributor tests. Sourcing does not access a cluster.
require_test_cluster() {
  : "${KUBECONFIG:?Set an explicit contributor kubeconfig}"
  local context
  context="$(kubectl --kubeconfig "$KUBECONFIG" config current-context)"
  case "$context" in
    kind-*|test-mcp-runtime) ;;
    *) echo "error: live tests require a contributor context, got $context" >&2; return 1 ;;
  esac
  # Context names can be renamed: require an actual Kind node provider ID too.
  kubectl --kubeconfig "$KUBECONFIG" get nodes -o json | jq -e \
    '(.items | length) > 0 and all(.items[]; (.spec.providerID // "") | startswith("kind://"))' >/dev/null || {
    echo "error: live tests require Kind nodes" >&2; return 1;
  }
}

require_test_url() {
  python3 - "$1" <<'URL'
import sys
from urllib.parse import urlsplit
url = urlsplit(sys.argv[1])
if url.scheme not in {"http", "https"} or url.hostname not in {"localhost", "127.0.0.1", "::1"}:
    raise SystemExit("Live test URLs must be explicit loopback endpoints")
URL
}
