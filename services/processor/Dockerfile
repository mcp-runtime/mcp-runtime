FROM golang:1.26.6 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/

COPY services/processor/go.mod services/processor/go.sum ./services/processor/
RUN cd services/processor && go mod download

COPY services/processor/*.go ./services/processor/
ARG TARGETOS
ARG TARGETARCH
RUN cd services/processor && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/processor .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/processor /app/processor
EXPOSE 9092
USER 65532:65532
ENTRYPOINT ["/app/processor"]
