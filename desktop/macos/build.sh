#!/usr/bin/env bash
# Compile/package only: never launches the app, daemon or launchctl.
set -euo pipefail

desktop_arch=${1:?Usage: build.sh arm64|amd64 OUTPUT_DIRECTORY [--sign]}
desktop_output=${2:?Output directory is required}
desktop_mode=${3:-unsigned}
case "$desktop_arch" in
  arm64) desktop_swift_arch=arm64 ;;
  amd64) desktop_swift_arch=x86_64 ;;
  *) echo 'Architecture must be arm64 or amd64' >&2; exit 2 ;;
esac
case "$desktop_mode" in unsigned|--sign) ;; *) echo 'Use --sign for signed/notarized distribution' >&2; exit 2 ;; esac
[[ $(uname -s) == Darwin ]] || { echo 'Build on a macOS runner with Xcode command-line tools' >&2; exit 2; }
desktop_source=$(cd "$(dirname "$0")" && pwd)
desktop_root=$(cd "$desktop_source/../.." && pwd)
mkdir -p "$desktop_output"
desktop_output=$(cd "$desktop_output" && pwd)
desktop_build_tmp=$(mktemp -d)
trap 'rm -rf "$desktop_build_tmp"' EXIT
desktop_app="$desktop_build_tmp/MCP Runtime Adapter.app"
mkdir -p "$desktop_app/Contents/MacOS"
cp "$desktop_source/Info.plist" "$desktop_app/Contents/Info.plist"
desktop_sdk=$(xcrun --sdk macosx --show-sdk-path)
xcrun swiftc -parse-as-library -swift-version 5 -O \
  -sdk "$desktop_sdk" -target "$desktop_swift_arch-apple-macos13.0" \
  "$desktop_source/AdapterCompanion.swift" -o "$desktop_app/Contents/MacOS/MCPRuntimeAdapter"
(
  cd "$desktop_root"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$desktop_arch" \
    go build -o "$desktop_app/Contents/MacOS/mcp-runtime" ./cmd/mcp-runtime
)
plutil -lint "$desktop_app/Contents/Info.plist"
desktop_stem="mcp-runtime-adapter-macos-$desktop_arch"
if [[ "$desktop_mode" == --sign ]]; then
  : "${MCP_DESKTOP_SIGNING_IDENTITY:?Set a Developer ID Application signing identity already installed in Keychain}"
  : "${MCP_DESKTOP_NOTARY_PROFILE:?Set an existing notarytool Keychain profile}"
  # Sign nested code first; no --deep signing or relaxed entitlements.
  codesign --force --options runtime --timestamp --sign "$MCP_DESKTOP_SIGNING_IDENTITY" "$desktop_app/Contents/MacOS/mcp-runtime"
  codesign --force --options runtime --timestamp --sign "$MCP_DESKTOP_SIGNING_IDENTITY" "$desktop_app/Contents/MacOS/MCPRuntimeAdapter"
  codesign --force --options runtime --timestamp --sign "$MCP_DESKTOP_SIGNING_IDENTITY" "$desktop_app"
  codesign --verify --deep --strict "$desktop_app"
  ditto -c -k --keepParent "$desktop_app" "$desktop_build_tmp/notary-upload.zip"
  xcrun notarytool submit "$desktop_build_tmp/notary-upload.zip" \
    --keychain-profile "$MCP_DESKTOP_NOTARY_PROFILE" --wait --timeout 30m
  xcrun stapler staple "$desktop_app"
  xcrun stapler validate "$desktop_app"
  spctl --assess --type execute "$desktop_app"
  ditto -c -k --keepParent "$desktop_app" "$desktop_output/$desktop_stem-notarized.zip"
else
  # Preview builds are intentionally labelled; never pass them off as releases.
  codesign --force --sign - "$desktop_app/Contents/MacOS/mcp-runtime"
  codesign --force --sign - "$desktop_app/Contents/MacOS/MCPRuntimeAdapter"
  codesign --force --sign - "$desktop_app"
  codesign --verify --deep --strict "$desktop_app"
  ditto -c -k --keepParent "$desktop_app" "$desktop_output/$desktop_stem-unsigned-preview.zip"
fi
