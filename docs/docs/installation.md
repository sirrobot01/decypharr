# Installation

There are multiple ways to install and run Decypharr. Choose the method that works best for your setup.

## Docker Installation (Recommended)

Docker is the easiest way to get started with Decypharr.

### Available Docker Registries

You can use either Docker Hub or GitHub Container Registry to pull the image:

- Docker Hub: `cy01/blackhole:latest`
- GitHub Container Registry: `ghcr.io/sirrobot01/decypharr:latest`

### Docker Tags

- `latest`: The latest stable release
- `beta`: The latest beta release
- `vX.Y.Z`: A specific version (e.g., `v0.1.0`)
- `nightly`: The latest nightly build (usually unstable)
- `experimental`: The latest experimental build (highly unstable)

### Docker CLI Setup

Pull the Docker image:
```bash
docker pull cy01/blackhole:latest
```
Run the Docker container:
```bash
docker run -d \
  --name decypharr \
  -p 8282:8282 \
  -v /mnt/:/mnt \
  -v ./config/:/app \
  -e PUID=1000 \
  -e PGID=1000 \
  -e UMASK=002 \
  cy01/blackhole:latest
```

### Docker Compose Setup

Create a `docker-compose.yml` file with the following content:

```yaml
version: '3.7'
services:
  decypharr:
    image: cy01/blackhole:latest
    container_name: decypharr
    ports:
      - "8282:8282"
    volumes:
      - /mnt/:/mnt # Mount your media directory
      - ./config/:/app # config.json must be in this directory
      - QBIT_PORT=8282 # qBittorrent Port (optional)
    restart: unless-stopped
```

Run the Docker Compose setup:
```bash
docker-compose up -d
```

#### Notes for Docker Users

- Ensure that the `/mnt/` directory is mounted correctly to access your media files.
- The `./config/` directory should contain your `config.json` file.
- You can adjust the `PUID` and `PGID` environment variables to match your user and group IDs for proper file permissions.
- The `UMASK` environment variable can be set to control file permissions created by Decypharr.


## Binary Installation
If you prefer not to use Docker, you can download and run the binary directly.

Download the binary from the releases page
Create a configuration file (see Configuration)
Run the binary:
```bash
chmod +x decypharr
./decypharr --config /path/to/config/folder
```

The config directory should contain your config.json file.

## config.json

The `config.json` file is where you configure Decypharr. You can find a sample configuration file in the `configs` directory of the repository.

You can also configure Decypharr through the web interface, but it's recommended to start with the config file for initial setup.

```json
{
  "debrids": [
    {
      "name": "realdebrid",
      "api_key": "your_api_key_here",
      "folder": "/mnt/remote/realdebrid/__all__/",
      "use_webdav": true
    }
  ],
  "qbittorrent": {
    "download_folder": "/mnt/symlinks/",
    "categories": ["sonarr", "radarr"]
  },
  "use_auth": false,
  "log_level": "info",
  "port": "8282"
}
```