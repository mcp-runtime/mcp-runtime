# Website

Minimal Flask landing page for MCP Runtime. The site positions the product as a
Kubernetes-native control plane to deploy, govern, and broker MCP servers, and
serves a single home page with links to GitHub, articles, and the documentation
site.

Documentation lives at `docs.mcpruntime.org` (deployed separately) and is
authored as Markdown under [`../docs/`](../docs/) at the repo root. Any
`/docs*` request to this site 302-redirects to `MCP_DOCS_URL`.

Articles live at `articles.mcpruntime.org` (deployed separately) and are
authored under [`../articles/`](../articles/) at the repo root.

## Run locally

```sh
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python3 app.py
```

Then open <http://localhost:8080>.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `MCP_DOCS_URL` | `https://docs.mcpruntime.org/` | Target for the Docs link and `/docs*` redirects. |
| `MCP_PLATFORM_URL` | `https://platform.mcpruntime.org/` | Target for the public hosted platform link shown on the landing page. |
| `MCP_ARTICLES_URL` | `https://articles.mcpruntime.org/` | Target for the Articles link. |
| `MCP_WEBSITE_BASE_URL` | derived from request | Trusted canonical origin for OG/sitemap URLs. |

## Docker

```sh
docker build -t mcp-runtime-website .
docker run --rm -p 8080:8080 mcp-runtime-website
```

## Production deploy (GitHub Actions)

The `deploy-website` job in [`.github/workflows/ci.yaml`](../.github/workflows/ci.yaml)
syncs `website/` to your remote host and, by default, builds/runs a Docker
container there. On `main`, website-only changes deploy as soon as the path
filter detects changes under `website/`; the deploy job does not wait for Go
unit, integration, or QA E2E jobs.

```sh
docker build -t mcp-runtime-website:latest .
docker rm -f mcp-runtime-website || true
docker run -d --name mcp-runtime-website \
  --restart unless-stopped \
  -p 8080:8080 \
  -e MCP_DOCS_URL=https://docs.mcpruntime.org/ \
  -e MCP_ARTICLES_URL=https://articles.mcpruntime.org/ \
  -e MCP_WEBSITE_BASE_URL=https://mcpruntime.org \
  mcp-runtime-website:latest
```

Required GitHub secrets:

- `WEBSITE_DEPLOY_HOST`
- `WEBSITE_DEPLOY_USER`
- `WEBSITE_DEPLOY_PATH`
- `WEBSITE_DEPLOY_SSH_KEY`

Optional GitHub secrets:

- `WEBSITE_DEPLOY_HOST_KEY`: pinned SSH host key for `WEBSITE_DEPLOY_HOST`. Use either a full known-hosts line such as `203.0.113.10 ssh-ed25519 AAAA...` or a bare host key such as `ssh-ed25519 AAAA...`. If omitted or malformed, CI falls back to `ssh-keyscan`.
- `WEBSITE_DOCS_URL` (default: `https://docs.mcpruntime.org/`)
- `WEBSITE_ARTICLES_URL` (default: `https://articles.mcpruntime.org/`)
- `WEBSITE_BASE_URL` (default: `https://mcpruntime.org`)
- `WEBSITE_HOST_PORT` (default: `8080`)
- `WEBSITE_CONTAINER_PORT` (default: `8080`)
- `WEBSITE_CONTAINER_NAME` (default: `mcp-runtime-website`)
- `WEBSITE_IMAGE_NAME` (default: `mcp-runtime-website:latest`)
- `WEBSITE_DEPLOY_COMMAND` (if set, CI runs this instead of the default
  Docker build/run sequence)

## Files

- `app.py`: Flask app (home, robots, sitemap, `/docs*` redirect).
- `templates/base.html`: shared shell (header, footer, security headers).
- `templates/index.html`: single landing page.
- `static/style.css`: page styles.
- `static/favicon.png`: transparent hexagon-and-cube mark, the same mark as the GitHub org avatar.
- `static/brand/`: light/dark logo lockups and app icon cut from that mark, plus the website/social banner. The same files are copied into `docs/assets/`, `articles/static/`, and `services/ui/{frontend/public,static}/`.
- `Dockerfile`: container build.
