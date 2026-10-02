# Hosting Go Package Docs

The Go package browser at [docs.pkg.mcpruntime.org](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime)
runs the [official pkgsite server](https://github.com/golang/pkgsite) on the same
host as the MkDocs site. Its image is built from each main-branch source
snapshot by [Go Package Docs](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/deploy-go-docs.yaml).
The existing [generated Go reference](go-package-reference.md) remains a
checked-in, reviewable snapshot of selected packages.

## How deployment works

The workflow builds a pkgsite `v0.5.0` image, checks a real package page in a
temporary container, transfers that exact image over SSH, and starts it on
host port `8082` on the docs host, matching the MkDocs container's host-port
deployment. It reuses the existing `DOCS_DEPLOY_*`
secrets. The remote script restores the previous image if the new container
does not serve the package page. The container runs as a non-root user with a
read-only filesystem, a temporary build cache, and no added capabilities.

Add a `docs.pkg.mcpruntime.org` host rule to the **same public reverse proxy**
that already routes `docs.mcpruntime.org`. Use the docs host and port `8082`
as the backend, and issue a certificate for the new hostname. A proxy on the
same host can use `http://127.0.0.1:8082`; a remote or containerized proxy
must use the address it already uses to reach the MkDocs host on port 8081.
If the proxy is host-level Nginx, use
[`reverse-proxy.nginx.example.conf`](https://github.com/mcp-runtime/mcp-runtime/blob/main/hack/deploy/pkgsite/reverse-proxy.nginx.example.conf)
as a starting point. For another proxy, use its equivalent host rule and TLS
configuration. DNS alone does not create the route. Restrict direct access to
port 8082 with the same host firewall policy used for the MkDocs backend on
port 8081; public traffic should use the HTTPS hostname.

After the workflow deploys, check:

```bash
curl -fsSI https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime/pkg/access
curl -fsSI https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime/services/runtime-api
```

On the docs host, read-only diagnostics are:

```bash
docker ps --filter name=mcp-runtime-pkgsite
docker logs --tail 80 mcp-runtime-pkgsite
curl -fsSI http://127.0.0.1:8082/github.com/mcp-runtime/mcp-runtime/pkg/access
```

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
