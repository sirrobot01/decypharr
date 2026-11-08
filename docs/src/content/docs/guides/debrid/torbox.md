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
| `rate_limit`                | `300/minute` |
| `torrents_refresh_interval` | `30m`        |

TorBox allows 300 API requests each minute for each endpoint and API key. TorBox allows 60 uncached adds each hour.

## Limits

Decypharr controls these TorBox limits for you:

- **Download links:** Decypharr requests a download link one time and streams from the CDN URL. If the CDN refuses the URL, Decypharr requests a new link.
- **Adds:** Decypharr sends each add one time. If TorBox refuses an add with a 429 error, Decypharr stops adds for the time that TorBox requests. Streaming continues during this time.
- **Cached-only adds:** If `download_uncached` is off, Decypharr checks the cache before each add. This prevents an add that TorBox cannot serve.
- **Active slots:** If all active slots are full, TorBox queues the torrent. Decypharr monitors the queued torrent until it starts.
- **Bandwidth:** Decypharr reads files in small ranges. The first range is 8 MiB. Each sequential range doubles, up to 64 MiB. This keeps the requested data near the data that you read.

## Status

A rate limit stops API requests for the time that TorBox requests. It does not delete a torrent. Decypharr removes a torrent from TorBox only if TorBox reports `failed`, `expired`, or `missing`, or if a cached-only torrent is not cached.

See [Configuration Reference](../configuration/#debrid-providers) for full options.
