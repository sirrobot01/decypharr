---
title: API Reference
description: REST API endpoints.
---

Decypharr provides a REST API for programmatic access.

## Authentication

Include API token in Authorization header:

```bash
curl -H "Authorization: Bearer YOUR_API_TOKEN" \
  http://localhost:8282/api/torrents
```

Get API token from **Settings** → **Auth** after login.

## Endpoints

### GET /version

Get Decypharr version.

```bash
curl http://localhost:8282/version
```

**Response:**

```json
{
  "version": "1.0.0"
}
```

### GET /api/config

Get current configuration.

```bash
curl -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/config
```

### POST /api/config

Update configuration.

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"log_level": "debug"}' \
  http://localhost:8282/api/config
```

### GET /api/torrents

List all torrents.

```bash
curl -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/torrents
```

**Query Parameters:**

- `category`: Filter by category
- `hash`: Get specific torrent

### POST /api/add

Add torrent or NZB.

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -F "file=@file.torrent" \
  http://localhost:8282/api/add
```

Or with URL:

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -d '{"url": "magnet:?xt=..."}' \
  http://localhost:8282/api/add
```

### DELETE /api/torrents

Delete torrent(s).

```bash
curl -X DELETE \
  -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/torrents?category=sonarr&hash=abc123
```

### GET /api/repair/config

Read the current health-checker config.

### PUT /api/repair/config

Update the health-checker config (validates cron, workers, source).

### GET /api/repair/status

Active run summary, last completed run, and counts of entries by status.

### POST /api/repair/run

Trigger a sweep now. Optional JSON body fields:

| Field                 | Type    | Description                                                        |
|-----------------------|---------|--------------------------------------------------------------------|
| `ignore_last_checked` | boolean | Probe entries even when their last health check is still fresh.    |
| `auto_repair`         | boolean | Override the configured auto-repair setting for this run.          |
| `unrestrict_link`     | boolean | For torrent entries, probe by generating an unrestricted link instead of calling the provider check endpoint. |
| `protocol`            | string  | `all`, `torrent`, or `nzb`. Selects which protocols this run probes. |

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"protocol":"all","unrestrict_link":true}' \
  http://localhost:8282/api/repair/run
```

Returns `409 Conflict` when a sweep is already running.

### POST /api/repair/stop

Cancel the active sweep.

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/repair/stop
```

### GET /api/repair/runs

Run history.

```bash
curl -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/repair/runs
```

### GET /api/repair/runs/{id}

Run detail.

### DELETE /api/repair/runs

Clear run history.

### GET /api/repair/health

List entry health. Optional `?status=broken` filter.

```bash
curl -H "Authorization: Bearer TOKEN" \
  'http://localhost:8282/api/repair/health?status=broken'
```

### GET /api/repair/health/{name}

Per-entry health, including the broken-file list.

### POST /api/repair/health/{name}/check

Force-recheck a single entry.

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  'http://localhost:8282/api/repair/health/My.Show.S01/check'
```

### POST /api/repair/recheck/media

Recheck a single Arr media item. Set `fix` to `true` to repair through Arr after checking.

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"arr":"Sonarr","media_id":"123","fix":true}' \
  http://localhost:8282/api/repair/recheck/media
```

### GET /api/arrs

List connected Arrs.

```bash
curl -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/arrs
```

### POST /api/refresh-token

Regenerate API token.

```bash
curl -X POST \
  -H "Authorization: Bearer OLD_TOKEN" \
  http://localhost:8282/api/refresh-token
```

**Response:**

```json
{
  "api_token": "NEW_TOKEN"
}
```

## QBitTorrent API

Decypharr implements QBitTorrent Web API for Arr compatibility.

### POST /api/v2/auth/login

Login (compatibility endpoint).

```bash
curl -X POST \
  -d "username=admin&password=pass" \
  http://localhost:8282/api/v2/auth/login
```

### GET /api/v2/torrents/info

List torrents (QBit format).

```bash
curl -H "Authorization: Bearer TOKEN" \
  http://localhost:8282/api/v2/torrents/info
```

### POST /api/v2/torrents/add

Add torrent (QBit format).

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -F "urls=magnet:?xt=..." \
  -F "category=sonarr" \
  http://localhost:8282/api/v2/torrents/add
```

### POST /api/v2/torrents/delete

Delete torrents (QBit format).

```bash
curl -X POST \
  -H "Authorization: Bearer TOKEN" \
  -d "hashes=hash1|hash2" \
  http://localhost:8282/api/v2/torrents/delete
```

## Browse API

Hierarchical file browsing (WebDAV-style).

### GET /api/browse/

List root groups (`__all__`, `__bad__`, categories).

### GET /api/browse/{group}

List torrents in group.

### GET /api/browse/{group}/{torrent}

List files in torrent.

### GET /api/browse/download/{torrent}/{file}

Download specific file.

## Error Responses

```json
{
  "error": "Error message",
  "code": 400
}
```

**Status Codes:**

- `200`: Success
- `400`: Bad Request
- `401`: Unauthorized (invalid token)
- `404`: Not Found
- `500`: Internal Server Error

## Rate Limiting

API respects Debrid provider rate limits configured in `config.json`. No additional API rate limiting.

## Examples

### Python

```python
import requests

TOKEN = "your_api_token"
BASE_URL = "http://localhost:8282"

headers = {"Authorization": f"Bearer {TOKEN}"}

# List torrents
r = requests.get(f"{BASE_URL}/api/torrents", headers=headers)
torrents = r.json()

# Add torrent
r = requests.post(
    f"{BASE_URL}/api/add",
    headers=headers,
    json={"url": "magnet:?xt=..."}
)
```

### JavaScript

```javascript
const TOKEN = 'your_api_token';
const BASE_URL = 'http://localhost:8282';

const headers = {
  'Authorization': `Bearer ${TOKEN}`,
  'Content-Type': 'application/json'
};

// List torrents
fetch(`${BASE_URL}/api/torrents`, { headers })
  .then(r => r.json())
  .then(data => console.log(data));

// Add torrent
fetch(`${BASE_URL}/api/add`, {
  method: 'POST',
  headers,
  body: JSON.stringify({ url: 'magnet:?xt=...' })
});
```
