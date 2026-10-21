#!/usr/bin/env bash
# Build the universal macOS adapter CLI and Dock app. Never launches the app,
# daemon, LaunchAgent or contacts a platform.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="${1:-${ROOT}/dist/macos-adapter}"
IDENTITY="${CODESIGN_IDENTITY:-}"
NOTARY_PROFILE="${NOTARY_KEYCHAIN_PROFILE:-}"

if [[ "$(uname -s)" != "Darwin" ]] || ! command -v swiftc >/dev/null 2>&1; then
  echo "swiftc on macOS is required to package the Dock app" >&2
  exit 1
fi
if [[ -n "${IDENTITY}" && -z "${NOTARY_PROFILE}" ]]; then
  echo "signed distribution requires NOTARY_KEYCHAIN_PROFILE for notarization" >&2
  exit 2
fi

mkdir -p "${OUT}"
OUT="$(cd "${OUT}" && pwd)"
BUILD_TMP="$(mktemp -d)"
trap 'rm -rf "${BUILD_TMP}"' EXIT

build_cli() {
  local goarch="$1"
  local dest="${BUILD_TMP}/mcp-runtime-darwin-${goarch}"
  (cd "${ROOT}" && CGO_ENABLED=0 GOOS=darwin GOARCH="${goarch}" go build -o "${dest}" ./cmd/mcp-runtime)
}

install_app_icon() {
  local app="$1"
  local src="${ROOT}/docs/assets/brand/mcp-runtime-icon.png"
  local iconset="${BUILD_TMP}/AppIcon.iconset"
  mkdir -p "${iconset}" "${app}/Contents/Resources"
  cp "${src}" "${app}/Contents/Resources/mcp-runtime-icon.png"
  for size in 16 32 128 256 512; do
    sips -z "${size}" "${size}" "${src}" --out "${iconset}/icon_${size}x${size}.png" >/dev/null
    local retina=$((size * 2))
    sips -z "${retina}" "${retina}" "${src}" --out "${iconset}/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "${iconset}" -o "${app}/Contents/Resources/AppIcon.icns"
}

cd "${ROOT}"
build_cli arm64
build_cli amd64
lipo -create "${BUILD_TMP}/mcp-runtime-darwin-arm64" "${BUILD_TMP}/mcp-runtime-darwin-amd64" \
  -output "${BUILD_TMP}/mcp-runtime"

APP="${BUILD_TMP}/MCPRuntimeAdapter.app"
mkdir -p "${APP}/Contents/MacOS" "${APP}/Contents/Helpers"
cp desktop/macos/Info.plist "${APP}/Contents/Info.plist"
install_app_icon "${APP}"
SDK="$(xcrun --sdk macosx --show-sdk-path)"
SWIFT_SOURCES=(
  desktop/macos/MCPRuntimeAdapter/MCPRuntimeAdapterApp.swift
  desktop/macos/MCPRuntimeAdapter/ContentView.swift
  desktop/macos/MCPRuntimeAdapter/InspectView.swift
  desktop/macos/MCPRuntimeAdapter/ControlClient.swift
)
for swift_arch in arm64 x86_64; do
  swiftc -parse-as-library -swift-version 5 -O -sdk "${SDK}" \
    -target "${swift_arch}-apple-macos13.0" \
    "${SWIFT_SOURCES[@]}" -framework SwiftUI -framework AppKit \
    -o "${BUILD_TMP}/MCPRuntimeAdapter-${swift_arch}"
done
lipo -create "${BUILD_TMP}/MCPRuntimeAdapter-arm64" "${BUILD_TMP}/MCPRuntimeAdapter-x86_64" \
  -output "${APP}/Contents/MacOS/MCPRuntimeAdapter"
cp "${BUILD_TMP}/mcp-runtime" "${APP}/Contents/Helpers/mcp-runtime"
cp "${BUILD_TMP}/mcp-runtime" "${OUT}/mcp-runtime-darwin-universal"
plutil -lint "${APP}/Contents/Info.plist"

if [[ -n "${IDENTITY}" ]]; then
  for executable in "${APP}/Contents/Helpers/mcp-runtime" "${APP}/Contents/MacOS/MCPRuntimeAdapter" \
    "${OUT}/mcp-runtime-darwin-universal"; do
    codesign --force --options runtime --timestamp --sign "${IDENTITY}" "${executable}"
  done
  codesign --force --options runtime --timestamp --sign "${IDENTITY}" "${APP}"
  codesign --verify --strict --deep --verbose=2 "${APP}"
  codesign --verify --strict --verbose=2 "${OUT}/mcp-runtime-darwin-universal"

  ditto -c -k --keepParent "${APP}" "${BUILD_TMP}/MCPRuntimeAdapter-notary.zip"
  xcrun notarytool submit "${BUILD_TMP}/MCPRuntimeAdapter-notary.zip" \
    --keychain-profile "${NOTARY_PROFILE}" --wait --timeout 30m
  xcrun stapler staple "${APP}"
  xcrun stapler validate "${APP}"
  spctl --assess --type execute --verbose=2 "${APP}"

  ditto -c -k --keepParent "${OUT}/mcp-runtime-darwin-universal" \
    "${BUILD_TMP}/mcp-runtime-darwin-universal-notary.zip"
  xcrun notarytool submit "${BUILD_TMP}/mcp-runtime-darwin-universal-notary.zip" \
    --keychain-profile "${NOTARY_PROFILE}" --wait --timeout 30m
  ditto -c -k --keepParent "${APP}" "${OUT}/MCPRuntimeAdapter-notarized.zip"
  ditto -c -k --keepParent "${OUT}/mcp-runtime-darwin-universal" \
    "${OUT}/mcp-runtime-darwin-universal-notarized.zip"
else
  # Explicitly mark ad-hoc signed preview artifacts; they are not releases.
  codesign --force --sign - "${APP}/Contents/Helpers/mcp-runtime"
  codesign --force --sign - "${APP}/Contents/MacOS/MCPRuntimeAdapter"
  codesign --force --sign - "${APP}"
  codesign --verify --strict --deep "${APP}"
  codesign --force --sign - "${OUT}/mcp-runtime-darwin-universal"
  codesign --verify --strict "${OUT}/mcp-runtime-darwin-universal"
  ditto -c -k --keepParent "${APP}" "${OUT}/MCPRuntimeAdapter-unsigned-preview.zip"
  ditto -c -k --keepParent "${OUT}/mcp-runtime-darwin-universal" \
    "${OUT}/mcp-runtime-darwin-universal-unsigned-preview.zip"
fi

echo "packaged universal macOS adapter artifacts in ${OUT}"
