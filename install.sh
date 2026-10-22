#!/bin/sh

set -eu

blue='' green='' red='' reset=''
if [ -t 2 ] && [ "${TERM:-dumb}" != dumb ] && [ "${NO_COLOR+x}" != x ]; then
  blue=$(printf '\033[36m')
  green=$(printf '\033[32m')
  red=$(printf '\033[31m')
  reset=$(printf '\033[0m')
fi
info() { printf '%s[info]%s %s\n' "$blue" "$reset" "$*" >&2; }
success() { printf '%s[done]%s %s\n' "$green" "$reset" "$*" >&2; }
fail() { printf '%s[error]%s %s\n' "$red" "$reset" "$*" >&2; exit 1; }

with_desktop=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --with-desktop) with_desktop=true ;;
    -h|--help)
      cat <<'HELP'
Usage: sh install.sh [--with-desktop]

Install the MCP Runtime CLI for macOS or Linux.
  --with-desktop  Also install the unsigned Adapter preview (macOS 13+).

MCP_RUNTIME_VERSION      Release tag, or latest (default).
MCP_RUNTIME_INSTALL_DIR  CLI directory (default: $HOME/.local/bin).
MCP_RUNTIME_APP_DIR      App directory (default: $HOME/Applications).

Downloads are verified against the release's SHA256SUMS. Existing desktop apps
are preserved; move the old app aside before reinstalling. The installer does
not open the app, start a service, or change your platform/profile settings.
HELP
      exit 0 ;;
    *) fail "Unknown option: $1. Run sh install.sh --help." ;;
  esac
  shift
done

info 'Installing the MCP Runtime CLI'
repo="mcp-runtime/mcp-runtime"
version="${MCP_RUNTIME_VERSION:-latest}"
install_dir="${MCP_RUNTIME_INSTALL_DIR:-${HOME}/.local/bin}"
app_dir="${MCP_RUNTIME_APP_DIR:-${HOME}/Applications}"

os="${MCP_RUNTIME_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
arch="$(uname -m)"

case "${os}" in
  darwin|linux) ;;
  *)
    fail "Unsupported operating system: ${os}"
    ;;
esac

case "${arch}" in
  x86_64|x86-64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    fail "Unsupported architecture: ${arch}"
    ;;
esac
info "Detected ${os}/${arch}; release: ${version}"

if [ "$with_desktop" = true ]; then
  [ "$os" = darwin ] || fail '--with-desktop requires macOS 13 or later. Omit it to install the CLI on Linux.'
  macos_version=$(sw_vers -productVersion)
  macos_major=${macos_version%%.*}
  case "$macos_major" in ''|*[!0-9]*) fail 'Could not determine the macOS version.' ;; esac
  [ "$macos_major" -ge 13 ] || fail 'The Adapter app requires macOS 13 or later.'
  [ ! -e "$app_dir/MCPRuntimeAdapter.app" ] && [ ! -L "$app_dir/MCPRuntimeAdapter.app" ] ||
    fail "An app already exists at ${app_dir}/MCPRuntimeAdapter.app. Quit it and move it aside before reinstalling; saved profiles are kept separately."
  command -v ditto >/dev/null 2>&1 || fail 'The macOS ditto tool is required to install the app.'
fi

asset="mcp-runtime-${os}-${arch}"
if [ "${os}" = "linux" ] && [ "${arch}" = "arm64" ]; then
  asset="mcp-runtime-linux-arm64"
fi

if [ "${version}" = "latest" ]; then
  release_url="https://github.com/${repo}/releases/latest/download"
else
  release_url="https://github.com/${repo}/releases/download/${version}"
fi
url="${release_url}/${asset}"

if command -v curl >/dev/null 2>&1; then
  fetch() {
    curl -fL --progress-bar --connect-timeout 15 --max-time 600 \
      --speed-limit 1024 --speed-time 60 --retry 3 "$1" -o "$2"
  }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget --timeout=60 --tries=4 "$1" -O "$2"; }
else
  fail 'Install curl or wget, then run this installer again.'
fi

tmp_dir="$(mktemp -d)"
app_stage=''
trap 'rm -rf "$tmp_dir"; if [ -n "$app_stage" ]; then rm -rf "$app_stage"; fi' EXIT
trap 'exit 1' HUP INT TERM
if command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail 'Install sha256sum or shasum to verify release downloads.'
fi
fetch "${release_url}/SHA256SUMS" "${tmp_dir}/SHA256SUMS" || fail 'Could not download release checksums.'
verify() {
  expected=$(awk -v asset="$1" '$2 == asset || $2 == "*" asset {print $1}' "${tmp_dir}/SHA256SUMS")
  case "$expected" in ''|*[!0-9a-fA-F]*) fail "Missing or invalid checksum for $1." ;; esac
  [ "${#expected}" -eq 64 ] || fail "Missing or invalid checksum for $1."
  actual=$(digest "$2")
  [ "$actual" = "$expected" ] || fail "Checksum mismatch for $1. No downloaded files were installed."
}
info "Downloading ${url}"
info 'Slow or failed downloads are retried up to 3 times.'
if ! fetch "${url}" "${tmp_dir}/mcp-runtime"; then
  fail 'Download failed. Check your connection to GitHub and release-assets.githubusercontent.com, then run the installer again.'
fi
success 'Download complete'
verify "$asset" "${tmp_dir}/mcp-runtime"
chmod 0755 "${tmp_dir}/mcp-runtime"

if [ "$with_desktop" = true ]; then
  desktop_asset='MCPRuntimeAdapter-unsigned-preview.zip'
  info 'Downloading the unsigned macOS Adapter preview (Apple Silicon and Intel)'
  fetch "${release_url}/${desktop_asset}" "${tmp_dir}/${desktop_asset}" || fail 'Could not download the desktop app. Choose a release that includes the macOS preview.'
  verify "$desktop_asset" "${tmp_dir}/${desktop_asset}"
  ditto -x -k "${tmp_dir}/${desktop_asset}" "${tmp_dir}/desktop"
  app_source="${tmp_dir}/desktop/MCPRuntimeAdapter.app"
  [ -x "$app_source/Contents/MacOS/MCPRuntimeAdapter" ] &&
    [ -x "$app_source/Contents/Helpers/mcp-runtime" ] || fail 'The desktop archive is missing its app or bundled CLI.'
  mkdir -p "$app_dir" || fail "Cannot create ${app_dir}. Choose a writable MCP_RUNTIME_APP_DIR."
  app_stage=$(mktemp -d "$app_dir/.mcp-runtime-adapter.XXXXXX")
  ditto "$app_source" "$app_stage/MCPRuntimeAdapter.app"
fi

info "Installing to ${install_dir}/mcp-runtime"
if ! mkdir -p "${install_dir}" || ! install -m 0755 "${tmp_dir}/mcp-runtime" "${install_dir}/mcp-runtime"; then
  fail "Cannot install to ${install_dir}. Choose a writable directory with MCP_RUNTIME_INSTALL_DIR."
fi

success "Installed mcp-runtime to ${install_dir}/mcp-runtime"
if [ "$with_desktop" = true ]; then
  [ ! -e "$app_dir/MCPRuntimeAdapter.app" ] && [ ! -L "$app_dir/MCPRuntimeAdapter.app" ] || fail 'The app destination appeared during installation; the existing app was preserved.'
  mv "$app_stage/MCPRuntimeAdapter.app" "$app_dir/MCPRuntimeAdapter.app"
  success "Installed Adapter preview to ${app_dir}/MCPRuntimeAdapter.app"
  info 'Unsigned preview: macOS may require allowing the app in System Settings > Privacy & Security.'
  info 'Open the app and choose Install and start. For an existing service, choose Service > Update service from this app.'
fi
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) info "Add ${install_dir} to your PATH to run mcp-runtime from any directory." ;;
esac
info 'Run mcp-runtime --version to verify the installation.'
