
# Debrid Providers Configuration

Decypharr supports multiple Debrid providers. This section explains how to configure each provider in your `config.json` file.

## Basic Configuration

Each Debrid provider is configured in the `debrids` array:

```json
"debrids": [
  {
    "name": "realdebrid",
    "api_key": "your-api-key",
    "folder": "/mnt/remote/realdebrid/__all__/",
  },
  {
    "name": "alldebrid",
    "api_key": "your-api-key",
    "folder": "/mnt/remote/alldebrid/downloads/"
  }
]
```

### Provider Options

Each Debrid provider accepts the following configuration options:


#### Basic(Required) Options

- `name`: The name of the Debrid provider (realdebrid, alldebrid, debridlink, torbox)
- `host`: The API endpoint of the Debrid provider
- `api_key`: Your API key for the Debrid service (can be comma-separated for multiple keys)
- `folder`: The folder where your Debrid content is mounted (via webdav, rclone, zurg, etc.)

#### Advanced Options

- `rate_limit`: Rate limit for API requests (null by default)
- `download_uncached`: Whether to download uncached torrents (disabled by default)
- `check_cached`: Whether to check if torrents are cached (disabled by default)
- `use_webdav`: Whether to create a WebDAV server for this Debrid provider (disabled by default)
- `proxy`: Proxy URL for the Debrid provider (optional)

#### WebDAV and Rclone Options
- `torrents_refresh_interval`: Interval for refreshing torrent data (e.g., `15s`, `1m`, `1h`).
- `download_links_refresh_interval`: Interval for refreshing download links (e.g., `40m`, `1h`).
- `workers`: Number of concurrent workers for processing requests.
- `serve_from_rclone`: Whether to serve files directly from Rclone (disabled by default)
- `add_samples`: Whether to add sample files when adding torrents to debrid (disabled by default)
- `folder_naming`: Naming convention for folders:
    - `original_no_ext`: Original file name without extension
    - `original`: Original file name with extension
    - `filename`: Torrent filename
    - `filename_no_ext`: Torrent filename without extension
    - `id`: Torrent ID
    - `hash`: Torrent hash
- `auto_expire_links_after`: Time after which download links will expire (e.g., `3d`, `1w`).
- `rc_url`, `rc_user`, `rc_pass`, `rc_refresh_dirs`: Rclone RC configuration for VFS refreshes
- `directories`: A map of virtual folders to serve via the webDAV server. The key is the virtual folder name, and the values are map of filters and their value

#### Example of `directories` configuration
```json
    "directories": {
        "Newly Added": {
          "filters": {
            "exclude": "9-1-1",
            "last_added": "20h"
          }
        },
        "Spiderman Collection": {
          "filters": {
            "regex": "(?i)spider[-\\s]?man(\\s+collection|\\s+\\d|\\s+trilogy|\\s+complete|\\s+ultimate|\\s+box\\s+set|:?\\s+homecoming|:?\\s+far\\s+from\\s+home|:?\\s+no\\s+way\\s+home)"
          }
        }
      }
```

### Example Configuration

#### Real Debrid

```json
{
  "name": "realdebrid",
  "api_key": "your-api-key",
  "folder": "/mnt/remote/realdebrid/__all__/",
  "rate_limit": null,
  "download_uncached": false,
  "use_webdav": true
}
```

#### All Debrid

```json
{
  "name": "alldebrid",
  "api_key": "your-api-key",
  "folder": "/mnt/remote/alldebrid/torrents/",
  "rate_limit": null,
  "download_uncached": false,
  "use_webdav": true
}
```

#### Debrid Link

```json
{
  "name": "debridlink",
  "api_key": "your-api-key",
  "folder": "/mnt/remote/debridlink/torrents/",
  "rate_limit": null,
  "download_uncached": false,
  "use_webdav": true
}
```

#### Torbox

```json
{
  "name": "torbox",
  "api_key": "your-api-key",
  "folder": "/mnt/remote/torbox/torrents/",
  "rate_limit": null,
  "download_uncached": false,
  "use_webdav": true
}
```