---
title: Installation
description: Install Decypharr via Docker or binary.
---

## Docker (Recommended)

### Docker Compose

Create a `docker-compose.yml`:

```yaml
services:
  decypharr:
    image: cy01/blackhole:latest
    container_name: decypharr
    ports:
      - "8282:8282"
      # Optional: NFSv4 (when NFS is enabled in Settings)
      # - "2049:20490/tcp"
      # Optional: SMB — Windows clients require host port 445 (when SMB is enabled in Settings)
      # - "445:1445/tcp"
    volumes:
      - /mnt/:/mnt:rshared
      - ./configs/:/app # config.json must be in this directory
    restart: unless-stopped
    devices:
      - /dev/fuse:/dev/fuse:rwm
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
```

Run:

```bash
docker compose up -d
```

Access at `http://localhost:8282`

### Docker Run

```bash
docker run -d \
  --name=decypharr \
  -p 8282:8282 \
  -v ./config:/app \
  -v ./downloads:/downloads \
  -v ./cache:/cache \
  -e PUID=1000 \
  -e PGID=1000 \
    --restart unless-stopped \
    --device /dev/fuse:/dev/fuse:rwm \
    --cap-add SYS_ADMIN \
    --security-opt apparmor:unconfined \
  cy01/blackhole:latest
```

## Binary

Download the latest release from [GitHub Releases](https://github.com/sirrobot01/decypharr/releases).

```bash
# Extract
tar -xzf decypharr_linux_amd64.tar.gz

# Run
./decypharr --config /path/to/
```

## Synology DSM / SRM

Native Synology packages can be built for DSM 7.3 and SRM 1.3 from the `synology/` spksrc overlay. Tagged releases can attach architecture-specific `.spk` files through the Synology GitHub Actions workflow.

After installing or upgrading the package, enable the FUSE helpers:

```bash
/var/packages/decypharr/target/bin/decypharr-fuse-fix
```

The Decypharr service itself remains unprivileged. Shared-folder ACLs must allow the package account and any consumers such as Sonarr, Radarr, Bazarr or Plex to traverse the configured mount path. See `synology/README.md` for details.

## Managed (ElfHosted)

Prefer not to self-host? A managed Decypharr instance is available
via [ElfHosted](https://store.elfhosted.com/product/decypharr/?utm_source=github&utm_medium=docs&utm_campaign=decypharr-docs),
preconfigured alongside Sonarr/Radarr and connected to your debrid provider. Includes a 7-day trial.

## Next Steps

After installation, access the web UI. You'll be redirected to the [Setup Wizard](./quick-start/) for first-run
configuration.
