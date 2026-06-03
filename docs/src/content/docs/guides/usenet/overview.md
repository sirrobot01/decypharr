---
title: Usenet Configuration
description: Direct NNTP streaming configuration.
---

Decypharr supports direct NNTP streaming from Usenet providers - no additional download client required.

## How It Works

Decypharr connects directly to NNTP servers to:

1. Parse NZB files for segment information
2. Stream segments on-demand for playback
3. Download and assemble complete files

## Provider Configuration

### Add Provider

```json
{
  "usenet": {
    "providers": [
      {
        "host": "news.provider.com",
        "port": 563,
        "username": "your_username",
        "password": "your_password",
        "backbone": "Omicron",
        "ssl": true,
        "max_connections": 20,
        "priority": 1
      }
    ]
  }
}
```

### Multiple Providers

Decypharr can use multiple providers with priority and failover:

```json
{
  "usenet": {
    "providers": [
      {
        "host": "primary.news.com",
        "port": 563,
        "username": "user1",
        "password": "pass1",
        "backbone": "UsenetExpress",
        "ssl": true,
        "max_connections": 20,
        "priority": 1
      },
      {
        "host": "backup.news.com",
        "port": 563,
        "username": "user2",
        "password": "pass2",
        "backbone": "Omicron",
        "ssl": true,
        "max_connections": 10,
        "priority": 2
      }
    ]
  }
}
```

Lower `priority` = higher preference.

`backbone` is optional. Set it when two providers share the same article spool so Decypharr can skip same-backbone providers after `423/430 article not found` responses.

## Performance Tuning

### Connection Limits

```json
{
  "usenet": {
    "max_connections": 15
  }
}
```

- `max_connections`: Per-file/stream connection limit
- Provider `max_connections`: Per-provider limit

**Example:**

- Global: `15`
- Provider A: `20`
- Provider B: `10`

→ Up to 15 connections per file, split between providers based on priority

### Read-Ahead Buffer

```json
{
  "usenet": {
    "read_ahead": "16MB"
  }
}
```

Prefetch buffer for smoother playback. Higher = smoother but more memory.

### Processing Limits

```json
{
  "usenet": {
    "max_concurrent_nzb": 2,
    "processing_timeout": "10m"
  }
}
```

- `max_concurrent_nzb`: How many NZBs to process in parallel
- `processing_timeout`: Mark as bad if processing exceeds this

### Availability Checking

```json
{
  "usenet": {
    "availability_sample_percent": 10,
    "import_availability_sample_percent": 1
  }
}
```

Use `availability_sample_percent` for repair checks and
`import_availability_sample_percent` for the availability gate when adding an NZB.

- `100`: Check all segments (slow but accurate)
- `10`: Check 10% (fast but may miss issues)
- `1`: Quick import check (default)

## Disk Buffer

```json
{
  "usenet": {
    "disk_buffer_path": "/cache/usenet/streams"
  }
}
```

Streams use disk buffer for assembly. Ensure sufficient disk space.

## Repair

```json
{
  "usenet": {
    "skip_repair": false
  }
}
```

## Arr Integration

Arrs send NZB files to Decypharr via the Sabnzbd API endpoint:

See [Sabnzbd Integration](./sabnzbd/) for details.

## Troubleshooting

### Connection Failures

- Verify host/port/SSL settings
- Test manually: `telnet news.provider.com 563`
- Check provider status

### Slow Streaming

1. Increase `max_connections` per provider
2. Increase global `max_connections`
3. Increase `read_ahead` buffer

### Processing Timeouts

- Increase `processing_timeout` for large files
- Reduce `availability_sample_percent` for faster checks
- Increase `max_concurrent_nzb` if CPU allows

### Incomplete Downloads

- Enable `skip_repair: false` for PAR2 repair
- Check provider retention (old files may be incomplete)
- Try backup provider if available

## Example Configuration

Full Usenet config with optimal settings:

```json
{
  "usenet": {
    "providers": [
      {
        "host": "us.news.provider.com",
        "port": 563,
        "username": "user",
        "password": "pass",
        "ssl": true,
        "max_connections": 30,
        "priority": 1
      }
    ],
    "max_connections": 15,
    "read_ahead": "32MB",
    "processing_timeout": "15m",
    "availability_sample_percent": 5,
    "max_concurrent_nzb": 3,
    "disk_buffer_path": "/cache/usenet",
    "skip_repair": false
  }
}
```
