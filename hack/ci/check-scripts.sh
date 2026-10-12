#!/usr/bin/env bash
# Offline automation gate: no Docker daemon, SSH target or Kubernetes context required.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
for tool in python3 shellcheck; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 1; }
done
python3 -m unittest discover -s hack/tests -p '*_test.py'
python3 hack/deploy/pkgsite/deploy_remote_test.py
while IFS= read -r -d '' script; do
  bash -n "$script"
  shellcheck -x "$script"
done < <(find hack -type f -name '*.sh' -print0)
echo 'PASS: automation behavior, pkgsite rollback, shell syntax and ShellCheck'
