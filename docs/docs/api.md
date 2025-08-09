# API Documentation

Decypharr provides a RESTful API for managing torrents, debrid services, and Arr integrations. The API requires authentication and all endpoints are prefixed with `/api`.

## Authentication

The API supports two authentication methods:

### 1. Session-based Authentication (Cookies)
Log in through the web interface (`/login`) to establish an authenticated session. The session cookie (`auth-session`) will be automatically included in subsequent API requests from the same browser session.

### 2. API Token Authentication (Bearer Token)
Use API tokens for programmatic access. Include the token in the `Authorization` header for each request:

- `Authorization: Bearer <your-token>`

## Interactive API Documentation

<swagger-ui src="api-spec.yaml"/>

## API Endpoints Overview

### Arrs Management
- `GET /api/arrs` - Get all configured Arr applications (Sonarr, Radarr, etc.)

### Content Management
- `POST /api/add` - Add torrent files or magnet links for processing through debrid services

### Repair Operations
- `POST /api/repair` - Start repair process for media items
- `GET /api/repair/jobs` - Get all repair jobs
- `POST /api/repair/jobs/{id}/process` - Process a specific repair job
- `POST /api/repair/jobs/{id}/stop` - Stop a running repair job
- `DELETE /api/repair/jobs` - Delete multiple repair jobs

### Torrent Management
- `GET /api/torrents` - Get all torrents
- `DELETE /api/torrents/{category}/{hash}` - Delete a specific torrent
- `DELETE /api/torrents/` - Delete multiple torrents

## Usage Examples

### Adding Content via API

#### Using API Token:
```bash
curl -H "Authorization: Bearer $API_TOKEN" -X POST http://localhost:8080/api/add \
  -F "arr=sonarr" \
  -F "debrid=realdebrid" \
  -F "urls=magnet:?xt=urn:btih:..." \
  -F "downloadUncached=true"
  -F "file=@/path/to/torrent/file.torrent"
  -F "callbackUrl=http://your.callback.url/endpoint"
```

#### Using Session Cookies:
```bash
# Login first (this sets the session cookie)
curl -c cookies.txt -X POST http://localhost:8080/login \
  -H "Content-Type: application/json" \
  -d '{"username": "your_username", "password": "your_password"}'

# Then use the session cookie for API calls
curl -b cookies.txt -X POST http://localhost:8080/api/add \
  -F "arr=sonarr" \
  -F "debrid=realdebrid" \
  -F "urls=magnet:?xt=urn:btih:..." \
  -F "downloadUncached=true"
```

### Getting Torrents

```bash
# With API token
curl -H "Authorization: Bearer $API_TOKEN" -X GET http://localhost:8080/api/torrents
```

### Starting a Repair Job

```bash
# With API token
curl -H "Authorization: Bearer $API_TOKEN" -X POST http://localhost:8080/api/repair \
  -H "Content-Type: application/json" \
  -d '{
    "arrName": "sonarr",
    "mediaIds": ["123", "456"],
    "autoProcess": true,
    "async": true
  }'
```