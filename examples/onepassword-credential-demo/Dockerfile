FROM --platform=$BUILDPLATFORM golang:1.26.10 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -o /out/demo .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/demo /demo
COPY .mcp/upstream-auth.json /upstream-auth.json
EXPOSE 8088
USER 65532:65532
ENTRYPOINT ["/demo"]
