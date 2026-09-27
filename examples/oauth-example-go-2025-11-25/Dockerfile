FROM --platform=$BUILDPLATFORM golang:1.26.4 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -o /out/oauth-example-go-2025-11-25 ./main.go

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/oauth-example-go-2025-11-25 /app/oauth-example-go-2025-11-25
EXPOSE 8088
USER 65532:65532
ENTRYPOINT ["/app/oauth-example-go-2025-11-25"]
