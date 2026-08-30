---
title: NFS Server
description: Expose Decypharr libraries to any NFSv4 client — Linux, macOS, and more.
---

Decypharr includes a read-only NFSv4 server. It exposes the same library tree as WebDAV, including every custom
virtual folder. Any NFSv4 client can mount it. The library then appears as a normal folder for Plex, Jellyfin, Emby,
or file managers.

Like WebDAV, it is a thin protocol adapter over the library catalog. It streams directly from the debrid/usenet
source. The library tree, virtual folders, and folder naming are the same for every share — see
[Shares Overview](../overview/). You can put an optional on-disk cache in front of it. See
[Share cache](../overview/#share-cache).

NFSv4 uses one TCP port. There is no portmapper and no mount service. This removes the port-111 conflicts that
NFSv3 setups have with the host's own `rpcbind`.

## Enable NFS

Open **Settings → Mount Settings**. Enable **NFSv4 Server**. Save.

NFS runs independently of the selected local mount type. You can run a DFS or rclone mount *and* serve NFS at the
same time.

The **Stats** page has a **Connect to Your Library** panel. It shows the exact mount commands for your host once NFS
is enabled.

## Exports

The server exports the [library tree](../overview/#library-tree). Mount the whole tree (`SERVER_IP:/`) or a single
library (`SERVER_IP:/Movies`). Nested paths inside torrents, such as season directories, are preserved.

## Restarts

Client mounts survive a Decypharr restart or upgrade. Cached content also survives, when the cache is on: it
validates each file against the catalog on open, and discards content whose size or timestamp changed. The server persists its filehandle key in the data directory
and rebuilds long filehandles from the catalog. Clients re-open their files without a remount. Byte-range locks do
not survive a restart, but the export is read-only, so media clients do not use them.

## Docker ports

The image runs Decypharr as the configured PUID/PGID, so it listens on an unprivileged port inside the container —
`20490` by default. Publish it as the standard NFS port:

~~~yaml
services:
  decypharr:
    ports:
      - "8282:8282"
      - "2049:20490/tcp"
~~~

If host port 2049 is taken (the host runs its own NFS server), publish a different host port and give it in the
mount command with `port=`.

## Mount on Linux

~~~bash
sudo mkdir -p /mnt/decypharr
sudo mount -t nfs -o vers=4.0,tcp,port=2049,ro SERVER_IP:/ /mnt/decypharr
~~~

`port=` names the published host port. Use `port=20490` when Decypharr runs directly on the host without Docker.
Port 2049 is the NFS default, so `port=2049` can be omitted.

Mount a single library instead of the whole tree:

~~~bash
sudo mount -t nfs -o vers=4.0,tcp,ro SERVER_IP:/Movies /mnt/movies
~~~

To make it permanent, add a line to `/etc/fstab`:

~~~text
SERVER_IP:/    /mnt/decypharr    nfs    vers=4.0,tcp,ro,soft,_netdev,x-systemd.automount,noauto    0 0
~~~

When the client is the same host that runs the Decypharr container, add `x-systemd.requires=docker.service`. A plain
entry is mounted at boot before the container exists and fails. `x-systemd.automount` defers the mount until
something first touches the directory, which handles the ordering. Prefer `soft` over `hard` there as well: on
shutdown Docker stops the server before the mount is torn down, and a `hard` mount hangs the reboot.

~~~text
127.0.0.1:/    /mnt/decypharr    nfs    vers=4.0,tcp,ro,soft,timeo=600,retrans=5,rsize=1048576,_netdev,x-systemd.automount,noauto,x-systemd.requires=docker.service    0 0
~~~

Mounting over loopback on the Decypharr host itself is fine. Use `127.0.0.1` and raise `rsize` to `1048576`: there
is no network cost to large reads.

### Raise the readahead

The Linux kernel defaults NFS readahead to 128 KB. That is far too small for a streaming source: the client issues
small serial reads and waits on each one. Raise it to 16 MB after every mount:

~~~bash
echo 16384 | sudo tee /sys/class/bdi/$(mountpoint -d /mnt/decypharr)/read_ahead_kb
~~~

In our benchmarks this one change made sequential streaming 4× faster and halved seek latency.

## Mount on macOS

macOS needs a reserved source port (`resvport`). The mountpoint must exist first — macOS does not create it, and a
missing target fails with `invalid file system`:

~~~bash
sudo mkdir -p /Volumes/decypharr
sudo mount -t nfs -o vers=4.0,tcp,port=2049,resvport,ro SERVER_IP:/ /Volumes/decypharr
~~~

Running Decypharr directly on macOS without Docker, point at the listen port:

~~~bash
sudo mount -t nfs -o vers=4.0,tcp,port=20490,resvport,ro localhost:/ /Volumes/decypharr
~~~

## Kodi

Kodi's built-in NFS browser discovers servers over NFSv3 and portmapper, which Decypharr no longer speaks. Use an
OS-level mount as shown above and add the mounted path as a local source. On LibreELEC/CoreELEC, add the mount as a
`systemd` mount unit under `/storage/.config/system.d/`.

## Network access

NFSv4 carries only AUTH_SYS identities — there is no password. Access is restricted by `allowed_networks`. The
defaults permit loopback, private IPv4 networks, IPv6 unique-local addresses, and IPv6 link-local addresses.

For a tighter policy, allow only the client subnet:

~~~json
{
  "nfs": {
    "enabled": true,
    "allowed_networks": ["192.168.1.0/24"]
  }
}
~~~
