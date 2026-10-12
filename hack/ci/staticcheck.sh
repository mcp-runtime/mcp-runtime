#!/usr/bin/env bash
set -euo pipefail

gopath="$(go env GOPATH)"
bin="${gopath}/bin/staticcheck"

if [[ ! -x "${bin}" ]]; then
  echo "staticcheck not found at ${bin}. Install with:" >&2
  echo "  go install honnef.co/go/tools/cmd/staticcheck@v0.7.0" >&2
  exit 1
fi

exec "${bin}" "$@"
