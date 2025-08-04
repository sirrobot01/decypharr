# Stage 1: Build binaries
FROM --platform=$BUILDPLATFORM golang:1.24-alpine as builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0
ARG CHANNEL=dev

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download -x

COPY . .

# Build main binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags="-w -s -X github.com/sirrobot01/decypharr/pkg/version.Version=${VERSION} -X github.com/sirrobot01/decypharr/pkg/version.Channel=${CHANNEL}" \
    -o /decypharr

# Build healthcheck (optimized)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-w -s" \
    -o /healthcheck cmd/healthcheck/main.go

# Stage 2: Final image
FROM alpine:latest

ARG VERSION=0.0.0
ARG CHANNEL=dev

LABEL version = "${VERSION}-${CHANNEL}"
LABEL org.opencontainers.image.source = "https://github.com/sirrobot01/decypharr"
LABEL org.opencontainers.image.title = "decypharr"
LABEL org.opencontainers.image.authors = "sirrobot01"
LABEL org.opencontainers.image.documentation = "https://github.com/sirrobot01/decypharr/blob/main/README.md"

# Install dependencies
RUN apk add --no-cache fuse3 ca-certificates su-exec shadow && \
    echo "user_allow_other" >> /etc/fuse.conf

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

HEALTHCHECK --interval=3s --retries=10 CMD ["/usr/bin/healthcheck", "--config", "/app", "--basic"]

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/decypharr", "--config", "/app"]