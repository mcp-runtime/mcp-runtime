FROM golang:1.26.9 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/

COPY services/ingest/go.mod services/ingest/go.sum ./services/ingest/
RUN cd services/ingest && go mod download

COPY services/ingest/*.go ./services/ingest/
ARG TARGETOS
ARG TARGETARCH
RUN cd services/ingest && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/ingest .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/ingest /app/ingest
EXPOSE 8081 9091
USER 65532:65532
ENTRYPOINT ["/app/ingest"]
