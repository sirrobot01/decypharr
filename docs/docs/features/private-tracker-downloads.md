# Private Tracker Downloads

It is against the rules of most private trackers to download using debrid services. That's because debrid services do not seed back.

Despite that, **many torrents from private trackers are cached on debrid services**.

This can happen if the exact same torrent is uploaded to a public tracker or if another user downloads the torrent from the private tracker using their debrid account.

However, you do **_NOT_** want to be the first person who downloads and caches the private tracker torrent because it is a very quick way to get your private tracker account banned.

Fortunately, decypharr offers a feature that allows you to check whether a private tracker torrent has _already_ been cached.

In a way, this feature lets you use your private trackers to find hashes for the latest releases that have not yet been indexed by zilean, torrentio, and other debrid-focused indexers.

This allows you to add private tracker torrents to your debrid account without breaking the most common private tracker rules. This significantly reduces the chance of account bans, **but please read the `Risks` section below** for more details and other precautions you should make.

## Risks

A lot of care has gone into ensuring this feature is compliant with most private tracker rules:

- The passkey is not leaked
- The private tracker announce URLs are not leaked
- The private tracker swarm is not leaked
- Even the torrent content is not leaked (by you)

You are merely downloading it from another source. It's not much different than downloading a torrent that has been uploaded to MegaUpload or another file hoster.

**But it is NOT completely risk-free.**

### Suspicious-looking activity

To use this feature, you must download the `.torrent` file from the private tracker. But since you will never leech the content, it can make your account look suspicious.

In fact, there is a strictly forbidden technique called `ghostleeching` that also requires downloading of the `.torrent` file, and tracker admins might suspect that this is what you are doing.

We know of one user who got banned from a Unit3D-based tracker for this.

**Here is what is recommended:**

- Be a good private tracker user in general. Perma-seed, upload, contribute
- Only enable `Interactive Search` in the arrs (disable `Automatic Search`)
- Only use it for content that is not on public sources yet, and you need to watch **RIGHT NOW** without having time to wait for the download to finish
- Do **NOT** use it to avoid seeding

### Accidentally disable this feature

Another big risk is that you might accidentally disable the feature. The consequence will be that you actually leech the torrent from the tracker, don't seed it, and expose the private swarm to an untrusted third party.

You should avoid this at all costs.

Therefore, to reduce the risk further, it is recommended to enable the feature using both methods:

1. Using the global `Always Remove Tracker URLs` setting in your decypharr `config.json`
2. And by enabling the `First and Last First` setting in Radarr / Sonarr

This way, if one of them gets disabled, you have another backup.

## How to enable this feature

### Always Remove Tracker URLs

- In the web UI under `Settings -> QBitTorrent -> Always Remove Tracker URLs`
- Or in your `config.json` by setting the `qbittorrent.always_rm_tracker_url` to `true`

This ensures that the Tracker URLs are removed from **ALL torrents** (regardless of whether they are public, private, or how they were added).

But this can make downloads of uncached torrents slower or stall because the tracker helps the client find peers to download from.

If the torrent file has no tracker URLs, the torrent client can try to find peers for public torrents using [DHT](https://en.wikipedia.org/wiki/Mainline_DHT). However, this may be less efficient than connecting to a tracker, and the downloads may be slower or stall.

If you only download cached torrents, there is no further downside to enabling this option.

### Only on specific Arr-app clients and indexers

Alternatively, you can toggle it only for specific download clients and indexers in the Arr-apps...

- Enable `Show Advanced Settings` in your Arr app
- Add a new download client in `Settings -> Download Clients` and call it something like `Decypharr (Private)`
- Enable the `First and Last First` checkbox, which will tell Decypharr to remove the tracker URLs
- Add a duplicate version of your private tracker indexer for Decypharr downloads
  - Untick `Enable Automatic Search`
  - Tick `Enable Interactive Search`
  - Set `Download Client` to your new `Decypharr (Private)` client (requires `Show Advanced Settings`)

If you are using Prowlarr to sync your indexers, you can't set the `Download Client` in Prowlarr. You must update it directly in your Arr-apps after the indexers get synced. But future updates to the indexers won't reset the setting.

### Test it

After enabling the feature, try adding a [public torrent](https://ubuntu.com/download/alternative-downloads) through the Decypharr UI and a **public torrent** through your Arr-apps.

Then check the decypharr log to check for a log entry like...

```log
Removed 2 tracker URLs from torrent file
```

If you see this log entry, it means the tracker URLs are being stripped from your torrents and you can safely enable it on private tracker indexers.

## How it works

When you add a new torrent through the QBitTorrent API or through the Web UI, decypharr converts your torrent into a magnet link and then uses your debrid service's API to download that magnet link.

The torrent magnet link contains:

1. The `info hash` that uniquely identifies the torrent, files, and file names
2. The torrent name
3. The URLs of the tracker to connect to

Private tracker URLs in torrents contain a `passkey`. This is a unique identifier that ties the torrent file to your private tracker account.

Only if the `passkey` is valid will the tracker allow the torrent client to connect and download the files. This is also how private torrent trackers measure your downloads and uploads.

The `Remove Tracker URLs` feature removes all the tracker URLs (which include your private `passkey`). This means when decypharr attempts to download the torrent, it only passes the `info hash` and torrent name to the debrid service.

Without the tracker URLs, your debrid service has no way to connect to the private tracker to download the files, and your `passkey` and the private torrent tracker swarm are not exposed.

**But if the torrent is already cached, it's immediately added to your account.**
