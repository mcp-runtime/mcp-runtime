FROM golang:1.26.6 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/
COPY api/ ./api/

COPY services/runtime-api/go.mod services/runtime-api/go.sum ./services/runtime-api/
RUN cd services/runtime-api && go mod download

COPY services/runtime-api/ ./services/runtime-api/

ARG TARGETOS
ARG TARGETARCH
RUN cd services/runtime-api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/runtime-api .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/runtime-api /app/runtime-api
EXPOSE 8084 9094
USER 65532:65532
ENTRYPOINT ["/app/runtime-api"]
