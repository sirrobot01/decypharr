# WebDAV Server

![WebDAV Server](../images/webdav.png)

Decypharr includes a built-in WebDAV server that provides direct access to your Debrid files, making them easily accessible to media players and other applications.


## Overview

While most Debrid providers have their own WebDAV servers, Decypharr's implementation offers faster access and additional features.

## Accessing the WebDAV Server

- URL: `http://localhost:8282/webdav` or `http://<your-server-ip>:8282/webdav`

## Configuration

You can configure WebDAV settings either globally or per-Debrid provider in your `config.json`:

```json
"webdav": {
  "torrents_refresh_interval": "15s",
  "download_links_refresh_interval": "40m",
  "folder_naming": "original_no_ext",
  "auto_expire_links_after": "3d",
  "rc_url": "http://localhost:5572",
  "rc_user": "username",
  "rc_pass": "password",
  "serve_from_rclone": false,
  "directories": {
      "Newly Added": {
        "filters": {
          "exclude": "9-1-1",
          "last_added": "20h"
        }
      }
  }
}
```

### Configuration Options

- `torrents_refresh_interval`: Interval for refreshing torrent data (e.g., `15s`, `1m`, `1h`).
- `download_links_refresh_interval`: Interval for refreshing download links (e.g., `40m`, `1h`).
- `workers`: Number of concurrent workers for processing requests.
- folder_naming: Naming convention for folders:
  - `original_no_ext`: Original file name without extension
  - `original`: Original file name with extension
  - `filename`: Torrent filename
  - `filename_no_ext`: Torrent filename without extension
  - `id`: Torrent ID
- `auto_expire_links_after`: Time after which download links will expire (e.g., `3d`, `1w`).
- `rc_url`, `rc_user`, `rc_pass`: Rclone RC configuration for VFS refreshes
- `directories`: A map of virtual folders to serve via the WebDAV server. The key is the virtual folder name, and the values are a map of filters and their values.
- `serve_from_rclone`: Whether to serve files directly from Rclone (disabled by default).

### Using with Media Players
The WebDAV server works well with media players like:

- Infuse
- VidHub
- Plex, Emby, Jellyfin (with rclone, Check [this guide](../guides/rclone.md))
- Kodi

### Mounting with Rclone
You can mount the WebDAV server locally using Rclone. Example configuration:

```conf
[decypharr]
type = webdav
url = http://localhost:8282/webdav/realdebrid
vendor = other
```
For a complete Rclone configuration example, see our [sample rclone.conf](../extras/rclone.conf).