#!/bin/sh

set -eu

repo="mcp-runtime/mcp-runtime"
version="${MCP_RUNTIME_VERSION:-latest}"
install_dir="${MCP_RUNTIME_INSTALL_DIR:-${HOME}/.local/bin}"

os="${MCP_RUNTIME_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
arch="$(uname -m)"

case "${os}" in
  darwin|linux) ;;
  *)
    printf 'Unsupported operating system: %s\n' "${os}" >&2
    exit 1
    ;;
esac

case "${arch}" in
  x86_64|x86-64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    printf 'Unsupported architecture: %s\n' "${arch}" >&2
    exit 1
    ;;
esac

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
  fetch() { curl -fsSL --retry 3 "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  printf 'Install curl or wget, then run this installer again.\n' >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
fetch "${url}" "${tmp_dir}/mcp-runtime"
chmod 0755 "${tmp_dir}/mcp-runtime"

mkdir -p "${install_dir}"
install -m 0755 "${tmp_dir}/mcp-runtime" "${install_dir}/mcp-runtime"

printf 'Installed mcp-runtime to %s/mcp-runtime\n' "${install_dir}"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) printf 'Add %s to your PATH to run mcp-runtime from any directory.\n' "${install_dir}" ;;
esac
