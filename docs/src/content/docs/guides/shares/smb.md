---
title: SMB Server
description: Expose Decypharr libraries as a read-only SMB share for Windows, macOS, and Linux clients.
---

:::caution
The SMB server is experimental. It has passed client acceptance on Linux; the Windows and macOS test matrix is still
in progress. Sessions are signed but **not encrypted** — file contents are readable on the wire. Serve trusted
networks only.
:::

Decypharr includes a read-only SMB server (SMB 2.1 and 3.1.1). It exposes the same library tree as WebDAV and NFS as
one share. Windows Explorer, macOS Finder, Linux `cifs`, and media clients with SMB support can browse and stream it.

Like WebDAV and NFS, it is a thin protocol adapter over the library catalog. It streams directly from the
debrid/usenet source. The library tree, virtual folders, and folder naming are the same for every share — see
[Shares Overview](../overview/). You can put an optional on-disk cache in front of it, shared with the NFS server.
Configure it in **Settings → Shares → Cache**; see [Share cache](../overview/#share-cache).

## Enable SMB

Open **Settings → Mount Settings**. Enable **SMB Server**. Set a **Username** and **Password** — the share grants no
anonymous access. Save.

The **Stats** page has a **Connect to Your Library** panel. It shows the exact connection commands for your host
once SMB is enabled.

The NTLM domain a client sends is ignored, so any workgroup or machine name works. **Require Signing** refuses
clients that will not sign; the server signs whenever the client asks for it either way. Leave it off on a trusted
LAN — mandatory signing costs CPU at streaming bitrates.

## Docker ports

The image runs Decypharr as the configured PUID/PGID, so it listens on an unprivileged port inside the container —
`1445` by default. Windows connects to TCP port 445 only, so publish the host's 445:

~~~yaml
services:
  decypharr:
    ports:
      - "8282:8282"
      - "445:1445/tcp"
~~~

macOS and Linux clients can dial a non-standard port directly, so the 445 mapping is only required for Windows.

## Connect from Windows

Windows requires the share on host port 445. Map a drive letter:

~~~text
net use Z: \\SERVER_IP\decypharr /user:USERNAME
~~~

Or enter `\\SERVER_IP\decypharr` in File Explorer's address bar. Windows prompts for the password.

## Connect from macOS

**Finder → Go → Connect to Server** (`Cmd+K`):

~~~text
smb://USERNAME@SERVER_IP:1445/decypharr
~~~

Omit `:1445` when the host maps port 445.

## Mount on Linux

~~~bash
sudo mkdir -p /mnt/decypharr
sudo mount -t cifs -o vers=3.1.1,port=1445,user=USERNAME,ro //SERVER_IP/decypharr /mnt/decypharr
~~~

Omit `port=` when the host maps port 445. For `/etc/fstab`, store the password in a credentials file instead of the
mount line:

~~~text
//SERVER_IP/decypharr    /mnt/decypharr    cifs    vers=3.1.1,credentials=/etc/decypharr-smb.cred,ro,_netdev,x-systemd.automount,noauto    0 0
~~~

~~~text
# /etc/decypharr-smb.cred (chmod 600)
username=USERNAME
password=PASSWORD
~~~

## Network access

Access is restricted twice: by the username/password, and by `allowed_networks`. The defaults permit loopback,
private IPv4 networks, IPv6 unique-local addresses, and IPv6 link-local addresses.

~~~json
{
  "smb": {
    "enabled": true,
    "username": "media",
    "password": "change-me",
    "allowed_networks": ["192.168.1.0/24"]
  }
}
~~~
