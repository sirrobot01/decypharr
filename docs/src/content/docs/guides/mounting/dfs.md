---
title: DFS Mounting
description: Decypharr File System details.
---

DFS (Decypharr File System) is a custom VFS implementation optimized for streaming from Debrid and Usenet.

## Windows Requirement

If you run Decypharr on Windows and use `mount.type: "dfs"`, you must install **WinFsp** first.  
Without WinFsp, DFS mounts will not start.

## Features

- **Sequential Read Optimization**: Adaptive chunk sizing for streaming
- **Disk Caching**: Local cache with size limits and expiry
- **Low Latency**: Direct API integration, no RC overhead
- **Auto-Unmount**: Daemon timeout for idle periods

## Configuration

In `config.json`:

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
      "daemon_timeout": "30m",
      "uid": 1000,
      "gid": 1000,
      "allow_other": true
    }
  }
}
```

### Key Settings

| Setting           | Purpose                                                               |
|-------------------|-----------------------------------------------------------------------|
| `chunk_size`      | Initial read chunk size (starts at 10MB, doubles on sequential reads) |
| `disk_cache_size` | Max local cache before cleanup                                        |
| `cache_expiry`    | Remove unused chunks after duration                                   |
| `daemon_timeout`  | Unmount after idle time (empty = stay mounted)                        |

## Performance Tuning

**For Streaming:**

- `chunk_size`: `10MB` - `20MB`
- `disk_cache_size`: `50GB` - `100GB`

**For Downloads:**

- Enable larger `disk_cache_size` to avoid re-fetching

## Permissions

Set `uid` and `gid` to match your media server user:

```bash
id plex
# uid=1001(plex) gid=1001(plex)
```

```json
{
  "dfs": {
    "uid": 1001,
    "gid": 1001,
    "umask": "022",
    "allow_other": true
  }
}
```

## vs Rclone

| Feature        | DFS                          | Rclone                    |
|----------------|------------------------------|---------------------------|
| Streaming      | Optimized sequential reads   | General-purpose VFS       |
| Setup          | Zero configuration           | Multiple VFS modes        |
| Cache          | Chunk-based with auto-expire | Full VFS cache system     |
| Resource Usage | Lower overhead               | Higher (separate process) |
