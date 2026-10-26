# syntax=docker/dockerfile:1
# One image per service: docker build --build-arg SERVICE=graph .
FROM golang:1.26-alpine AS build
WORKDIR /src
# the modules of the go.work (ADR 0125): their go.mod / go.sum first, so the download layer is cached
COPY go.work go.work.sum ./
COPY contracts/go.mod contracts/go.sum contracts/
COPY lib/go.mod lib/go.sum lib/
COPY graph/go.mod graph/go.sum graph/
COPY engine/go.mod engine/go.sum engine/
COPY mcp/go.mod mcp/go.sum mcp/
COPY gateway/go.mod gateway/go.sum gateway/
COPY platform/go.mod platform/go.sum platform/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# every binary at once, SERVICE not read yet: this stage is the same for every service, so the builds
# of a `docker compose up --build` share it (BuildKit runs it once) instead of each compiling the tree
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./platform/cmd/...

# every service in one image, /goap/<service> (docker build --target services -t goap/goap:dev .): the compose
# stack runs it with one entrypoint per service, so it is built once, by docker and podman alike
FROM gcr.io/distroless/static-debian12:nonroot AS services
COPY --from=build /out/ /goap/
COPY methodologies /methodologies
COPY domains /domains
# the built-in domains, which the domain registry seeds protected (ADR 0167)
COPY lib/domains/builtin /domains/builtin
EXPOSE 8080
USER nonroot

# the git importer (ADR 0148) runs git itself: alpine with git rather than distroless (built without cgo, it reads no
# symbols): docker build --target importer-git -t goap/importer-git:dev .
FROM alpine:3.22 AS importer-git
RUN apk add --no-cache git openssh-client ca-certificates && adduser -D -u 65532 nonroot
COPY --from=build /out/importer-git /goap/importer-git
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/goap/importer-git"]

# the Rust binaries (ADR 0163), each in an image of its own: the graph service and the domain registry, docker build
# --target rust -t goap/rust:dev . (/goap/goap-graph-rs, /goap/goap-domains-rs, ADR 0167), and the comparison proxy, a
# separate binary the service knows nothing of,
# docker build --target proxy -t goap/proxy:dev . (/goap/goap-proxy); SQLite is compiled in: rusqlite bundled, the one C
# of the build
FROM rust:1.97-slim-bookworm AS rust-build
WORKDIR /src
COPY proto proto
COPY graph/graph/migrations graph/graph/migrations
COPY graph/graph/migrations_sqlite graph/graph/migrations_sqlite
COPY graph/domainsvc/migrations graph/domainsvc/migrations
COPY graph/domainsvc/migrations_sqlite graph/domainsvc/migrations_sqlite
COPY graph/smarttagsvc/migrations graph/smarttagsvc/migrations
COPY graph/smarttagsvc/migrations_sqlite graph/smarttagsvc/migrations_sqlite
COPY platform/devseed/demo.json platform/devseed/demo.json
# read by graph-catalog with include_str!: the widget catalogue of the forms (the built-in domains are the registry's,
# read from /domains/builtin at its start, ADR 0167)
COPY web/src/lib/forms/widgetCatalog.gen.ts web/src/lib/forms/widgetCatalog.gen.ts
COPY rust rust
RUN --mount=type=cache,target=/usr/local/cargo/registry --mount=type=cache,target=/src/rust/target \
    cd rust && cargo build --release --locked -p goap-proxy -p graph-service \
    && mkdir -p /out/graph /out/proxy && cp target/release/goap-graph-rs target/release/goap-domains-rs /out/graph/ \
    && cp target/release/goap-proxy /out/proxy/

FROM gcr.io/distroless/cc-debian12:nonroot AS rust
COPY --from=rust-build /out/graph/ /goap/
COPY domains /domains
# the built-in domains, which the domain registry seeds protected (ADR 0167)
COPY lib/domains/builtin /domains/builtin
EXPOSE 8080
USER nonroot

FROM gcr.io/distroless/cc-debian12:nonroot AS proxy
COPY --from=rust-build /out/proxy/ /goap/
EXPOSE 8080
USER nonroot

# one service (the default target): docker build --build-arg SERVICE=graph .
FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
COPY --from=build /out/${SERVICE} /service
COPY methodologies /methodologies
COPY domains /domains
# the built-in domains, which the domain registry seeds protected (ADR 0167)
COPY lib/domains/builtin /domains/builtin
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/service"]
