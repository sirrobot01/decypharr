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
  sirrobot01/decypharr:latest
```

## Binary

Download the latest release from [GitHub Releases](https://github.com/sirrobot01/decypharr/releases).

```bash
# Extract
tar -xzf decypharr_linux_amd64.tar.gz

# Run
./decypharr --config /path/to/
```

## Managed (ElfHosted)

Prefer not to self-host? A managed Decypharr instance is available via [ElfHosted](https://store.elfhosted.com/product/decypharr/?utm_source=github&utm_medium=docs&utm_campaign=decypharr-docs), preconfigured alongside Sonarr/Radarr and connected to your debrid provider. Includes a 7-day trial.

## Next Steps

After installation, access the web UI. You'll be redirected to the [Setup Wizard](./quick-start/) for first-run configuration.
