#!/usr/bin/env bash
# Workstation/code-generation helper. Never creates clusters or changes kube contexts.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
isolated="$(mktemp)"
trap 'rm -f "$isolated"' EXIT
export KUBECONFIG="$isolated"
case "${1:-check}" in
  check) STRICT_DEPS_CHECK=1 bash hack/dev/deps.sh check ;;
  install) make -f Makefile.operator controller-gen kustomize ;;
  generate) make -f Makefile.operator generate manifests ;;
  format) go fmt ./... ;;
  validate) go vet ./... ;;
  all)
    make -f Makefile.operator controller-gen kustomize generate manifests
    go fmt ./...
    go vet ./... ;;
  --help|-h|help)
    echo 'Usage: dev-setup.sh [check|install|generate|format|validate|all]'
    echo 'Default is check. Use mcp-runtime bootstrap/setup or test/e2e/qa-e2e.sh for Kind.' ;;
  *) echo 'Unknown command; see --help' >&2; exit 2 ;;
esac
