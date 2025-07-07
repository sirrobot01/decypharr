# Private Tracker Downloads

It is against the rules of most private trackers to download using debrid services. That's because debrid services do not seed back.

Despite that, many torrents from private trackers are cached on debrid services. This can happen if the exact same torrent is uploaded to a public tracker or if another user downloads the torrent from the private tracker using their debrid account.

However, you do _NOT_ want to be the first person who downloads and caches the private tracker torrent because it could get your private tracker account banned.

Fortunately, decypharr offers a feature that allow you to check whether a private tracker torrent has _already_ been cached.

This allows you to add many private tracker torrents to your debrid account without breaking any rules or risking your account.

In a way, this feature lets you use your private trackers to find hashes for the latest releases that have not yet been indexed by zilean, torrentio, and other debrid-focused indexers.

## How to enable this feature

This is achieved using the `Remove Tracker URLs` settings:

- In the web UI under `Settings -> QBitTorrent -> Always Remove Tracker URLs`
- Or in your `config.json` by setting the `qbittorrent.always_rm_tracker_url` to `true`
- Or in Radarr / Sonarr by enabling the `First and Last First` checkbox in `Settings -> Download Clients -> Your Decypharr Client`

After enabling the feature, try adding a [public torrent](https://ubuntu.com/download/alternative-downloads) and check the decypharr log to check for a log entry like...

```log
Removed 2 tracker URLs from torrent file
```

If you see this log entry it means the tracker urls are being stripped from your torrents.

## How it works

When you add a new torrent through the QBitTorrent API or through the Web UI, decypharr converts your torrent into a magnet link and then uses your debrid service's API to download that magnet link.

The torrent magnet link contains:

1. The `info hash` that uniquely identifies the torrent, files and file names
2. The torrent name
3. The URLs of the tracker to connect to

Private tracker URLs in torrents contain a `passkey`. This is a unique identifier that ties the torrent file to your private tracker account.

Only if the `passkey` is valid, will the tracker allow the torrent client to connect and download the files. This is also how private torrent tracker measure your downloads and uploads.

The `Remove Tracker URLs` feature removes all the tracker urls (which include your private `passkey`). This means when decypharr attemps to downlaod the torrent, it only passes the `info hash` and torrent name to the debrid service.

Without the tracker URLs your debrid service has no way to connect to the private tracker to download the files and your `passkey` is not exposed.

**But if the torrent is already cached, it's immediately added to your account.**

## Risks

A lot of care has gone into ensuring this feature removes all identifying information from the torrent files.

We also have automated tests that run whenever a new docker image is published that verify if any of the new code changes break the tracker URL removal feature.

Most likely, your biggest risk is that you accidentally disable the feature.

Therefore, to reduce the risk further it is recommended to enable the feature using both methods:

1. Using the global `Always Remove Tracker URLs` setting in your decypharr `config.json`
2. And by enabling the `First and Last First` setting in Radarr / Sonarr

This way, if one of them gets disabled, you have another backup.

## Other downsides

The only other downside is that when this feature is enabled, uncached downloads might be slower or not download at all.

The tracker helps the client find peers to download from.

If the torrent file has no tracker URLs, the torrent client can try to find peers for public torrents using [DHT](https://en.wikipedia.org/wiki/Mainline_DHT).

But it may be less efficient than connecting to a tracker and the downloads may be slower or stall.

But if you only download cached torrents anyways, there is no downside.
