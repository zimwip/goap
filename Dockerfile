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
ARG SERVICE
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./platform/cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/service /service
COPY methodologies /methodologies
COPY domains /domains
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/service"]
