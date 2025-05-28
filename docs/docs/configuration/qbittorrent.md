# qBittorrent Configuration

Decypharr emulates a qBittorrent instance to integrate with Arr applications. This section explains how to configure the qBittorrent settings in your `config.json` file.

## Basic Configuration

The qBittorrent functionality is configured under the `qbittorrent` key:

```json
"qbittorrent": {
  "download_folder": "/mnt/symlinks/",
  "categories": ["sonarr", "radarr", "lidarr"],
  "refresh_interval": 5
}
```

### Configuration Options
#### Required Settings

- `download_folder`: The folder where symlinks or downloaded files will be placed
- `categories`: An array of categories to organize downloads (usually matches your Arr applications)

#### Advanced Settings

- `refresh_interval`: How often (in seconds) to refresh the Arrs Monitored Downloads (default: 5)
- `max_downloads`: The maximum number of concurrent downloads. This is only for downloading real files(Not symlinks). If you set this to 0, it will download all files at once. This is not recommended for most users.(default: 5)
- `skip_pre_cache`: This option disables the process of pre-caching files. This caches a small portion of the file to speed up your *arrs import process. 

#### Categories
Categories help organize your downloads and match them to specific Arr applications. Typically, you'll want to configure categories that match your Sonarr, Radarr, or other Arr applications:

```json
"categories": ["sonarr", "radarr", "lidarr", "readarr"]
```

When setting up your Arr applications to connect to Decypharr, you'll specify these same category names.

#### Download Folder

The `download_folder` setting specifies where Decypharr will place downloaded files or create symlinks:

```json
"download_folder": "/mnt/symlinks/"
```

This folder should be:

- Accessible to Decypharr
- Accessible to your Arr applications
- Have sufficient space if downloading files locally


#### Refresh Interval
The refresh_interval setting controls how often Decypharr checks for updates from your Arr applications:

```json
"refresh_interval": 5
```


This value is in seconds. Lower values provide more responsive updates but may increase CPU usage.