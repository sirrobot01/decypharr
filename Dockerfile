# xx provides cross-compilation toolchains for CGO builds
FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx

# Stage 1: Build binaries — pinned to BUILDPLATFORM so Go runs natively (fast)
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
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
    go mod download -x

COPY . .

# Build main binary — xx-go sets CC/CXX/GOOS/GOARCH automatically
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 \
    xx-go build -trimpath \
    -ldflags="-w -s -X github.com/sirrobot01/decypharr/pkg/version.Version=${VERSION} -X github.com/sirrobot01/decypharr/pkg/version.Channel=${CHANNEL}" \
    -o /decypharr && \
    xx-verify /decypharr

# Build healthcheck (no CGO needed, plain cross-compile)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-w -s" \
    -o /healthcheck cmd/healthcheck/main.go

# Stage 2: Final image
FROM alpine:latest

ARG VERSION=0.0.0
ARG CHANNEL=dev

LABEL version="${VERSION}-${CHANNEL}"
LABEL org.opencontainers.image.source="https://github.com/sirrobot01/decypharr"
LABEL org.opencontainers.image.title="decypharr"
LABEL org.opencontainers.image.authors="sirrobot01"
LABEL org.opencontainers.image.documentation="https://github.com/sirrobot01/decypharr/blob/main/README.md"

# Install dependencies including rclone (from binary)
RUN apk add --no-cache fuse3 ca-certificates su-exec shadow curl unzip tzdata && \
    echo "user_allow_other" >> /etc/fuse.conf && \
    case "$(uname -m)" in \
        x86_64) ARCH=amd64 ;; \
        aarch64) ARCH=arm64 ;; \
        armv7l|armv7) ARCH=arm ;; \
        *) echo "Unsupported architecture: $(uname -m)" && exit 1 ;; \
    esac && \
    curl -O "https://downloads.rclone.org/rclone-current-linux-${ARCH}.zip" && \
    unzip "rclone-current-linux-${ARCH}.zip" && \
    cp rclone-*/rclone /usr/local/bin/ && \
    chmod +x /usr/local/bin/rclone && \
    rm -rf rclone-* && \
    apk del curl unzip

# Copy binaries and entrypoint
COPY --from=builder /decypharr /usr/bin/decypharr
COPY --from=builder /healthcheck /usr/bin/healthcheck
COPY scripts/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Set environment variables
ENV PUID=1000
ENV PGID=1000
ENV LOG_PATH=/app/logs

EXPOSE 8282
VOLUME ["/app"]

HEALTHCHECK --interval=10s --retries=10 CMD ["/usr/bin/healthcheck", "--config", "/app"]

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/decypharr", "--config", "/app"]
