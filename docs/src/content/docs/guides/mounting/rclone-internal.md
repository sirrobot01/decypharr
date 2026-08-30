---
title: Rclone (Internal)
description: Use the embedded Rclone instance to mount.
---

Decypharr includes an embedded Rclone instance with full VFS support. Decypharr starts and stops this instance for you.

To use an Rclone process that you start yourself, see [Rclone (External)](../rclone-external/).

## Configuration

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
      "transfers": 4
    }
  }
}
```

## VFS Cache Modes

| Mode      | Description           | Use Case                      |
|-----------|-----------------------|-------------------------------|
| `off`     | No caching            | Low disk space                |
| `minimal` | Small metadata cache  | Light usage                   |
| `writes`  | Cache writes only     | Streaming + occasional writes |
| `full`    | Full read/write cache | Best performance              |

**Recommended**: `writes` for most use cases

## Performance Settings

### Streaming Optimization

```json
{
  "rclone": {
    "vfs_cache_mode": "writes",
    "vfs_read_chunk_size": "128MB",
    "vfs_read_ahead": "256MB",
    "buffer_size": "32MB",
    "transfers": 8
  }
}
```

### Bandwidth Limiting

```json
{
  "rclone": {
    "bw_limit": "10M"
  }
}
```

Limits to 10 MB/s.

## Troubleshooting

### Mount Permission Denied

Set `uid`/`gid` to match media server user:

```json
{
  "rclone": {
    "uid": 1001,
    "gid": 1001
  }
}
```

### High Memory Usage

Reduce cache limits:

```json
{
  "rclone": {
    "vfs_cache_max_size": "5GB",
    "transfers": 2
  }
}
```
