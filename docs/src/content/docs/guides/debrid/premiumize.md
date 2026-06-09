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

See [Configuration Reference](../configuration/#debrid-providers) for full options.
