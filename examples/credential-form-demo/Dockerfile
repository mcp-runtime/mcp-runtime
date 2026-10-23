FROM --platform=$BUILDPLATFORM golang:1.26.10 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -o /out/upstream .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/upstream /upstream
EXPOSE 8088
USER 65532:65532
ENTRYPOINT ["/upstream"]
