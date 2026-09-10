# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25.3

# Shared Go foundation for development and compilation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS go-base

WORKDIR /workspace/inventory-service

# Development stage: Go toolchain, source code, and hot reload.
FROM go-base AS development

ARG AIR_VERSION=v1.64.5

RUN GOBIN=/usr/local/bin go install github.com/air-verse/air@${AIR_VERSION} && \
    go clean -cache -modcache && \
    addgroup -g 1000 developer && \
    adduser -D -u 1000 -G developer developer && \
    chown -R developer:developer /go /workspace

# The api module is a separate module resolved through a replace directive, so
# it has to be present before `go mod download` can see it.
COPY inventory-service/go.mod inventory-service/go.sum ./
COPY inventory-service/api/ ./api/
RUN go mod download && \
    chown -R developer:developer /go

COPY --chown=developer:developer inventory-service/ ./

USER developer

EXPOSE 50051

CMD ["air", "-c", ".air.toml"]

# Build stage: compile the service with the full Go toolchain.
FROM go-base AS build

ARG TARGETOS
ARG TARGETARCH

# These files are copied into the minimal runtime image below.
RUN apk add --no-cache ca-certificates tzdata

# Keep dependency download in its own cached layer.
COPY inventory-service/go.mod inventory-service/go.sum ./
COPY inventory-service/api/ ./api/
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY inventory-service/ ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -mod=readonly \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/inventory \
      ./cmd/inventory

# Scratch has no writable temporary directory by default.
RUN mkdir -p /out/tmp && chmod 1777 /out/tmp

# Production stage: ship only what is required to execute the service.
FROM scratch AS production

ENV TZ=UTC

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build --chown=65532:65532 /out/tmp /tmp
COPY --from=build --chown=65532:65532 /out/inventory /inventory

USER 65532:65532

EXPOSE 50051

ENTRYPOINT ["/inventory"]
