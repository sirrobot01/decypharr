# Arr Applications Configuration

Decypharr can integrate directly with Sonarr, Radarr, and other Arr applications. This section explains how to configure the Arr integration in your `config.json` file.

## Basic Configuration

The Arr applications are configured under the `arrs` key:

```json
"arrs": [
  {
    "name": "sonarr",
    "host": "http://sonarr:8989",
    "token": "your-sonarr-api-key",
    "cleanup": true
  },
  {
    "name": "radarr",
    "host": "http://radarr:7878",
    "token": "your-radarr-api-key",
    "cleanup": true
  }
]
```

### !!! note
This configuration is optional if you've already set up the qBittorrent client in your Arr applications with the correct host and token information. It's particularly useful for the Repair Worker functionality.


### Configuration Options
Each Arr application supports the following options:

- `name`: The name of the Arr application, which should match the category in qBittorrent
- `host`: The host URL of the Arr application, including protocol and port
- `token`: The API token/key of the Arr application
- `cleanup`: Whether to clean up the Arr queue (removes completed downloads). This is only useful for Sonarr.
- `skip_repair`: Automated repair will be skipped for this *arr.
- `download_uncached`: Whether to download uncached torrents (defaults to debrid/manual setting)

### Finding Your API Key
#### Sonarr/Radarr/Lidarr

1. Go to Sonarr > Settings > General
2. Look for "API Key" in the "Security" section
3. Copy the API key

### Multiple Arr Applications
You can configure multiple Arr applications by adding more entries to the arrs array:

```json
"arrs": [
  {
    "name": "sonarr",
    "host": "http://sonarr:8989",
    "token": "your-sonarr-api-key",
    "cleanup": true
  },
  {
    "name": "sonarr-anime",
    "host": "http://sonarr-anime:8989",
    "token": "your-sonarr-anime-api-key",
    "cleanup": true
    },
  {
    "name": "radarr",
    "host": "http://radarr:7878",
    "token": "your-radarr-api-key",
    "cleanup": false
  },
  {
    "name": "lidarr",
    "host": "http://lidarr:8686",
    "token": "your-lidarr-api-key",
    "cleanup": false
  }
]
```