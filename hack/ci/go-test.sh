#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 <module-dir> [go-test-args...]" >&2
  exit 2
fi

# Core kubeconfig discovery can fall back to this host file even with an empty env file.
# Refuse this host shape; run unit tests in an isolated container/CI runner instead.
if [[ -e /etc/rancher/k3s/k3s.yaml ]]; then
  echo 'Refusing unit tests on a host with a system k3s kubeconfig; use an isolated container or CI runner' >&2
  exit 1
fi

test_kubeconfig="$(mktemp)"
trap 'rm -f "$test_kubeconfig"' EXIT
export KUBECONFIG="$test_kubeconfig"

module_dir="$1"
shift

: "${GOCACHE:=$(go env GOCACHE)}"
: "${GOMODCACHE:=$(go env GOMODCACHE)}"
mkdir -p "$GOCACHE" "$GOMODCACHE"
export GOCACHE GOMODCACHE

if [ $# -eq 0 ]; then
  (cd "$module_dir" && go test ./...)
else
  (cd "$module_dir" && go test "$@")
fi
