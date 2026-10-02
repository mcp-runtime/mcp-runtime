#!/bin/sh
set -eu

# The image contains canonical go.mod paths for pkgsite only. Discover service
# and example modules so new modules appear without editing this command.
set -- /src
for module in /src/services/*/go.mod /src/examples/*/go.mod; do
  [ -f "$module" ] || continue
  set -- "$@" "${module%/go.mod}"
done

exec /usr/local/bin/pkgsite -http=:8080 -list=false "$@"
