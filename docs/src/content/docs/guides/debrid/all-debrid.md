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

See [Configuration Reference](../configuration/#debrid-providers) for full options.

## Slot Management

AllDebrid has a limit of ~1000 active torrents. Use `slot_strategy` to automatically manage slots:

### Strategies

- **`remove_oldest`**: Before adding a new torrent, removes the oldest one if the limit is reached.
- **`remove_after_add`**: After adding a cached torrent, removes it from AllDebrid to free the slot. File links remain functional — streaming still works. If links expire later, the repair system automatically re-inserts the torrent.

### Configuration

```json
{
  "debrids": [
    {
      "provider": "alldebrid",
      "name": "All Debrid",
      "api_key": "YOUR_API_KEY",
      "slot_strategy": "remove_oldest",
      "limit": 1000
    }
  ]
}
```

The `limit` field defines the maximum number of torrents. `minimum_free_slot` reserves slots.
