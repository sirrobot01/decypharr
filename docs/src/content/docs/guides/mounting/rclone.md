---
title: Rclone Mounting
description: Using embedded Rclone for mounting.
---

Decypharr includes an embedded Rclone instance with full VFS support.

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

## External Rclone

Connect to an existing Rclone instance:

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

Start Rclone with RC:

```bash
rclone rcd --rc-addr=:5572 --rc-user=user --rc-pass=pass
```

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
