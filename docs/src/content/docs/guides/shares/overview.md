---
title: Shares Overview
description: What the WebDAV, NFS, and SMB servers have in common — the library tree, folder naming, and the share cache.
---

A share exposes your library over a network protocol. Decypharr has three: [WebDAV](../webdav/),
[NFS](../nfs/), and [SMB](../smb/).

All three are thin adapters over the same library catalog. They show the same tree, they use the same
folder names, and they stream directly from the debrid or usenet source. Every share is **read-only**.

Shares are independent of the local mount. You can serve NFS while a DFS or rclone mount runs, and you
can enable more than one share at the same time.

## Which share to use

| Share  | Best for                                | Access control            |
|--------|-----------------------------------------|---------------------------|
| WebDAV | Quick access, STRM files, HTTP clients  | Basic Auth                |
| NFS    | Linux and macOS media servers           | Network ranges only       |
| SMB    | Windows clients, mixed networks         | User name, password, and network ranges |

For a local mount on the same host, DFS is faster than any share. Use a share when the client is a
different machine, or when the client cannot use a FUSE mount.

## Library tree

Every share exposes the same top-level tree:

```text
/
├── __all__/          # Every item
├── __bad__/          # Failed or broken items
├── torrents/         # Torrent items
├── nzbs/             # Usenet items
├── realdebrid/       # One folder per configured debrid provider
├── Movies, Shows/    # Your virtual folders
└── version.txt       # Running Decypharr version
```

Each folder holds one directory per item. Nested paths inside an item, such as season directories, are
kept. The same item can appear in more than one folder.

## Virtual folders

Virtual folders are filtered views of the library. Add a `4K` folder, and it shows only the items that
match your filters. The items stay available in `__all__` and in the other folders.

Virtual folders appear in every share and in the local mount. See
[Virtual Folders](../../virtual-folders/) to create them.

## Folder naming

`folder_naming` sets how Decypharr names each item folder. It applies to every share and to the local
mount:

```json
{
  "folder_naming": "filename"
}
```

| Value             | Example                 |
|-------------------|-------------------------|
| `filename`        | `Movie.2024.1080p.mkv`  |
| `original`        | `Original Torrent Name` |
| `filename_no_ext` | `Movie.2024.1080p`      |
| `original_no_ext` | `Original Torrent Name` |
| `infohash`        | `abc123def456...`       |

:::caution
Changing this renames every folder in the library. Media servers see the old paths disappear. Scan the
library again after a change.
:::

## Share cache

The share cache is an optional on-disk read cache. It serves the **NFS and SMB** servers. WebDAV does
not use it.

The cache is **off by default**. Turn it on to read through a local disk cache instead of streaming
every byte from the source. NFS and SMB use one cache, because they export the same tree.

The cache does not try to hold whole files. It keeps the parts that clients read more than once: file
headers, and the recently played part of a stream. Without it, every seek and every library scan opens
a new debrid session for bytes that were already fetched. Behind the cache, the source sees one
sequential stream per file.

Turn it on if clients seek a lot, if more than one client reads the same file, or if a media server
scans the library often. Leave it off if disk space is short.

This is separate from the DFS mount cache. It applies only to the NFS and SMB exports.

Open **Settings → Shares → Cache** to configure it:

| Setting         | Default                     | Description                                                        |
|-----------------|-----------------------------|--------------------------------------------------------------------|
| Enabled         | off                         | Turn the cache on. When off, reads stream straight from the source. |
| Cache Directory | `<config dir>/share-cache`  | Where cached content is written.                                    |
| Max Size        | 10GB                        | Disk budget for cached content.                                     |
| Max Age         | 24h                         | Content that nothing reads for this long is dropped.                |
| Chunk Size      | 4MB                         | Base fetch size. It doubles up to 16x while a stream stays sequential. |
| Read Ahead      | 16MB                        | Fetched beyond each read, so a sequential stream does not stall.    |

Give the cache a directory on a filesystem that supports hole punching, such as ext4, XFS, or APFS. To
stay inside the budget while a client holds a large file open, the cache releases blocks behind the read
head. ZFS and some overlay setups refuse this. There the size limit becomes advisory and the directory
can grow past it. Decypharr writes a warning to the log when this happens. On those filesystems, set a
smaller **Max Size**, or move the cache.

## Performance

A share without the cache streams straight from the source. Playback speed depends on the debrid
provider, your network, and the client buffer. Seeks cost a new source request.

To make streaming faster:

- Turn on the [share cache](#share-cache) for NFS and SMB.
- Raise the client readahead. See [Raise the readahead](../nfs/#raise-the-readahead) for NFS.
- Use a DFS mount when the client is on the Decypharr host.
