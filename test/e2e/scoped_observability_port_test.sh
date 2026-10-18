#!/usr/bin/env bash
# The scoped UI client dials the Traefik port-forward. Server deploy can drop
# that forward, and Python retries then keep hitting a closed port.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
calls=0
recover_traefik_port_forward_if_needed() { calls=$((calls + 1)); }
# shellcheck source=test/e2e/scenarios/scoped-observability.sh
source "${SCRIPT_DIR}/scenarios/scoped-observability.sh"
ensure_scoped_observability_ui
[[ "${calls}" == 1 ]]

python3 - "${SCRIPT_DIR}/scenarios/scoped-observability.sh" <<'PY'
from pathlib import Path
import sys
text = Path(sys.argv[1]).read_text(encoding="utf-8")
restore = text.find("ensure_scoped_observability_ui")
client = text.find('python3 "${PROJECT_ROOT}/test/e2e/scoped_observability.py"')
if restore < 0 or client < 0 or restore > client:
    raise SystemExit("Traefik port-forward must be restored before the scoped UI client")
PY

e2e_adapter_certificates_enabled() { return 0; }
TRAEFIK_PORT=18080
TRAEFIK_TLS_PORT=18443
[[ "$(scoped_mcp_origin)" == "https://127.0.0.1:18443" ]]
e2e_adapter_certificates_enabled() { return 1; }
[[ "$(scoped_mcp_origin)" == "http://127.0.0.1:18080" ]]

echo "[pass] scoped observability restores the Traefik port-forward before the UI client"
