#!/usr/bin/env bash
# Development dependency helpers: Go modules, tool checks, optional OS package installs.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# Minimum Go for the main module (align with go.mod "go" directive).
REQUIRED_GO_MAJOR=1
REQUIRED_GO_MINOR=25

log() { printf '%s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }

go_version_ok() {
	if ! command -v go >/dev/null 2>&1; then
		return 1
	fi
	# e.g. "go version go1.25.0 darwin/arm64" or "go1.25.0"
	local ver
	ver=$(go version | awk '{print $3}' | sed 's/^go//')
	local major minor
	IFS=. read -r major minor _ <<<"$ver"
	[[ -n "${major:-}" && -n "${minor:-}" ]] || return 1
	if (( major > REQUIRED_GO_MAJOR )); then return 0; fi
	if (( major < REQUIRED_GO_MAJOR )); then return 1; fi
	if (( minor >= REQUIRED_GO_MINOR )); then return 0; fi
	return 1
}

cmd_go_modules() {
	log "Resolving Go modules (root)..."
	go mod download
	go mod tidy

	local mods=(
		"examples/oauth-example-go-2025-11-25"
		"services/platform-api"
		"services/runtime-api"
		"services/analytics-api"
		"services/ingest"
		"services/mcp-gateway"
		"services/processor"
		"services/ui"
		"traefik-plugins/pii-redactor"
		"traefik-plugins/spiffe-identity"
	)
	for mod in "${mods[@]}"; do
		if [[ -f "$mod/go.mod" ]]; then
			log "Resolving Go modules ($mod)..."
			( cd "$mod" && go mod download )
		fi
	done
	log "Go modules are ready."
}

cmd_check() {
	local missing=0
	if go_version_ok; then
		log "Go: $(go version) (ok)"
	else
		warn "Go 1.${REQUIRED_GO_MINOR}+ is required (https://go.dev/dl/)."
		missing=1
	fi
	if command -v docker >/dev/null 2>&1; then
		log "docker: present ($(command -v docker))"
		if docker_daemon_reachable; then
			log "docker daemon: reachable"
		else
			warn "docker is installed, but the daemon is not reachable. Start Docker Desktop, Colima, dockerd, or your compatible runtime before setup/image builds."
			missing=1
		fi
	else
		warn "docker is not in PATH. Image builds and mcp-runtime setup (non test-mode) need a Docker (or compatible) client."
		missing=1
	fi
	if command -v make >/dev/null 2>&1; then
		log "make: present ($(command -v make))"
	else
		warn "make not found. Install GNU/BSD make before running documented make targets."
		missing=1
	fi
	if command -v kubectl >/dev/null 2>&1; then
		log "kubectl: present ($(command -v kubectl))"
	elif command -v k3s >/dev/null 2>&1; then
		warn "kubectl not found. k3s provides 'k3s kubectl', but this repo and CI invoke plain kubectl."
		missing=1
	else
		warn "kubectl not found. Install kubernetes CLI or use a cluster admin kubeconfig on this host."
		missing=1
	fi
	if command -v curl >/dev/null 2>&1; then
		log "curl: present ($(command -v curl))"
	else
		warn "curl not in PATH (used by e2e and many docs examples)."
		missing=1
	fi
	if command -v jq >/dev/null 2>&1; then
		log "jq: present ($(command -v jq))"
	else
		warn "jq not in PATH (AGENTS.md and scripts use it)."
		missing=1
	fi
	if command -v python3 >/dev/null 2>&1; then
		log "python3: present ($(command -v python3))"
	else
		warn "python3 not in PATH (e2e and traffic generation use it)."
		missing=1
	fi
	if command -v kind >/dev/null 2>&1; then
		log "kind: present ($(command -v kind)) (optional local clusters)"
	else
		warn "kind not in PATH (optional; see AGENTS.md for Kind-based dev)"
	fi
	if [[ ${STRICT_DEPS_CHECK:-0} == 1 ]] && (( missing )); then
		return 1
	fi
	return 0
}

have_apt() {
	command -v apt-get >/dev/null 2>&1
}

have_brew() {
	command -v brew >/dev/null 2>&1
}

docker_daemon_reachable() {
	if command -v timeout >/dev/null 2>&1; then
		timeout 5 docker info >/dev/null 2>&1
		return $?
	fi
	if command -v gtimeout >/dev/null 2>&1; then
		gtimeout 5 docker info >/dev/null 2>&1
		return $?
	fi
	docker info >/dev/null 2>&1 &
	local pid=$!
	local waited=0
	while kill -0 "$pid" >/dev/null 2>&1; do
		if (( waited >= 5 )); then
			kill "$pid" >/dev/null 2>&1 || true
			wait "$pid" >/dev/null 2>&1 || true
			return 1
		fi
		sleep 1
		waited=$((waited + 1))
	done
	wait "$pid" >/dev/null 2>&1
}

cmd_install() {
	local missing_basics=()
	for tool in make curl jq python3; do
		if ! command -v "$tool" >/dev/null 2>&1; then
			missing_basics+=("$tool")
		fi
	done
	if ((${#missing_basics[@]})); then
		if have_brew; then
			for tool in "${missing_basics[@]}"; do
				case "$tool" in
				python3) brew install python ;;
				make) warn "Install command line tools or GNU make for this macOS host." ;;
				*) brew install "$tool" ;;
				esac
			done
		elif have_apt; then
			if [[ "$(id -u)" -eq 0 ]]; then
				apt-get update
				apt-get install -y "${missing_basics[@]}"
			else
				log "Install basics: sudo apt-get update && sudo apt-get install -y ${missing_basics[*]}"
			fi
		else
			warn "Missing basic tools: ${missing_basics[*]}. Install them with your OS package manager (for example dnf, pacman, apk, zypper) or use a supported Homebrew/apt-based image."
		fi
	fi
	if go_version_ok; then
		log "Go: ok ($(go version | awk '{print $3}'))"
	else
		if have_brew; then
			log "Installing Go with Homebrew..."
			brew install go
		elif have_apt; then
			warn "Go 1.25+ is not in default Debian/Ubuntu apt on many versions."
			warn "Install from https://go.dev/dl/ or use Homebrew; then re-run: make deps-check"
		else
			warn "Install Go 1.25+ from https://go.dev/dl/ for this OS."
		fi
	fi
	if ! command -v docker >/dev/null 2>&1; then
		if have_brew; then
			warn "Install a container runtime, then a Docker client (Docker Desktop, Colima, or: brew install docker; follow brew caveats to start the daemon)."
		elif have_apt; then
			if [[ "$(id -u)" -eq 0 ]]; then
				apt-get update
				apt-get install -y docker.io
				log "Installed docker.io. Add your user to the docker group if needed: usermod -aG docker \"\$USER\""
			else
				log "Run with sudo to install docker.io, or: sudo apt-get update && sudo apt-get install -y docker.io"
			fi
		else
			warn "Install Docker or a Docker-compatible CLI (e.g. podman-docker). See https://docs.docker.com/engine/install/"
		fi
	else
		log "docker: already installed."
	fi
	if ! command -v kubectl >/dev/null 2>&1; then
		if have_apt; then
			if [[ "$(id -u)" -eq 0 ]]; then
				apt-get update
				apt-get install -y kubectl || warn "Package kubectl not found; your distro may use a different package name."
			else
				log "Install kubectl, e.g.: sudo apt-get update && sudo apt-get install -y kubectl"
			fi
		elif have_brew; then
			brew install kubectl
		else
			warn "Install kubectl: https://kubernetes.io/docs/tasks/tools/"
		fi
	else
		log "kubectl: already installed."
	fi
	if ! command -v kind >/dev/null 2>&1; then
		if have_brew; then
			brew install kind
		else
			warn "kind is optional but recommended for local verification. Install it from https://kind.sigs.k8s.io/docs/user/quick-start/"
		fi
	else
		log "kind: already installed."
	fi
}

usage() {
	log "Usage: $0 {go|check|install}"
	log "  go      Download and tidy the main module; download nested service/example modules"
	log "  check   Verify go, docker daemon, kubectl, make, curl, jq, python3, and optional kind; STRICT_DEPS_CHECK=1 fails on required misses"
	log "  install Best-effort install of go, docker client, kubectl, make, curl, jq, python3, and kind on Homebrew/apt hosts"
}

main() {
	case "${1:-}" in
	go) cmd_go_modules ;;
	check) cmd_check ;;
	install) cmd_install ;;
	*) usage; exit 1 ;;
	esac
}

main "$@"
