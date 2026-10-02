#!/usr/bin/env bash
set -euo pipefail

# This script receives an image already loaded by the CI runner. The public
# reverse proxy terminates TLS and forwards docs.pkg.mcpruntime.org to 8083.
# MkDocs uses 8081 and the articles container uses 8082 on the same host.
image="${PKGSITE_IMAGE:?PKGSITE_IMAGE is required}"
container=mcp-runtime-pkgsite
listen_port=8083
previous_image=""

# Fail before touching the running container if another one holds the port.
port_owner="$(docker ps --filter "publish=${listen_port}" --format '{{.Names}}' | grep -vx "$container" || true)"
if [[ -n "$port_owner" ]]; then
  echo "Host port ${listen_port} is already published by: ${port_owner}" >&2
  exit 1
fi

if docker container inspect "$container" >/dev/null 2>&1; then
  previous_image="$(docker inspect --format '{{.Config.Image}}' "$container")"
  docker rm -f "$container" >/dev/null
fi

start_container() {
  docker run -d --name "$container" \
    --restart unless-stopped \
    --read-only \
    --tmpfs /tmp:rw,nosuid,nodev,size=512m \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    --pids-limit 256 \
    --memory 1536m \
    --cpus 2 \
    -p "${listen_port}:8080" \
    "$1" >/dev/null
}

rollback() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  if [[ -n "$previous_image" ]]; then
    echo "Restoring previous pkgsite image"
    start_container "$previous_image"
  fi
}

if ! start_container "$image"; then
  rollback
  exit 1
fi

for attempt in {1..45}; do
  if docker exec "$container" wget -q -O /dev/null \
    http://127.0.0.1:8080/github.com/mcp-runtime/mcp-runtime/pkg/access; then
    echo "Pkgsite is serving MCP Runtime packages on localhost:${listen_port}"
    # Keep this image and one rollback image; each main build has a unique tag.
    while IFS= read -r old_image; do
      if [[ "$old_image" != "$image" && "$old_image" != "$previous_image" ]]; then
        docker image rm "$old_image" >/dev/null 2>&1 || true
      fi
    done < <(docker image ls mcp-runtime-pkgsite --format '{{.Repository}}:{{.Tag}}')
    exit 0
  fi
  sleep 2
done

docker logs --tail 80 "$container" >&2 || true
rollback
echo "Pkgsite did not become ready; previous image restored when available" >&2
exit 1
