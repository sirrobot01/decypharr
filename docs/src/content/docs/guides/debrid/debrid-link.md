---
title: Debrid Link Setup
description: Configure Debrid Link provider.
---

Debrid Link is a supported Debrid provider.

## Configuration

```json
{
  "debrids": [
    {
      "provider": "debridlink",
      "name": "Debrid Link",
      "api_key": "YOUR_API_KEY"
    }
  ]
}
```

Get your API key from the Debrid Link dashboard.

All configuration options from [Real Debrid](./real-debrid/) apply (rate limits, workers, proxy, etc.).

## Defaults

| Option                      | Default      |
| --------------------------- | ------------ |
| `rate_limit`                | `250/minute` |
| `torrents_refresh_interval` | `10m`        |

Debrid Link does not publish a rate limit. Decypharr uses its general default.

## Limits

- **Flood block:** If Debrid Link reports `floodDetected`, it blocks the API for about 1 hour. Decypharr stops all API requests for this API key for 1 hour. Do not set a high `rate_limit`.
- **Cache check:** Debrid Link has no cache check. A cached-only add finds this result after the add.
- **Active torrents:** If you reach the transfer limit, Decypharr keeps the torrent in its queue and tries again.

See [Configuration Reference](../configuration/#debrid-providers) for full options.
