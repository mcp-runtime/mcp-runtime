FROM node:22-bookworm-slim AS frontend
WORKDIR /src/services/ui/frontend

COPY services/ui/frontend/package*.json ./
RUN npm ci

COPY services/ui/frontend/ ./
RUN npm run build

FROM golang:1.26.9 AS build
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/ ./pkg/

COPY services/ui/go.mod services/ui/go.sum ./services/ui/
RUN cd services/ui && go mod download

COPY services/ui/*.go ./services/ui/
COPY --from=frontend /src/services/ui/static/ ./services/ui/static/
ARG TARGETOS
ARG TARGETARCH
RUN cd services/ui && \
    mkdir -p /out && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/ui .

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/ui /app/ui
EXPOSE 8082
USER 65532:65532
ENTRYPOINT ["/app/ui"]
