# Configuration Overview

Decypharr uses a JSON configuration file to manage its settings. This file should be named `config.json` and placed in your configured directory.

## Basic Configuration

Here's a minimal configuration to get started:

```json
{
  "debrids": [
    {
      "name": "realdebrid",
      "api_key": "realdebrid_key",
      "folder": "/mnt/remote/realdebrid/__all__/",
      "use_webdav": true
    }
  ],
  "qbittorrent": {
    "port": "8282",
    "download_folder": "/mnt/symlinks/",
    "categories": ["sonarr", "radarr"]
  },
  "repair": {
    "enabled": false,
    "interval": "12h",
    "run_on_start": false
  },
  "use_auth": false,
  "log_level": "info"
}
```

### Configuration Sections

Decypharr's configuration is divided into several sections:

- [General Configuration](general.md) - Basic settings like logging and authentication
- [Debrid Providers](debrid.md) - Configure one or more Debrid services
- [qBittorrent Settings](qbittorrent.md) - Settings for the qBittorrent API
- [Arr Integration](arrs.md) - Configuration for Sonarr, Radarr, etc.

Full Configuration Example
For a complete configuration file with all available options, see our [full configuration example](../extras/config.full.json).