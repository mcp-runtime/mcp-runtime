#!/usr/bin/env bash
# Compare committed OpenAPI specs against merge-base for breaking changes (M3.3).
# Requires oasdiff and an explicit base. Usage: bash hack/oasdiff-openapi.sh <base-ref>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

[[ $# == 1 ]] || { echo 'Usage: oasdiff-openapi.sh <base-ref> (use the previous release for intentional breaking-change review)' >&2; exit 2; }
BASE_REF="$(git rev-parse "${1}^{commit}")"
[[ "$BASE_REF" != "$(git rev-parse HEAD)" ]] || { echo 'Base and current revision are identical' >&2; exit 1; }
command -v oasdiff >/dev/null || { echo 'oasdiff is required; install it before running this check' >&2; exit 1; }
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

specs=(
  services/platform-api/openapi.yaml
  services/runtime-api/openapi.yaml
  services/analytics-api/openapi.yaml
)

failed=0
for spec in "${specs[@]}"; do
  if ! git cat-file -e "${BASE_REF}:${spec}" 2>/dev/null; then
    echo "FAIL missing base spec: $spec" >&2
    exit 1
  fi
  base_file="$tmp_dir/$(basename "$(dirname "$spec")").yaml"
  git show "${BASE_REF}:${spec}" >"$base_file"
  echo "=== oasdiff breaking ${spec} (base ${BASE_REF}) ==="
  if ! oasdiff breaking "$base_file" "$spec" -o text; then
    failed=1
  fi
  rm -f "$base_file"
done

if [ "$failed" -ne 0 ]; then
  echo "FAIL oasdiff detected breaking OpenAPI changes"
  exit 1
fi
echo "PASS oasdiff OpenAPI breaking-change check"
