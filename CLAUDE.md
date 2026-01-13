# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Decypharr is a Go application that implements a mock QBittorrent API with multiple debrid service support. It bridges *Arr applications (Sonarr, Radarr, Lidarr, Readarr) with debrid services (Real-Debrid, Torbox, Debrid Link, All Debrid).

## Build Commands

```bash
# Build main binary
go build -o decypharr

# Build with version info (as used in Docker)
go build -trimpath -ldflags="-w -s -X github.com/sirrobot01/decypharr/pkg/version.Version=X.X.X" -o decypharr

# Build healthcheck binary
go build -trimpath -ldflags="-w -s" -o healthcheck cmd/healthcheck/main.go

# Run locally
./decypharr --config /path/to/config/dir
```

## Architecture

### Entry Points
- `main.go` → `cmd/decypharr/main.go` - Application bootstrap with graceful shutdown handling
- `cmd/healthcheck/main.go` - Docker healthcheck binary

### Core Services (started in `cmd/decypharr/main.go:startServices`)
1. **HTTP Server** (`pkg/server/`) - Chi router serving all HTTP endpoints
2. **WebDAV Server** (`pkg/webdav/`) - Per-debrid WebDAV handlers for file access
3. **Rclone Manager** (`pkg/rclone/`) - Optional filesystem mounting via rclone RC API
4. **Repair Worker** (`pkg/repair/`) - Scheduled job to detect and fix broken symlinks

### Request Flow
```
*Arr App → QBit API (pkg/qbit/) → Debrid Processing (pkg/debrid/) → Download/Symlink
```

### Key Packages

**`pkg/wire/`** - Dependency injection container (singleton)
- `Store` holds all service instances: debrid storage, arr storage, torrent storage, rclone manager, repair
- Access via `wire.Get()`

**`pkg/debrid/`** - Debrid service abstraction
- `common.Client` interface defines all debrid operations
- Provider implementations in `providers/{realdebrid,torbox,debridlink,alldebrid}/`
- `store/` contains caching logic for WebDAV mode

**`pkg/qbit/`** - Mock QBittorrent API
- Implements `/api/v2/*` endpoints that *Arr apps expect
- Routes defined in `routes.go`, torrent operations in `torrent.go`

**`pkg/arr/`** - *Arr application client
- Communicates with Sonarr/Radarr/etc APIs for cleanup, refresh, and repair operations
- Type inference based on host/name patterns

**`pkg/webdav/`** - WebDAV server implementation
- One handler per debrid provider, mounted at `/webdav/{provider}/`
- Custom PROPFIND implementation in `propfind.go`

**`internal/config/`** - Configuration management
- Singleton pattern via `config.Get()`
- Config file at `{config_path}/config.json`
- Supports hot reload via `config.Reload()`

### HTTP Routes
- `/` - Web UI (`pkg/web/`)
- `/api/v2/*` - QBittorrent-compatible API (`pkg/qbit/`)
- `/webdav/*` - WebDAV endpoints per debrid provider
- `/debug/*` - Debug endpoints (logs, stats, ingests)
- `/webhooks/tautulli` - Tautulli webhook handler

### Configuration
Config is JSON-based, stored in the config directory. Key sections:
- `debrids[]` - Debrid service credentials and settings
- `qbittorrent` - Mock qBit settings (download folder, categories)
- `arrs[]` - *Arr application connections
- `repair` - Repair worker settings
- `rclone` - Rclone mount configuration
- `webdav` - WebDAV-specific settings

### Platform-Specific Code
- `cmd/decypharr/umask_unix.go` / `umask_win.go` - OS-specific umask handling
- `pkg/rclone/killed_unix.go` / `killed_windows.go` - Process termination detection

## Testing

```bash
# Run all tests
go test ./...

# Run specific package tests
go test ./internal/utils/...
```

Test files: `internal/utils/magnet_test.go`

## Docker

The application runs on port 8282 by default. Requires:
- `/dev/fuse` for rclone mounts
- `SYS_ADMIN` capability
- `apparmor:unconfined` security option

## URL Base and Reverse Proxy

The `url_base` config option allows running Decypharr under a subfolder (e.g., `/decypharr/`).

### Key Implementation Details

- Routes are mounted under `url_base` via Chi's `r.Route()` in `pkg/server/server.go`
- The `StripSlashes` middleware requires routes to be defined without trailing slash (handled automatically)
- All redirects use the `redirectTo()` helper in `pkg/web/middlewares.go` which prepends the URL base
- Cookies use `Path: "/"` which works for all subpaths

### Nginx Configuration

Example configs are in `examples/nginx/`:
- `subfolder.conf` - For running at `/decypharr/` (recommended)
- `subdomain.conf` - For running at a dedicated subdomain

Full documentation: `docs/docs/guides/reverse-proxy.md`

```nginx
# Subfolder setup - proxy_pass must include the full path
location /decypharr/ {
    include /etc/nginx/snippets/proxy.conf;
    proxy_pass http://localhost:8282/decypharr/;
}
```

When using external auth (Authelia, etc.), bypass `/api/` routes so *Arr apps can use built-in API token auth.
