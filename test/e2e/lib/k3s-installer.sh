#!/usr/bin/env bash

# Download completely before executing. The fallback is the official installer
# reviewed on 2026-09-29, pinned so upstream changes require an explicit update.
staging_download_k3s_installer() {
  local destination="$1" source
  for source in \
    https://get.k3s.io \
    https://raw.githubusercontent.com/k3s-io/k3s/fb46cc3da277692c5ec9ec6da20aaaf63f256864/install.sh; do
    if curl --fail --show-error --silent --location --retry 3 --retry-delay 2 \
      --connect-timeout 15 --max-time 120 --output "${destination}" "${source}" &&
      [[ -s "${destination}" ]] && sh -n "${destination}"; then
      return 0
    fi
    printf 'k3s installer download failed: %s\n' "${source}" >&2
  done
  rm -f "${destination}"
  return 1
}
