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

# one service (the default target): docker build --build-arg SERVICE=graph .
FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
COPY --from=build /out/${SERVICE} /service
COPY methodologies /methodologies
COPY domains /domains
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/service"]
