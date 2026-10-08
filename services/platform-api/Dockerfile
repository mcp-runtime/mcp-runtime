FROM golang:1.26.6 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/
COPY api/ ./api/

COPY services/platform-api/go.mod services/platform-api/go.sum ./services/platform-api/
RUN cd services/platform-api && go mod download

COPY services/platform-api/ ./services/platform-api/

ARG TARGETOS
ARG TARGETARCH
RUN cd services/platform-api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/platform-api .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/platform-api /app/platform-api
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/app/platform-api"]
