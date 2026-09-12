# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25.3

# Shared Go foundation for development and compilation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS go-base

WORKDIR /workspace/order-service

# Development stage: Go toolchain, source code, and hot reload.
FROM go-base AS development

ARG AIR_VERSION=v1.64.5

RUN GOBIN=/usr/local/bin go install github.com/air-verse/air@${AIR_VERSION} && \
    go clean -cache -modcache && \
    addgroup -g 1000 developer && \
    adduser -D -u 1000 -G developer developer && \
    chown -R developer:developer /go /workspace

# inventory-service/api is a separate module reached through a replace
# directive, so it has to be present before `go mod download` can resolve it.
COPY order-service/go.mod order-service/go.sum ./
COPY inventory-service/api/ /workspace/inventory-service/api/
RUN go mod download && \
    chown -R developer:developer /go

COPY --chown=developer:developer order-service/ ./

USER developer

ENV GIN_MODE=debug

EXPOSE 8080

CMD ["air", "-c", ".air.toml"]

# Build stage: compile the service with the full Go toolchain.
FROM go-base AS build

ARG TARGETOS
ARG TARGETARCH

# These files are copied into the minimal runtime image below.
RUN apk add --no-cache ca-certificates tzdata

# Keep dependency download in its own cached layer.
COPY order-service/go.mod order-service/go.sum ./
COPY inventory-service/api/ /workspace/inventory-service/api/
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY order-service/ ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -mod=readonly \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/order \
      ./cmd/order

# Scratch has no writable temporary directory by default.
RUN mkdir -p /out/tmp && chmod 1777 /out/tmp

# Production stage: ship only what is required to execute the service.
FROM scratch AS production

ENV GIN_MODE=release \
    TZ=UTC

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build --chown=65532:65532 /out/tmp /tmp
COPY --from=build --chown=65532:65532 /out/order /order

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/order"]
