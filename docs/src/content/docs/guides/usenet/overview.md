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
        "priority": 2,
        "backup": true
      }
    ]
  }
}
```

Lower `priority` = higher preference.

`backbone` is optional. Set it when two providers share the same article spool so Decypharr can skip same-backbone providers after `423/430 article not found` responses.

Set `backup` for a fallback or block-account provider. By default, a backup is
used only when the primary tier fails or does not have an article; a merely busy
primary does not spend block-account traffic. To trade block usage for lower
playback startup latency, opt urgent reads into delayed spillover:

```json
{
  "usenet": {
    "stream_backup_wait": "250ms"
  }
}
```

Only urgent playback demand spills over after this wait. Read-ahead, downloads,
and maintenance continue waiting for the primary tier. Leave the value unset or
use `"0"` to disable spillover.

## Performance Tuning

### Connection Limits

```json
{
  "usenet": {
    "processing_max_connections": 15
  }
}
```

- Provider `max_connections`: The connection limit for one provider.
- `processing_max_connections`: The connection limit for one file during parsing and NZB downloads.

Streaming uses 75% of the connections of your primary providers. Decypharr keeps the remaining 25% for imports, repairs, and health checks. Backup providers count only when you have no primary provider.

All streams share these connections. When a stream has less data ready ahead of the player, Decypharr gives it the next free connection first. One stream alone can use all streaming connections.

**Example:**

- Provider A: `20`
- Provider B: `10`
- `processing_max_connections`: `15`

→ Streaming uses up to 22 connections. Each import uses up to 15 connections.

### Internet Link Protection

Decypharr does not fill your internet link. It sets a link limit on the connections that transfer data at the same time. The link limit applies to streaming read-ahead, imports, repairs, and health checks. It does not apply to a read that a player waits for now.

Decypharr changes the link limit automatically:

- When work waits on the link limit, Decypharr increases it.
- When more connections do not give more speed, your link is full. Decypharr goes back to the last limit.
- When replies start to arrive later than on an empty link, your link is queueing data. Decypharr decreases the link limit by a quarter.
- After a decrease, the link limit stays the same for 30 seconds. Then Decypharr tries a higher limit again. If a player waits for data, Decypharr tries a higher limit after 2 seconds.
- When no work waits, the link limit decreases to the number of connections in use.

You do not configure the link limit. To see the link limit, the usenet speed, and the reply delay, open the **Usenet** tab on the stats page. To see the total speed of all downloads and the speed cap, open the **Streams** tab.

To set a fixed speed cap, set `max_download_speed` in the general settings. The cap applies to all downloads, debrid and usenet. Refer to [Downloads](../configuration/#downloads).

### Read-Ahead Buffer

```json
{
  "usenet": {
    "read_ahead": "16MB",
    "body_pipeline_depth": 2
  }
}
```

Prefetch buffer for smoother playback. Higher = smoother but more memory.
Read-ahead bodies use an NNTP pipeline after enough work is queued to keep the
available prefetch workers occupied; shorter ranges retain full connection
parallelism. `body_pipeline_depth` accepts 1-4: 1 disables pipelining, 2 is the
balanced default, and 4 favors throughput on high-latency links. Direct playback
demand stays at one body per request so it can take priority at the next article
boundary.

### Connection Idle Timeout

```json
{
  "usenet": {
    "conn_idle_timeout": "5m"
  }
}
```

How long unused NNTP connections stay warm in the pool before being closed
(default: `5m`). Idle connections are kept healthy with periodic keepalive
pings and verified before reuse. Players read in bursts with quiet gaps in
between, so closing connections too early forces a TCP+TLS+AUTH reconnect
on every resume — visible as playback stutter. Lower this only if your
provider aggressively drops idle sessions.

### Processing Limits

```json
{
  "max_active_downloads": 5,
  "usenet": {"processing_timeout": "10m"}
}
```

- `max_active_downloads`: Shared active-download limit for torrents and NZBs
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

### Content Verification

The availability check only proves that the articles exist. After it passes,
decypharr also reads the head of each video file through the streaming stack
and checks for a valid media container signature. This catches NZBs whose
articles all exist but assemble into a broken stream (for example, RAR volumes
in the wrong order). If the check fails, the NZB is marked failed and the Arr
grabs a replacement release. The check reads one article per video file.

## Disk Buffer

```json
{
  "usenet": {
    "disk_path": "/cache/usenet/streams"
  }
}
```

The default empty `disk_path` keeps the bounded streaming window in memory. Set a path to enable disk-backed rewind buffering, and ensure that location has sufficient free space.

## Health Checks and Replacement

The health checker samples NZB article availability and can optionally validate
each media file's container signature. If a file is definitively broken and is
linked to an Arr item, `auto_repair` deletes it through Arr and triggers a
replacement search. Connection and provider errors remain inconclusive and do
not trigger replacement. See the [Health Checker & Repair guide](../repair/) for
configuration and API details.

## Arr Integration

Arrs send NZB files to Decypharr via the Sabnzbd API endpoint:

See [Sabnzbd Integration](./sabnzbd/) for details.

## Troubleshooting

### Connection Failures

- Verify host/port/SSL settings
- Test manually: `telnet news.provider.com 563`
- Check provider status

### Slow Streaming

1. Increase `max_connections` for each provider. This also increases the streaming connections.
2. Increase `read_ahead`.

### Processing Timeouts

- Increase `processing_timeout` for large files
- Reduce `availability_sample_percent` for faster checks
- Increase `max_active_downloads` if the system and providers have capacity

### Incomplete Downloads

- Check provider retention (old files may be incomplete)
- Try backup provider if available

## Example Configuration

Full Usenet config with optimal settings:

```json
{
  "max_active_downloads": 5,
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
    "processing_max_connections": 15,
    "read_ahead": "32MB",
    "body_pipeline_depth": 2,
    "processing_timeout": "15m",
    "availability_sample_percent": 5,
    "disk_path": "/cache/usenet"
  }
}
```
