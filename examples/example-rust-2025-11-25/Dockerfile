FROM rust:1.88-alpine AS build

RUN apk add --no-cache build-base

WORKDIR /src
COPY Cargo.toml Cargo.lock /src/
COPY src /src/src
RUN cargo build --release

FROM alpine:3.20

RUN adduser -D -H -u 10001 app

WORKDIR /app
COPY --from=build /src/target/release/example-rust-2025-11-25 /app/example-rust-2025-11-25

USER 10001

EXPOSE 8088

ENTRYPOINT ["/app/example-rust-2025-11-25"]
