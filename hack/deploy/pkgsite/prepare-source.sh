#!/bin/sh
set -eu

# pkgsite treats bare module names such as "mcp-runtime" as standard-library
# paths. Give the image's source snapshot canonical paths without changing the
# buildable modules in the repository.
source_root="${1:-/src}"
canonical_root=github.com/mcp-runtime/mcp-runtime

for modfile in "$source_root/go.mod" "$source_root"/services/*/go.mod "$source_root"/examples/*/go.mod; do
  [ -f "$modfile" ] || continue
  relative="${modfile#"$source_root"/}"
  relative="${relative%/go.mod}"
  if [ "$relative" = go.mod ]; then
    canonical="$canonical_root"
  else
    canonical="$canonical_root/$relative"
  fi
  sed -i "1s|^module .*|module $canonical|" "$modfile"
done
