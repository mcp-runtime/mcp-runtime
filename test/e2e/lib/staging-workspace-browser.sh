# shellcheck shell=bash
# Called only after the existing disposable target guard and CLI-created
# multitenancy fixtures. No provider credentials or browser state are persisted.
staging_check_workspace_browser() (
  set -euo pipefail
  if ! staging_flag_enabled "${E2E_RUN_MULTITENANCY:-1}"; then
    staging_skip "workspace browser roles require the CLI-created multitenancy fixtures"
  fi
  local image='mcr.microsoft.com/playwright:v1.64.1-noble@sha256:7852dc5a7d72f5fe458a597d690f6c42fbc8e9e26ddfa95f8e6ef552558d9bbf'
  local directory="${WORK_DIR}/workspace-browser" output="${STAGING_ARTIFACT_DIR}/workspace-browser"
  mkdir -p "${directory}/deps" "${output}"
  chmod 700 "${directory}" "${directory}/deps" "${output}"
  python3 - "${ROOT_DIR}/services/ui/frontend/package-lock.json" <<'PIN'
import json, sys
assert json.load(open(sys.argv[1]))["packages"]["node_modules/playwright"]["version"] == "1.64.1", "update reviewed Playwright image pin with the frontend lockfile"
PIN
  cp "${ROOT_DIR}/services/ui/frontend/package.json" "${ROOT_DIR}/services/ui/frontend/package-lock.json" "${directory}/deps/"
  # The Docker command line contains paths and public tool pins only. The
  # generated account passwords reach Node solely through its stdin pipe.
  python3 "${ROOT_DIR}/test/e2e/workspace_browser_fixture.py" |
    docker run --rm --interactive --init --network host --ipc host --cap-drop ALL --security-opt no-new-privileges \
      --user "$(id -u):$(id -g)" --workdir /workspace \
      --env HOME=/tmp --env PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
      --env NODE_PATH=/workspace/node_modules --env WORKSPACE_BROWSER_ARTIFACTS=/artifacts \
      --mount "type=bind,src=${ROOT_DIR},dst=/repo,readonly" \
      --mount "type=bind,src=${directory}/deps,dst=/workspace" \
      --mount "type=bind,src=${output},dst=/artifacts" \
      --entrypoint /bin/bash "${image}" -lc \
      'npm ci --ignore-scripts --no-audit --no-fund </dev/null >&2 && node /repo/test/e2e/workspace_browser.cjs'
)
