---
title: Rclone (External)
description: Mount with an Rclone process that you start and control.
---

Use this mount type if you already run Rclone yourself, or if you want full control of the Rclone
configuration. Decypharr does not start, stop, or mount Rclone in this mode. Decypharr only sends
cache refresh commands to your Rclone instance.

## How it works

1. You run Rclone with the remote control (RC) API enabled.
2. You mount the Decypharr [WebDAV server](../../shares/webdav/) with your own Rclone remote.
3. Decypharr calls the RC API to refresh the Rclone VFS when torrents or files change.

:::caution
The WebDAV server must stay enabled. If `disable_webdav` is `true`, the external mount does not start.
:::

## Configuration

In `config.json`:

```json
{
  "mount": {
    "type": "external_rclone",
    "external_rclone": {
      "rc_url": "http://localhost:5572",
      "rc_username": "user",
      "rc_password": "pass"
    }
  }
}
```

| Setting       | Purpose                                          |
|---------------|--------------------------------------------------|
| `rc_url`      | Address of the Rclone RC API                     |
| `rc_username` | RC user name. Leave empty if RC auth is disabled |
| `rc_password` | RC password. Leave empty if RC auth is disabled  |

If you leave these fields empty, Decypharr uses the RC settings of the first debrid provider.

## Start Rclone

Enable the RC API when you start Rclone:

```bash
rclone rcd --rc-addr=:5572 --rc-user=user --rc-pass=pass
```

Add the Decypharr WebDAV server as a remote:

```bash
rclone config create decypharr webdav \
  url=http://decypharr:8282/webdav/ \
  vendor=other \
  user=USER \
  pass=PASS
```

Then mount the remote:

```bash
rclone mount decypharr: /mnt/decypharr \
  --allow-other \
  --dir-cache-time 1000h \
  --vfs-cache-mode writes \
  --rc --rc-addr=:5572 --rc-user=user --rc-pass=pass
```

Set `--dir-cache-time` to a long value. Decypharr refreshes the directory cache through the RC API
when the contents change.

## Troubleshooting

### Directories do not update

- Make sure the RC API is reachable from Decypharr: `curl -u user:pass http://localhost:5572/rc/noop -X POST`
- Make sure `rc_url`, `rc_username`, and `rc_password` match the Rclone flags.
- Check the Decypharr logs for refresh errors.

### Mount does not start

- Check that `disable_webdav` is `false`.
- Check that the WebDAV URL and credentials in the Rclone remote are correct.
