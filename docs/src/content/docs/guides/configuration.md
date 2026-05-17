---
title: Configuration Reference
description: Complete config.json reference.
---

Configuration is stored in `config.json`. Most settings can be managed via the Web UI under Settings.

## Server

```json
{
  "bind_address": "0.0.0.0",
  "port": "8282",
  "url_base": "",
  "app_url": "http://localhost:8282",
  "log_level": "info"
}
```

| Field          | Type   | Description                                      | Default       |
|----------------|--------|--------------------------------------------------|---------------|
| `bind_address` | string | IP to bind to                                    | `0.0.0.0`     |
| `port`         | string | Port to listen on                                | `8282`        |
| `url_base`     | string | Base path for reverse proxy                      | `""`          |
| `app_url`      | string | External URL for callbacks                       | Auto-detected |
| `log_level`    | string | Logging level (`debug`, `info`, `warn`, `error`) | `info`        |

## Authentication

```json
{
  "use_auth": true,
  "username": "admin",
  "password": "$2a$10$...",
  "api_token": "..."
}
```

Password is bcrypt-hashed. API token is auto-generated.

## Debrid Providers

Array of Debrid services:

```json
{
  "debrids": [
    {
      "provider": "realdebrid",
      "name": "RD Primary",
      "api_key": "YOUR_API_KEY",
      "download_uncached": false,
      "rate_limit": "200/minute",
      "workers": 50,
      "minimum_free_slot": 0,
      "priority": 0,
      "limit": 100,
      "torrents_refresh_interval": "5m",
      "download_links_refresh_interval": "10m",
      "auto_expire_links_after": "24h",
      "proxy": "",
      "unpack_rar": true
    }
  ]
}
```

### Provider Fields

| Field                             | Type   | Description                                                      | Default                         |
|-----------------------------------|--------|------------------------------------------------------------------|---------------------------------|
| `provider`                        | string | Provider type: `realdebrid`, `alldebrid`, `debridlink`, `torbox` | **Required**                    |
| `name`                            | string | Display name                                                     | Provider type                   |
| `api_key`                         | string | API key from provider dashboard                                  | **Required**                    |
| `download_api_keys`               | array  | Additional keys for download rotation                            | `[api_key]`                     |
| `download_uncached`               | bool   | Download torrents not in provider cache                          | `false`                         |
| `rate_limit`                      | string | API rate limit (`200/minute`, `10/second`)                       | `200/minute`                    |
| `repair_rate_limit`               | string | Separate limit for repair operations                             | Same as `rate_limit`            |
| `download_rate_limit`             | string | Separate limit for downloads                                     | Same as `rate_limit`            |
| `proxy`                           | string | HTTP(S) proxy URL                                                | `""`                            |
| `unpack_rar`                      | bool   | Auto-extract RAR archives                                        | `true`                          |
| `minimum_free_slot`               | int    | Minimum free torrent slots to use this provider                  | `0`                             |
| `priority`                        | int    | Provider priority (lower values are tried first)                 | `0`                             |
| `limit`                           | int    | Max torrents allowed on this provider                            | `0` (unlimited)                 |
| `workers`                         | int    | Concurrent API workers                                           | Auto (CPU * 50 / num_providers) |
| `torrents_refresh_interval`       | string | How often to refresh torrent list                                | `5m`                            |
| `download_links_refresh_interval` | string | How often to refresh download links                              | `10m`                           |
| `auto_expire_links_after`         | string | Auto-remove links after duration                                 | `24h`                           |
| `user_agent`                      | string | Custom User-Agent header                                         | Default                         |

## Usenet

```json
{
  "usenet": {
    "providers": [
      {
        "host": "news.provider.com",
        "port": 563,
        "username": "user",
        "password": "pass",
        "backbone": "Omicron",
        "ssl": true,
        "max_connections": 20,
        "priority": 1
      }
    ],
    "max_connections": 15,
    "read_ahead": "16MB",
    "processing_timeout": "10m",
    "availability_sample_percent": 10,
    "max_concurrent_nzb": 2,
    "disk_buffer_path": "/cache/usenet/streams",
    "skip_repair": false
  }
}
```

### Usenet Fields

| Field                         | Type   | Description                     | Default                      |
|-------------------------------|--------|---------------------------------|------------------------------|
| `providers`                   | array  | NNTP server configurations      | `[]`                         |
| `max_connections`             | int    | Max connections per file/stream | `15`                         |
| `read_ahead`                  | string | Prefetch buffer size            | `16MB`                       |
| `processing_timeout`          | string | Max time for NZB processing     | `10m`                        |
| `availability_sample_percent` | int    | % of segments to check (1-100)  | `10`                         |
| `max_concurrent_nzb`          | int    | Parallel NZB processing limit   | `2`                          |
| `disk_buffer_path`            | string | Disk buffer location            | `{main_path}/usenet/streams` |
| `skip_repair`                 | bool   | Disable NZB repair operations   | `false`                      |

### Provider Fields

| Field             | Type   | Description                        | Default             |
|-------------------|--------|------------------------------------|---------------------|
| `host`            | string | NNTP server hostname               | **Required**        |
| `port`            | int    | NNTP port                          | `119` (563 for SSL) |
| `username`        | string | NNTP username                      | **Required**        |
| `password`        | string | NNTP password                      | **Required**        |
| `backbone`        | string | Optional shared article backbone for article-not-found failover | `""` |
| `ssl`             | bool   | Use SSL/TLS                        | `false`             |
| `max_connections` | int    | Max connections to this server     | `20`                |
| `priority`        | int    | Provider priority (lower = higher) | Index + 1           |

## Mounting

Mount configuration determines how files are exposed on the filesystem.

### Mount Type Selection

```json
{
  "mount": {
    "type": "dfs",
    "mount_path": "/mnt/decypharr"
  }
}
```

| Type              | Description                            |
|-------------------|----------------------------------------|
| `dfs`             | Custom VFS optimized for streaming     |
| `rclone`          | Embedded Rclone with full VFS features |
| `external_rclone` | Connect to existing Rclone RC instance |
| `none`            | No filesystem mounting                 |

### DFS Configuration

```json
{
  "mount": {
    "type": "dfs",
    "mount_path": "/mnt/decypharr",
    "dfs": {
      "cache_dir": "/cache/dfs",
      "chunk_size": "10MB",
      "disk_cache_size": "50GB",
      "cache_expiry": "24h",
      "cache_cleanup_interval": "1h",
      "daemon_timeout": "30m",
      "uid": 1000,
      "gid": 1000,
      "umask": "022",
    }
  }
}
```

| Field                    | Description                  | Default         |
|--------------------------|------------------------------|-----------------|
| `cache_dir`              | Local cache storage          | Required        |
| `chunk_size`             | Initial chunk size for reads | `10MB`          |
| `disk_cache_size`        | Max disk cache size          | `0` (unlimited) |
| `cache_expiry`           | Chunk expiry time            | `1h`            |
| `cache_cleanup_interval` | Cache cleanup frequency      | `10m`           |
| `daemon_timeout`         | Idle timeout before unmount  | `""` (never)    |
| `uid`                    | File owner UID               | Current user    |
| `gid`                    | File owner GID               | Current group   |
| `umask`                  | Permission mask              | `022`           |
| `allow_other`            | Allow other users to access  | `false`         |
| `default_permissions`    | Enable permission checks     | `false`         |

### Rclone Configuration

```json
{
  "mount": {
    "type": "rclone",
    "mount_path": "/mnt/decypharr",
    "rclone": {
      "cache_dir": "/cache/rclone",
      "vfs_cache_mode": "writes",
      "vfs_cache_max_size": "10GB",
      "vfs_read_chunk_size": "128MB",
      "vfs_read_ahead": "256MB",
      "buffer_size": "16MB",
      "transfers": 4,
      "uid": 1000,
      "gid": 1000
    }
  }
}
```

| Field                 | Description                        | Default         |
|-----------------------|------------------------------------|-----------------|
| `cache_dir`           | VFS cache directory                | Required        |
| `vfs_cache_mode`      | `off`, `minimal`, `writes`, `full` | `writes`        |
| `vfs_cache_max_size`  | Max VFS cache size                 | `0` (unlimited) |
| `vfs_read_chunk_size` | Read chunk size                    | `128MB`         |
| `vfs_read_ahead`      | Read-ahead buffer                  | `0`             |
| `buffer_size`         | I/O buffer size                    | `16MB`          |
| `bw_limit`            | Bandwidth limit                    | `0` (unlimited) |
| `transfers`           | Concurrent transfers               | `4`             |
| `uid` / `gid`         | File ownership                     | Current user    |

### External Rclone

```json
{
  "mount": {
    "type": "external_rclone",
    "external_rclone": {
      "rc_url": "http://localhost:5572",
      "rc_username": "user",
      "rc_password": "pass"
    }
  }
}
```

Connect to an existing Rclone instance's RC API.

## Health Checker

```json
{
  "repair": {
    "enabled": true,
    "source": "arr",
    "schedule": "0 4 * * *",
    "workers": 5,
    "strategy": "per_entry",
    "recheck_interval": "168h",
    "auto_repair": true,
    "nntp_connection_percent": 20
  }
}
```

| Field                     | Description                                                                | Default     |
|---------------------------|----------------------------------------------------------------------------|-------------|
| `enabled`                 | Master switch for the recurring sweep                                      | `false`     |
| `source`                  | `arr` (walk Arr media) or `managed` (walk managed entries)                 | `arr`       |
| `schedule`                | Cron expression. Required when enabled                                     | —           |
| `workers`                 | Concurrent probe workers                                                   | `5`         |
| `strategy`                | `per_entry` (stop at first broken file) or `per_file` (probe every file)   | `per_entry` |
| `recheck_interval`        | How long a healthy entry stays fresh before becoming a candidate again     | `168h`      |
| `arrs`                    | Optional Arr filter when `source=arr`. Empty = all eligible                | `[]`        |
| `auto_repair`             | When `true`, brokens are repaired in-sweep. When `false`, detect-only      | `false`     |
| `notify_on_complete`      | Send a notification when a sweep finishes                                  | `false`     |
| `nntp_connection_percent` | Share of NNTP connections probes may use, to avoid starving downloads      | `20`        |

See the [Health Checker & Repair guide](/guides/repair/) for the full model, API, and Browse-page integration.

## Arr Configuration

```json
{
  "arrs": [
    {
      "name": "Sonarr",
      "host": "http://sonarr:8989",
      "token": "API_TOKEN",
      "cleanup": true,
      "skip_repair": false,
      "download_uncached": false,
      "selected_debrid": ""
    }
  ]
}
```

| Field               | Description                      | Default     |
|---------------------|----------------------------------|-------------|
| `name`              | Display name                     | Required    |
| `host`              | Arr URL                          | Required    |
| `token`             | Arr API key                      | Required    |
| `cleanup`           | Auto-remove completed downloads  | `true`      |
| `skip_repair`       | Skip repair for this Arr         | `false`     |
| `download_uncached` | Download uncached torrents       | `false`     |
| `selected_debrid`   | Force specific Debrid provider   | `""` (auto) |
| `source`            | Config source (`auto`, `config`) | `config`    |

## Environment Variables

All config options support environment variable overrides using double underscore notation:

```bash
# Server
PORT=8282
LOG_LEVEL=debug

# Debrid
DEBRIDS__0__PROVIDER=realdebrid
DEBRIDS__0__API_KEY=your_key

# Usenet
USENET__MAX_CONNECTIONS=20
USENET__PROVIDERS__0__HOST=news.provider.com
USENET__PROVIDERS__0__PORT=563
USENET__PROVIDERS__0__BACKBONE=Omicron

# Mount - DFS
MOUNT__DFS__CACHE_DIR=/cache
MOUNT__DFS__CHUNK_SIZE=10MB

# Repair
REPAIR__ENABLED=true
REPAIR__INTERVAL=30m
```

See [defaults.go](https://github.com/sirrobot01/decypharr/blob/main/internal/config/defaults.go) for all defaults.
