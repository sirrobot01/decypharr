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
  --restart unless-stopped \
  -p 8282:8282 \
  -v /mnt/:/mnt:rshared \
  -v ./config/:/app \
  --device /dev/fuse:/dev/fuse:rwm \
  --cap-add SYS_ADMIN \
  --security-opt apparmor:unconfined \
  cy01/blackhole:latest
```

### Docker Compose Setup

Create a `docker-compose.yml` file with the following content:

```yaml
services:
  decypharr:
    image: cy01/blackhole:latest
    container_name: decypharr
    ports:
      - "8282:8282"
    volumes:
      - /mnt/:/mnt:rshared
      - ./config/:/app
    restart: unless-stopped
    devices:
      - /dev/fuse:/dev/fuse:rwm
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
```

Run the Docker Compose setup:
```bash
docker-compose up -d
```


## Binary Installation
If you prefer not to use Docker, you can download and run the binary directly.

Download your OS-specific release from the [release page](https://github.com/sirrobot01/decypharr/releases).
Create a configuration file (see Configuration)
Run the binary:

```bash
chmod +x decypharr
./decypharr --config /path/to/config/folder
```

### Notes for Docker Users

- Ensure that the `/mnt/` directory is mounted correctly to access your media files.
- You can adjust the `PUID` and `PGID` environment variables to match your user and group IDs for proper file permissions.
- The `UMASK` environment variable can be set to control file permissions created by Decypharr.

##### Health Checks
- Health checks are disabled by default. You can enable them by adding a `healthcheck` section in your `docker-compose.yml` file.
- Health checks the availability of several parts of the application;
    - The main web interface
    - The qBittorrent API
    - The WebDAV server (if enabled). You should disable health checks for the initial indexes as they can take a long time to complete.

```yaml
services:
  decypharr:
    ...
    ...
    healthcheck:
      test: ["CMD", "/usr/bin/healthcheck", "--config", "/app/"]
      interval: 10s
      timeout: 10s
      retries: 3
```
