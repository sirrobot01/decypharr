# Decypharr

![ui](docs/src/assets/images/index.png)

**Decypharr** is a **Media Gateway** for Debrid services and Usenet written in Go.

## What is Decypharr?

Decypharr provides a unified interface for Sonarr, Radarr, and other *Arr applications to access Debrid providers and
Usenet streaming.

## Features

- Mock Qbittorent and Sabnzbd API that supports the Arrs (Sonarr, Radarr, Lidarr etc)
- Multiple Debrid and usenet providers support with a single interface
- Direct Usenet streaming via NNTP (no separate download client required)

## Supported Debrid Providers

- [Real Debrid](https://real-debrid.com)
- [Torbox](https://torbox.app)
- [Debrid Link](https://debrid-link.com)
- [All Debrid](https://alldebrid.com)
- [Premiumize](https://www.premiumize.me)

## Quick Start

### Docker (Recommended)

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

> Prefer not to self-host? A managed Decypharr instance is available
> via [ElfHosted](https://store.elfhosted.com/product/decypharr/?utm_source=github&utm_medium=readme&utm_campaign=decypharr-readme),
> preconfigured alongside Sonarr/Radarr to route requests to your debrid provider (7-day trial).

## Documentation

For complete documentation, please visit our [Documentation](https://docs.decypharr.com).

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
