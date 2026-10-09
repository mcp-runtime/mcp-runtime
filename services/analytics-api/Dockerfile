FROM golang:1.26.9 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/
COPY api/ ./api/

COPY services/analytics-api/go.mod services/analytics-api/go.sum ./services/analytics-api/
RUN cd services/analytics-api && go mod download

COPY services/analytics-api/ ./services/analytics-api/

ARG TARGETOS
ARG TARGETARCH
RUN cd services/analytics-api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/analytics-api .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/analytics-api /app/analytics-api
EXPOSE 8085 9095
USER 65532:65532
ENTRYPOINT ["/app/analytics-api"]
