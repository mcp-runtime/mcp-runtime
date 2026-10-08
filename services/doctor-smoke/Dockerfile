FROM golang:1.26.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY pkg/ ./pkg/
COPY cmd/doctor-smoke/ ./cmd/doctor-smoke/
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/doctor-smoke ./cmd/doctor-smoke

FROM gcr.io/distroless/base-debian12
COPY --from=build --chown=65532:65532 /out/doctor-smoke /doctor-smoke
EXPOSE 8088
USER 65532:65532
ENTRYPOINT ["/doctor-smoke"]
