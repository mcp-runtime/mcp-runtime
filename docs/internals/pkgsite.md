# Hosting Go Package Docs

The Go package browser at [docs.pkg.mcpruntime.org](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime)
runs the [official pkgsite server](https://github.com/golang/pkgsite) on the same
host as the MkDocs site. Its image is built from each main-branch source
snapshot by [Go Package Docs](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/deploy-go-docs.yaml).

## How deployment works

The workflow builds a pkgsite `v0.5.0` image using the Go version declared in
the root `go.mod` for both build and runtime stages, checks a real package page in a
temporary container, transfers that exact image over SSH, and starts it on
host port `8083` on the docs host, matching the MkDocs container's host-port
deployment. MkDocs uses `8081` and the articles container uses `8082`. It
reuses the existing `DOCS_DEPLOY_*` secrets. The remote script stops early if
another container already publishes the port, and restores the previous image
if the new container does not serve the package page. The container runs as a non-root user with a
read-only filesystem, a temporary build cache, and no added capabilities.
The PR smoke container uses the production budget: two CPUs, 1536MiB memory,
and 256 processes. `GOMAXPROCS=2` and `GOFLAGS=-p=1` bound Go runtime and
build parallelism during module loading. Readiness uses HTTP from the host
instead of spawning a container process, with up to 90 bounded HTTP probes.
After rollout, the workflow checks public package pages over HTTPS. A missing
Ingress, failed certificate, or broken public route fails the deployment run.

The public route lives on the production k3s cluster, whose Traefik also
serves `docs.mcpruntime.org`. The docs host is that cluster's node, and the
`web/web` Ingress reaches the MkDocs, website, and articles containers through
selectorless Services with a manual EndpointSlice for each host port.
[`hack/deploy/pkgsite/route.yaml`](https://github.com/mcp-runtime/mcp-runtime/blob/main/hack/deploy/pkgsite/route.yaml)
adds the same for pkgsite: a `web/pkgsite` Service and EndpointSlice for port
`8083`, and its own Ingress with a `letsencrypt-prod` certificate
(`docs-pkg-tls`). It never edits `web/web`. Apply it once, with the production
kubeconfig passed explicitly:

```bash
KUBECONFIG=<prod file> kubectl apply --dry-run=server -f hack/deploy/pkgsite/route.yaml
KUBECONFIG=<prod file> kubectl apply -f hack/deploy/pkgsite/route.yaml
KUBECONFIG=<prod file> kubectl get certificate docs-pkg-tls -n web
```

DNS alone does not create the route: without it the host returns Traefik's
404 over HTTP and no certificate over HTTPS. Public traffic should use the
HTTPS hostname rather than port 8083 directly.

After the workflow deploys, check:

```bash
curl -fsSI https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime/pkg/access
curl -fsSI https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime/services/runtime-api
```

On the docs host, read-only diagnostics are:

```bash
docker ps --filter name=mcp-runtime-pkgsite
docker logs --tail 80 mcp-runtime-pkgsite
curl -fsSI http://127.0.0.1:8083/github.com/mcp-runtime/mcp-runtime/pkg/access
```

For a local image build, pass the same module-derived toolchain explicitly:

```bash
go_version="$(awk '$1 == "go" { print $2; exit }' go.mod)"
docker build --build-arg "GO_VERSION=${go_version}" \
  -f hack/deploy/pkgsite/Dockerfile -t mcp-runtime-pkgsite:local .
```

The runtime keeps `GOTOOLCHAIN=local` and `GOPROXY=off`; it does not download a
new toolchain at startup. Changes to module requirements also trigger the
package-docs PR smoke test, so incompatible requirements fail before merge.

## Package paths

Several repository modules use short names such as `mcp-runtime` and
`mcp-runtime-api`. Pkgsite interprets a bare first path segment as a standard
library path. The image therefore rewrites only its **documentation copy** of
each `go.mod` module declaration and local Go import literal to a canonical
URL under `github.com/mcp-runtime/mcp-runtime`. This keeps links between
rendered package symbols working. The source modules used by builds and
releases are not changed. Service and example modules are discovered from
their `go.mod` files, so a new module appears after the next main-branch
deployment without adding it to the startup command.

The page paths are for browsing this source snapshot. Use the import paths in
the checked-in `go.mod` files when building code.
