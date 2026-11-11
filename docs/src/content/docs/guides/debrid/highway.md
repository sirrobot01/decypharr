---
title: HighWay Setup
description: Configure HighWay provider.
---

HighWay is a supported Debrid provider.

## Configuration

```json
{
  "debrids": [
    {
      "provider": "highway",
      "name": "HighWay",
      "api_key": "YOUR_API_TOKEN"
    }
  ]
}
```

Create an API token in the HighWay Download Center under **Sessions** (`https://cloud.high-way.me/#sessions`).
The token needs at least `torrent:read` and `torrent:write`; add `torrent:delete_self` (or `torrent:delete`) so Decypharr can remove torrents. Without it, removals fail with HTTP 403 and the torrent stays in your High-Way cloud.

Notes:

- Cache checks, adding, file lists and download links use HighWay's transfer API.
- Torrents above the account's transfer limit are queued by HighWay instead of being rejected.
- Download links are valid for about 24 hours; `auto_expire_links_after` is capped at 23 hours.

All configuration options from [Real Debrid](./real-debrid/) apply (rate limits, workers, proxy, etc.).

See [Configuration Reference](../configuration/#debrid-providers) for full options.
