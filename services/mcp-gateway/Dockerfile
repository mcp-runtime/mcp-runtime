FROM golang:1.26.6 AS build
WORKDIR /src

# Copy root module first to satisfy local replace directives used by the gateway module.
COPY go.mod go.sum ./
COPY api/ ./api/
COPY pkg/ ./pkg/

# Copy service module and resolve dependencies from the service directory.
COPY services/mcp-gateway/go.mod services/mcp-gateway/go.sum ./services/mcp-gateway/
RUN cd services/mcp-gateway && go mod download

# Copy service source and build from within the service module. Copying the
# whole directory (rather than *.go) picks up subpackages, embedded assets,
# and any non-Go source files added later.
COPY services/mcp-gateway/ ./services/mcp-gateway/
ARG TARGETOS
ARG TARGETARCH
RUN cd services/mcp-gateway && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/mcp-gateway .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/mcp-gateway /app/mcp-gateway
EXPOSE 8091
USER 65532:65532
ENTRYPOINT ["/app/mcp-gateway"]
