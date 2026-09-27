#!/usr/bin/env bash
set -euo pipefail

# Build a tooling image and run the e2e script inside it.
# Requires Docker on the host; the container uses the host docker daemon via /var/run/docker.sock.

IMAGE_NAME="${IMAGE_NAME:-mcp-runtime-e2e}"
DOCKER_CONFIG="${DOCKER_CONFIG:-/tmp/docker-config}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

mkdir -p "${DOCKER_CONFIG}"

echo "[build] building e2e tooling image ${IMAGE_NAME}"
DOCKER_CONFIG="${DOCKER_CONFIG}" docker build -f test/e2e/Dockerfile -t "${IMAGE_NAME}" .

echo "[run] executing qa-e2e.sh inside container"
if stat -c '%g' /var/run/docker.sock >/dev/null 2>&1; then
  DOCKER_SOCK_GID="$(stat -c '%g' /var/run/docker.sock)"
elif stat -f '%g' /var/run/docker.sock >/dev/null 2>&1; then
  DOCKER_SOCK_GID="$(stat -f '%g' /var/run/docker.sock)"
else
  DOCKER_SOCK_GID=999
fi
DOCKER_FLAGS=(-i --rm --privileged)
if [ -t 0 ]; then
  DOCKER_FLAGS=(-it --rm --privileged)
fi
DOCKER_BASE_ARGS=(--network=host -v /var/run/docker.sock:/var/run/docker.sock --group-add "${DOCKER_SOCK_GID}")
MOUNT_ARGS=(-v "${ROOT}":/workspace -w /workspace)

# Test if bind mounts work with a simple check first (suppress errors for clean fallback)
if DOCKER_CONFIG="${DOCKER_CONFIG}" docker run --rm "${DOCKER_BASE_ARGS[@]}" \
  "${MOUNT_ARGS[@]}" "${IMAGE_NAME}" -lc "test -d /workspace" 2>/dev/null; then
  echo "[run] bind mount available, using host workspace"
  DOCKER_CONFIG="${DOCKER_CONFIG}" docker run "${DOCKER_FLAGS[@]}" "${DOCKER_BASE_ARGS[@]}" \
    "${MOUNT_ARGS[@]}" "${IMAGE_NAME}" \
    test/e2e/qa-e2e.sh
else
  echo "[warn] bind mount unavailable, running from image contents"
  DOCKER_CONFIG="${DOCKER_CONFIG}" docker run "${DOCKER_FLAGS[@]}" "${DOCKER_BASE_ARGS[@]}" \
    "${IMAGE_NAME}" \
    test/e2e/qa-e2e.sh
fi
