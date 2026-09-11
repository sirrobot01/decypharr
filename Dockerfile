# xx provides cross-compilation toolchains for CGO builds
FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx

# Stage 1: Build binaries — pinned to BUILDPLATFORM so Go runs natively (fast)
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0
ARG CHANNEL=dev

# Copy xx scripts for cross-compilation
COPY --from=xx / /

WORKDIR /app

# Install cross-compilation toolchain via xx
RUN apk add --no-cache clang lld && \
    xx-apk add --no-cache gcc g++ musl-dev libc-dev fuse-dev

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Build main binary — xx-go sets CC/CXX/GOOS/GOARCH automatically.
# disable_libutp keeps anacrolix/torrent (hearsay transport) on its
# pure-Go uTP stack; the cgo libutp variant links libstdc++, which the
# final image does not ship.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 \
    xx-go build -trimpath -tags disable_libutp \
    -ldflags="-w -s -X github.com/dylanmazurek/decypharr/pkg/version.Version=${VERSION} -X github.com/dylanmazurek/decypharr/pkg/version.Channel=${CHANNEL}" \
    -o /decypharr && \
    xx-verify /decypharr

# Build healthcheck (no CGO needed, plain cross-compile)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-w -s" \
    -o /healthcheck cmd/healthcheck/main.go

# Stage 2: Final image
FROM alpine:3.24.1

ARG VERSION=0.0.0
ARG CHANNEL=dev
ARG RCLONE_VERSION=1.69.3

LABEL version="${VERSION}-${CHANNEL}"
LABEL org.opencontainers.image.source="https://github.com/dylanmazurek/decypharr"
LABEL org.opencontainers.image.title="decypharr"
LABEL org.opencontainers.image.authors="dylanmazurek"
LABEL org.opencontainers.image.documentation="https://github.com/dylanmazurek/decypharr/blob/main/README.md"

# Install dependencies including rclone (from binary).
# libstdc++/libgcc: required at runtime by rapidyenc's C++ decoder.
RUN apk add --no-cache fuse3 ca-certificates su-exec shadow curl unzip tzdata libstdc++ libgcc && \
    echo "user_allow_other" >> /etc/fuse.conf && \
    case "$(uname -m)" in \
        x86_64) ARCH=amd64 ;; \
        aarch64) ARCH=arm64 ;; \
        armv7l|armv7) ARCH=arm ;; \
        *) echo "Unsupported architecture: $(uname -m)" && exit 1 ;; \
    esac && \
    curl -fsSLO "https://downloads.rclone.org/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-${ARCH}.zip" && \
    curl -fsSLO "https://downloads.rclone.org/v${RCLONE_VERSION}/SHA256SUMS" && \
    grep "rclone-v${RCLONE_VERSION}-linux-${ARCH}.zip" SHA256SUMS | sha256sum -c - && \
    unzip -q "rclone-v${RCLONE_VERSION}-linux-${ARCH}.zip" && \
    cp rclone-*/rclone /usr/local/bin/ && \
    chmod +x /usr/local/bin/rclone && \
    rm -rf rclone-* SHA256SUMS && \
    apk del curl unzip

# Copy binaries, frontend, and entrypoint
COPY --from=builder /decypharr /usr/bin/decypharr
COPY --from=builder /healthcheck /usr/bin/healthcheck
COPY --from=builder /app/frontend /app/frontend
COPY --chmod=755 scripts/entrypoint.sh /entrypoint.sh

# Set environment variables
ENV PUID=1000
ENV PGID=1000
ENV LOG_PATH=/app/logs
# This anacrolix diagnostic fires whenever an internal unlock handler takes
# 20ms; actual torrent errors remain visible and operators can override it.
ENV GO_LOG=client-unlock-handlers.go=err

EXPOSE 8282
VOLUME ["/app"]

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=10 \
    CMD ["/usr/bin/healthcheck", "--config", "/app"]

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/decypharr", "--config", "/app"]
