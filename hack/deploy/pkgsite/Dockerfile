ARG GO_VERSION=1.26.9
FROM golang:${GO_VERSION}-alpine AS build

# Keep the documentation renderer on a reviewed release rather than following
# the upstream default at each deployment.
RUN GOBIN=/out go install golang.org/x/pkgsite/cmd/pkgsite@v0.5.0

FROM golang:${GO_VERSION}-alpine

LABEL org.opencontainers.image.source="https://github.com/mcp-runtime/mcp-runtime" \
      org.opencontainers.image.title="mcp-runtime-pkgsite" \
      org.opencontainers.image.description="Go package documentation for MCP Runtime"

WORKDIR /src
COPY --chown=65532:65532 . /src
COPY --from=build /out/pkgsite /usr/local/bin/pkgsite
COPY hack/deploy/pkgsite/start.sh /usr/local/bin/start-pkgsite
COPY hack/deploy/pkgsite/prepare-source.sh /usr/local/bin/prepare-pkgsite-source

RUN chmod 755 /usr/local/bin/start-pkgsite /usr/local/bin/prepare-pkgsite-source \
    && /usr/local/bin/prepare-pkgsite-source /src \
    && chown -R 65532:65532 /src

ENV GOMAXPROCS=2 \
    GOFLAGS=-p=1 \
    GOCACHE=/tmp/go-build \
    GOPATH=/tmp/gopath \
    GOPROXY=off \
    GOTOOLCHAIN=local \
    HOME=/tmp

USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=180s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/github.com/mcp-runtime/mcp-runtime/pkg/access || exit 1
ENTRYPOINT ["/usr/local/bin/start-pkgsite"]
