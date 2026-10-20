#!/usr/bin/env bash
# Package the macOS adapter CLI and Dock app for arm64 and amd64.
# Codesign and notarize only when the caller supplies credentials.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="${1:-${ROOT}/dist/macos-adapter}"
IDENTITY="${CODESIGN_IDENTITY:-}"
NOTARY_PROFILE="${NOTARY_KEYCHAIN_PROFILE:-}"

mkdir -p "${OUT}"

build_cli() {
  local goarch="$1"
  local dest="${OUT}/mcp-runtime-darwin-${goarch}"
  CGO_ENABLED=0 GOOS=darwin GOARCH="${goarch}" go build -o "${dest}" ./cmd/mcp-runtime
  echo "built ${dest}"
}

install_app_icon() {
  local app="$1"
  local src="${ROOT}/docs/assets/brand/mcp-runtime-icon.png"
  local iconset
  iconset="$(mktemp -d)/AppIcon.iconset"
  mkdir -p "${iconset}" "${app}/Contents/Resources"
  cp "${src}" "${app}/Contents/Resources/mcp-runtime-icon.png"
  if command -v sips >/dev/null 2>&1 && command -v iconutil >/dev/null 2>&1; then
    for size in 16 32 128 256 512; do
      sips -z "${size}" "${size}" "${src}" --out "${iconset}/icon_${size}x${size}.png" >/dev/null
      local retina=$((size * 2))
      sips -z "${retina}" "${retina}" "${src}" --out "${iconset}/icon_${size}x${size}@2x.png" >/dev/null
    done
    iconutil -c icns "${iconset}" -o "${app}/Contents/Resources/AppIcon.icns"
  fi
}

cd "${ROOT}"
build_cli arm64
build_cli amd64

APP="${OUT}/MCPRuntimeAdapter.app"
mkdir -p "${APP}/Contents/MacOS"
cp desktop/macos/MCPRuntimeAdapter/Info.plist "${APP}/Contents/Info.plist"
install_app_icon "${APP}"
if [[ "$(uname -s)" != "Darwin" ]] || ! command -v swiftc >/dev/null 2>&1; then
  echo "swiftc on macOS is required to package the Dock app" >&2
  exit 1
fi
swiftc -parse-as-library -O \
  -o "${APP}/Contents/MacOS/MCPRuntimeAdapter" \
  desktop/macos/MCPRuntimeAdapter/MCPRuntimeAdapterApp.swift \
  desktop/macos/MCPRuntimeAdapter/ContentView.swift \
  desktop/macos/MCPRuntimeAdapter/InspectView.swift \
  desktop/macos/MCPRuntimeAdapter/ControlClient.swift \
  -framework SwiftUI -framework AppKit
echo "built ${APP}"

lipo -create \
  "${OUT}/mcp-runtime-darwin-arm64" \
  "${OUT}/mcp-runtime-darwin-amd64" \
  -output "${OUT}/mcp-runtime-darwin-universal"
echo "built ${OUT}/mcp-runtime-darwin-universal"

ENTITLEMENTS="${ROOT}/hack/release/macos-adapter/entitlements.plist"
if [[ -n "${IDENTITY}" ]]; then
  codesign --force --options runtime --timestamp --sign "${IDENTITY}" \
    "${OUT}/mcp-runtime-darwin-arm64" \
    "${OUT}/mcp-runtime-darwin-amd64" \
    "${OUT}/mcp-runtime-darwin-universal"
  codesign --force --options runtime --timestamp --entitlements "${ENTITLEMENTS}" \
    --sign "${IDENTITY}" "${APP}/Contents/MacOS/MCPRuntimeAdapter"
  codesign --force --options runtime --timestamp --entitlements "${ENTITLEMENTS}" \
    --sign "${IDENTITY}" "${APP}"
  codesign --verify --strict --verbose=2 "${APP}"
  if [[ -n "${NOTARY_PROFILE}" ]]; then
    ditto -c -k --keepParent "${APP}" "${OUT}/MCPRuntimeAdapter.zip"
    xcrun notarytool submit "${OUT}/MCPRuntimeAdapter.zip" --keychain-profile "${NOTARY_PROFILE}" --wait
    xcrun stapler staple "${APP}"
    ditto -c -k --keepParent "${OUT}/mcp-runtime-darwin-universal" "${OUT}/mcp-runtime-darwin-universal.zip"
    xcrun notarytool submit "${OUT}/mcp-runtime-darwin-universal.zip" --keychain-profile "${NOTARY_PROFILE}" --wait
    xcrun stapler staple "${OUT}/mcp-runtime-darwin-universal"
  else
    echo "signed with ${IDENTITY}; set NOTARY_KEYCHAIN_PROFILE to notarize"
  fi
else
  echo "unsigned: set CODESIGN_IDENTITY to sign and NOTARY_KEYCHAIN_PROFILE to notarize"
fi
