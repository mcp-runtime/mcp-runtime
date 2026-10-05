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

info 'Installing the MCP Runtime CLI'
repo="mcp-runtime/mcp-runtime"
version="${MCP_RUNTIME_VERSION:-latest}"
install_dir="${MCP_RUNTIME_INSTALL_DIR:-${HOME}/.local/bin}"

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

asset="mcp-runtime-${os}-${arch}"
if [ "${os}" = "linux" ] && [ "${arch}" = "arm64" ]; then
  asset="mcp-runtime-linux-arm64"
fi

if [ "${version}" = "latest" ]; then
  url="https://github.com/${repo}/releases/latest/download/${asset}"
else
  url="https://github.com/${repo}/releases/download/${version}/${asset}"
fi

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
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
info "Downloading ${url}"
info 'Slow or failed downloads are retried up to 3 times.'
if ! fetch "${url}" "${tmp_dir}/mcp-runtime"; then
  fail 'Download failed. Check your connection to GitHub and release-assets.githubusercontent.com, then run the installer again.'
fi
success 'Download complete'
chmod 0755 "${tmp_dir}/mcp-runtime"

info "Installing to ${install_dir}/mcp-runtime"
if ! mkdir -p "${install_dir}" || ! install -m 0755 "${tmp_dir}/mcp-runtime" "${install_dir}/mcp-runtime"; then
  fail "Cannot install to ${install_dir}. Choose a writable directory with MCP_RUNTIME_INSTALL_DIR."
fi

success "Installed mcp-runtime to ${install_dir}/mcp-runtime"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) info "Add ${install_dir} to your PATH to run mcp-runtime from any directory." ;;
esac
info 'Run mcp-runtime --version to verify the installation.'
