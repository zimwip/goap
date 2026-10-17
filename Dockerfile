# syntax=docker/dockerfile:1
# One image per service: docker build --build-arg SERVICE=graph .
# SERVICE is a binary of platform/cmd (crate goap-cmd-<SERVICE>, binary <SERVICE>).
FROM rust:1.97-slim AS build
# the C parts of the dependencies (bundled SQLite, aws-lc): a C compiler comes with the image, cmake for aws-lc
RUN apt-get update && apt-get install -y --no-install-recommends cmake && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY . .
# the image's toolchain builds: rust-toolchain.toml would have rustup fetch the stable channel again
RUN rm rust-toolchain.toml
ARG SERVICE
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/src/target \
    cargo build --release --locked -p goap-cmd-${SERVICE} \
    && install -D target/release/${SERVICE} /out/service

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --no-create-home nonroot
COPY --from=build /out/service /service
COPY methodologies /methodologies
COPY domains /domains
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/service"]
