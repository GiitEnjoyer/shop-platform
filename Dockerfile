# syntax=docker/dockerfile:1.7
# One Dockerfile for every service: docker build --build-arg SERVICE=orders .
# Result: ~15 MB distroless image, non-root, no shell, read-only rootfs friendly.

ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Dependencies layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG SERVICE
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -n "$SERVICE" && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
ARG VERSION=dev
LABEL org.opencontainers.image.title="${SERVICE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/GiitEnjoyer/shop-platform"
COPY --from=build /out/app /app
COPY deploy/data/catalog.json /data/catalog.json
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
