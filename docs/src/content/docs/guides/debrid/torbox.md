---
title: Torbox Setup
description: Configure Torbox provider.
---

Torbox is a supported Debrid provider.

## Configuration

```json
{
  "debrids": [
    {
      "provider": "torbox",
      "name": "Torbox",
      "api_key": "YOUR_API_KEY"
    }
  ]
}
```

Get your API key from the Torbox dashboard.

All configuration options from [Real Debrid](./real-debrid/) apply (rate limits, workers, proxy, etc.).

## Defaults

| Option                      | Default      |
| --------------------------- | ------------ |
| `rate_limit`                | `240/minute` |
| `torrents_refresh_interval` | `30m`        |

TorBox allows 300 API requests each minute for each API key. Keep `rate_limit` below this value.

## Limits

Decypharr controls these TorBox limits for you:

- **Download links:** Decypharr requests at most 15 links each minute, with a burst of 20. TorBox does not document this limit. The value comes from third-party measurements. If the wait is too long, the read stops with an error. Try the read again.
- **Link lifetime:** Decypharr keeps a download link for 24 hours or less. A higher `auto_expire_links_after` value has no effect on TorBox.
- **Adds:** Decypharr sends each add one time. If TorBox refuses an add with a 429 error, Decypharr stops adds for the time that TorBox requests. Streaming continues during this time.
- **Cached-only adds:** If `download_uncached` is off, Decypharr checks the cache before each add. This prevents an add that TorBox cannot serve.
- **Active slots:** If all active slots are full, TorBox queues the torrent. Decypharr monitors the queued torrent until it starts.
- **Bandwidth:** Decypharr reads files in small ranges. The first range is 8 MiB. Each sequential range doubles, up to 64 MiB. This keeps the requested data near the data that you read.

## Status

A rate limit stops API requests for the time that TorBox requests. It does not delete a torrent. Decypharr removes a torrent from TorBox only if TorBox reports `failed`, `expired`, or `missing`, or if a cached-only torrent is not cached.

See [Configuration Reference](../configuration/#debrid-providers) for full options.
