---
title: Frequently Asked Questions
description: Common questions about Decypharr.
---

## General

### What is Decypharr?

Decypharr is a media gateway that provides a unified interface for accessing Debrid providers (Real Debrid, All Debrid,
etc.) and Usenet via Sonarr, Radarr, and other *Arr applications. It acts as a QBitTorrent/Sabnzbd-compatible download
client.

### Is it free?

Yes, Decypharr itself is free and open source. However, you need subscriptions to:

- Debrid providers (Real Debrid, All Debrid, etc.)
- Usenet providers (if using Usenet features)

### How is this different from just using Debrid directly?

Decypharr adds:

- Arr integration (Sonarr/Radarr compatibility)
- File mounting (DFS, Rclone, WebDAV)
- Automated repair for broken links
- Usenet + Debrid hybrid support
- Centralized management

## Setup & Configuration

### Where is the config file located?

- **Docker**: `/config/config.json` (mapped volume)
- **Binary**: You choose the location on first run `./decypharr --config /path/to/`

### Can I use environment variables instead of config.json?

Yes! All config options support environment variables using double underscore notation:

```bash
PORT=8282
DEBRIDS__0__PROVIDER=realdebrid
DEBRIDS__0__API_KEY=your_key
```

See [Configuration Reference](../guides/configuration/) for all options.

### How do I get my API token?

After setup, go to **Settings** → **Auth** in the web UI. Your API token is displayed once after initial setup - save it
immediately.

To regenerate:

```bash
curl -X POST -H "Authorization: Bearer OLD_TOKEN" \
  http://localhost:8282/api/refresh-token
```

## Debrid Providers

### Can I use multiple Debrid providers?

Yes! Add multiple providers in config:

```json
{
  "debrids": [
    {"provider": "realdebrid", "api_key": "..."},
    {"provider": "alldebrid", "api_key": "..."}
  ]
}
```

Decypharr will automatically distribute torrents across providers based on available slots.

### How do I handle Debrid rate limits?

Configure per-provider rate limits:

```json
{
  "debrids": [
    {
      "provider": "realdebrid",
      "rate_limit": "200/minute",
      "repair_rate_limit": "60/minute"
    }
  ]
}
```

Or add multiple API keys for rotation:

```json
{
  "download_api_keys": ["KEY1", "KEY2", "KEY3"]
}
```

### What happens when Debrid slots are full?

Configure `minimum_free_slot` to switch to backup provider:

```json
{
  "debrids": [
    {
      "provider": "realdebrid",
      "minimum_free_slot": 5
    },
    {
      "provider": "alldebrid",
      "minimum_free_slot": 0
    }
  ]
}
```

If RD has <5 free slots, Decypharr uses All Debrid.

## Mounting

### Which mount type should I use?

| Use Case                  | Recommended         |
|---------------------------|---------------------|
| Streaming-focused         | **DFS**             |
| Need write caching        | **Rclone**          |
| Already have Rclone setup | **External Rclone** |
| API/WebDAV only           | **None**            |

**DFS** is recommended for most users.
On Windows, DFS mounting requires WinFsp to be installed.

### How do I fix "Permission Denied" on mount?

Set `uid`/`gid` to match your media server user:

```bash
id plex
# uid=1001(plex) gid=1001(plex)
```

```json
{
  "mount": {
    "dfs": {
      "uid": 1001,
      "gid": 1001
    }
  }
}
```

### Can I mount without FUSE?

Yes, use WebDAV:

```
http://decypharr:8282/webdav/
```

Mount as network drive in Windows/macOS/Linux. See [WebDAV Guide](../guides/mounting/webdav/).

## Usenet

### Do I need Sabnzbd or NZBGet?

No! Decypharr connects directly to NNTP servers. Just add your Usenet provider(s) in config.

### Why is Usenet processing slow?

Increase connection limits:

```json
{
  "usenet": {
    "max_connections": 20,
    "max_concurrent_nzb": 3
  }
}
```

And per-provider:

```json
{
  "usenet": {
    "providers": [
      {
        "max_connections": 30
      }
    ]
  }
}
```

## Arr Integration

### Path mapping not working?

Ensure Arr and Decypharr see files at the **same path**.

**Wrong** (Docker):

```yaml
decypharr:
  volumes:
    - /mnt/storage:/data
sonarr:
  volumes:
    - /mnt/storage:/media  # Different path!
```

**Correct**:

```yaml
decypharr:
  volumes:
    - /mnt/storage:/mnt/storage
sonarr:
  volumes:
    - /mnt/storage:/mnt/storage  # Same path!
```

### Downloads not importing in Arr?

1. Check download action is set correctly (`symlink` for mounts)
2. Verify mount path is accessible
3. Check Arr logs for specific error

### Can I use both Debrid and Usenet in same Arr?

Yes! Add both download clients:

1. **Decypharr (QBitTorrent)** for torrents
2. **Decypharr (Sabnzbd)** for NZBs

Set different priorities in Arr.

## Health Checker

### When should I enable auto-repair?

Set `auto_repair: true` if:

- You have stable providers.
- You want fully automated operation.
- You're comfortable with the sweep deleting broken Arr file records and triggering a search.

Set `auto_repair: false` if:

- You're testing the health checker on a new library.
- You want to review brokens before letting the system act.
- You're worried about Arr API rate limits.

In both cases, single-entry **Recheck & fix** from the Browse UI remains available regardless of the global setting.

### What's the difference between repair strategies?

- **`per_entry`**: probe stops at the first broken file in an entry. Faster on broken libraries; tells you which entries are intact.
- **`per_file`**: probe every file. Use when you want a complete broken-file list per entry.

### What's `recheck_interval`?

It's how long an entry's last successful check stays "fresh." Healthy entries probed within this window are skipped on the next sweep, so a healthy library where nothing has changed does almost no work.

## Performance

### How much disk space does caching use?

Configure `disk_cache_size`:

```json
{
  "mount": {
    "dfs": {
      "disk_cache_size": "50GB"
    }
  }
}
```

Actual usage depends on your viewing patterns.

### Streaming is buffering constantly

1. Increase chunk size:
   ```json
   {"dfs": {"chunk_size": "20MB"}}
   ```
2. Increase read-ahead (Usenet):
   ```json
   {"usenet": {"read_ahead": "32MB"}}
   ```
3. Check Debrid provider performance
4. Verify network bandwidth

## Troubleshooting

### WebUI won't load

1. Verify Decypharr is running:
   ```bash
   docker logs decypharr
   curl http://localhost:8282/version
   ```
2. Check port binding
3. Check firewall rules

### "Setup Required" loop

Delete config to restart setup:

```bash
rm /config/config.json  # Docker
# or
rm /path/to/config.json  # Binary
```

### High CPU/Memory usage

1. Reduce concurrent workers:
   ```json
   {
     "debrids": [{"workers": 25}],
     "repair": {"workers": 1}
   }
   ```
2. Check for repair loops (disable auto-repair temporarily)
3. Review logs for errors

### Where are the logs?

**Docker**:

```bash
docker logs decypharr
docker logs -f decypharr  # Follow
```

**Binary**: stdout (redirect to file if needed)

Set log level:

```json
{"log_level": "debug"}
```

## Migration

### Moving from MKVToolNix/Stremio/etc?

Decypharr can coexist:

1. Keep existing setup running
2. Add Decypharr as additional download client in Arrs
3. Set higher priority for Decypharr
4. Test with new downloads
5. Once stable, remove old client

### Switching from Rclone mount to DFS?

1. Stop Decypharr
2. Change config:
   ```json
   {"mount": {"type": "dfs"}}
   ```
3. Restart
4. Verify mount at same path
5. Test playback

No need to re-download - Decypharr reuses existing Debrid torrents.
