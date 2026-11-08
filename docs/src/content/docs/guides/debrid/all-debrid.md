---
title: All Debrid Setup
description: Configure All Debrid provider.
---

All Debrid is a supported Debrid provider.

## Configuration

```json
{
  "debrids": [
    {
      "provider": "alldebrid",
      "name": "All Debrid",
      "api_key": "YOUR_API_KEY"
    }
  ]
}
```

Get your API key from the All Debrid dashboard.

All configuration options from [Real Debrid](./real-debrid/) apply (rate limits, workers, proxy, etc.).

## Defaults

| Option                      | Default      |
| --------------------------- | ------------ |
| `rate_limit`                | `600/minute` |
| `torrents_refresh_interval` | `10m`        |

All Debrid allows 12 API requests each second and 600 API requests each minute.

## Limits

- **Active magnets:** All Debrid allows 30 active magnets. If you reach this limit, Decypharr keeps the torrent in its queue and tries again.
- **Cache check:** All Debrid has no cache check. A cached-only add finds this result after the add.
- **Quiet swarms:** If a magnet has no peers, Decypharr restarts the magnet. If the magnet stays in this state, Decypharr marks it as failed.

See [Configuration Reference](../configuration/#debrid-providers) for full options.
