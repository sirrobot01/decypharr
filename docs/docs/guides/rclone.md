# Setting up Decypharr with Rclone

This guide will help you set up Decypharr with Rclone, allowing you to use your Debrid providers as a remote storage solution.

#### Rclone
Make sure you have Rclone installed and configured on your system. You can follow the [Rclone installation guide](https://rclone.org/install/) for instructions.

It's recommended to use docker version of Rclone, as it provides a consistent environment across different platforms. 


### Steps

We'll be using docker compose to set up Rclone and Decypharr together.

#### Note
This guide assumes you have a basic understanding of Docker and Docker Compose. If you're new to Docker, consider checking out the [Docker documentation](https://docs.docker.com/get-started/) for more information.

Also, ensure you have Docker and Docker Compose installed on your system. You can find installation instructions in the [Docker documentation](https://docs.docker.com/get-docker/) and [Docker Compose documentation](https://docs.docker.com/compose/install/).


Create a directory for your Decypharr and Rclone setup:
```bash
mkdir -p /opt/decypharr
mkdir -p /opt/rclone
mkdir -p /mnt/remote/realdebrid

# Set permissions
chown -R $USER:$USER /opt/decypharr
chown -R $USER:$USER /opt/rclone
chown -R $USER:$USER /mnt/remote/realdebrid
```

Create a `rclone.conf` file in `/opt/rclone/` with your Rclone configuration. 

```conf
[decypharr]
type = webdav
url = https://your-ip-or-domain:8282/webdav/realdebrid
vendor = other
pacer_min_sleep = 0
```

Create a `config.json` file in `/opt/decypharr/` with your Decypharr configuration. 

```json
{
  "debrids": [
    {
      "name": "realdebrid",
      "api_key": "realdebrid_key",
      "folder": "/mnt/remote/realdebrid/__all__/",
      "rate_limit": "250/minute",
      "use_webdav": true,
      "rc_url": "http://your-ip-address:5572" // Rclone RC URL
    }
  ],
  "qbittorrent": {
    "download_folder": "data/media/symlinks/",
    "refresh_interval": 10
  }
}

```

Create a `docker-compose.yml` file with the following content:

```yaml
services:
  decypharr:
    image: cy01/blackhole:latest
    container_name: decypharr
    user: "1000:1000"
    volumes:
      - /mnt/:/mnt
      - /opt/decypharr/:/app
    environment:
      - PUID=1000
      - PGID=1000
      - UMASK=002
    ports:
      - "8282:8282/tcp"
    restart: unless-stopped
  
  rclone:
    image: rclone/rclone:latest
    container_name: rclone
    restart: unless-stopped
    environment:
      TZ: UTC
      PUID: 1000
      PGID: 1000
    ports:
     - 5572:5572
    volumes:
      - /mnt/remote/realdebrid:/data:rshared
      - /opt/rclone/rclone.conf:/config/rclone/rclone.conf
      - /mnt:/mnt
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
    devices:
      - /dev/fuse:/dev/fuse:rwm
    depends_on:
      decypharr:
        condition: service_healthy
        restart: true
    command: "mount decypharr: /data --allow-non-empty --allow-other --uid=1000 --gid=1000 --umask=002 --dir-cache-time 10s --rc --rc-addr :5572 --rc-no-auth "
```

Start the containers:
```bash
docker-compose up -d
```

Access the Decypharr web interface at `http://your-ip-address:8282` and configure your settings as needed.

- Access your webdav server at `http://your-ip-address:8282/webdav` to see your files.
- You should be able to see your files in the `/mnt/remote/realdebrid/__all__/` directory.
- You can now use your Debrid provider as a remote storage solution with Rclone and Decypharr.
- You can also use the Rclone mount command to mount your Debrid provider locally. For example:


### Notes

- Make sure to replace `your-ip-address` with the actual IP address of your server.
- You can use multiple Debrid providers by adding them to the `debrids` array in the `config.json` file.

For each provider, you'll need a different rclone. OR you can change your `rclone.conf`


```apache
[decypharr]
type = webdav
url = https://your-ip-or-domain:8282/webdav/
vendor = other
pacer_min_sleep = 0
```

You'll still be able to access the directories via `/mnt/remote/realdebrid, /mnt/remote/alldebrid` etc


