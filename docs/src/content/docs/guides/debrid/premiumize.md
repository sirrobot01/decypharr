---
title: Premiumize Setup
description: Configure Premiumize provider.
---

Premiumize is a supported Debrid provider.

## Configuration

```json
{
  "debrids": [
    {
      "provider": "premiumize",
      "name": "Premiumize",
      "api_key": "YOUR_API_KEY"
    }
  ]
}
```

Get your API key from your Premiumize account page.

All configuration options from [Real Debrid](./real-debrid/) apply (rate limits, workers, proxy, etc.).

## Defaults

| Option                      | Default      |
| --------------------------- | ------------ |
| `rate_limit`                | `120/minute` |
| `torrents_refresh_interval` | `10m`        |

Premiumize does not publish a rate limit.

## Limits

- **Rate limit:** If Premiumize reports `rate_limit_reached`, Decypharr stops API requests for this API key for 1 minute.
- **First sync:** The first sync after a start reads the files of each finished transfer. This takes more time on a large account. Later syncs use the stored file list.
- **Fair use:** If your account has no fair-use points left, Premiumize refuses new links until the points reset.

See [Configuration Reference](../configuration/#debrid-providers) for full options.
