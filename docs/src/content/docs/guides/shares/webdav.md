---
title: WebDAV Server
description: Access files via WebDAV protocol.
---

Decypharr includes a WebDAV server for browsing and streaming files without mounting.

The library tree, virtual folders, and folder naming are the same for every share. See
[Shares Overview](../overview/).

## Access WebDAV

**URL**: `http://decypharr:8282/webdav/`

## Mount in OS

### macOS

**Finder** → **Go** → **Connect to Server** (`Cmd+K`)

```
http://decypharr:8282/webdav/
```

Enter username and password.

### Windows

**File Explorer** → **This PC** → **Map network drive**

```
\\decypharr@8282\DavWWWRoot\webdav\
```

### Linux

```bash
# Install davfs2
sudo apt install davfs2

# Mount
sudo mount -t davfs http://decypharr:8282/webdav /mnt/decypharr

# With auth
sudo mount -t davfs -o username=USER,password=PASS \
  http://decypharr:8282/webdav /mnt/decypharr
```

## Authentication

WebDAV auth is controlled by:

```json
{
  "use_auth": true,
  "enable_webdav_auth": true
}
```

- `enable_webdav_auth: true`: Require Basic Auth
- `enable_webdav_auth: false`: Public access (not recommended)

## Streaming

WebDAV supports HTTP Range requests for streaming:

```bash
# Direct playback
vlc http://decypharr:8282/webdav/__all__/TorrentName/video.mkv
```

Provide username:password if auth enabled:

```bash
vlc http://user:pass@decypharr:8282/webdav/__all__/TorrentName/video.mkv
```

## STRM Files

Create STRM files pointing to WebDAV URLs:

```
http://decypharr:8282/webdav/sonarr/ShowName/S01E01.mkv
```

When Plex/Jellyfin plays the STRM, it streams from WebDAV.

## Performance

WebDAV streams directly from Debrid/Usenet. It does not use the [share cache](../overview/#share-cache)
— that cache serves NFS and SMB only. Performance depends on:

- Debrid provider speed
- Network bandwidth
- Client buffer settings

For best performance, use [DFS mounting](../../mounting/dfs/) instead of WebDAV.

## Troubleshooting

### Connection Refused

- Verify Decypharr is running: `curl http://decypharr:8282/version`
- Check firewall rules

### Authentication Failed

- Verify username/password in config
- Check `enable_webdav_auth` is `true`

### Slow Playback

WebDAV has no local cache. Consider:

- Using DFS/Rclone mount instead
- Increasing client buffer size
- Checking Debrid provider performance

## Security

:::caution
WebDAV uses Basic Auth (base64-encoded, not encrypted). Use HTTPS in production:
:::

```nginx
# nginx reverse proxy
location /webdav/ {
    proxy_pass http://decypharr:8282/webdav/;
    proxy_set_header Authorization $http_authorization;
    proxy_pass_header Authorization;
}
```

Or disable auth for internal network only:

```json
{
  "enable_webdav_auth": false
}
```
