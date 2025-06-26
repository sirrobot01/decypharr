# Setting up Decypharr with Rclone

This guide will help you set up Decypharr with Rclone, allowing you to use your Debrid providers as a remote storage solution.

#### Rclone
Make sure you have Rclone installed and configured on your system. You can follow the [Rclone installation guide](https://rclone.org/install/) for instructions.

It's recommended to use a docker version of Rclone, as it provides a consistent environment across different platforms. 


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
url = http://your-ip-or-domain:8282/webdav/realdebrid
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
      "rc_url": "rclone:5572"
    }
  ],
  "qbittorrent": {
    "download_folder": "data/media/symlinks/",
    "refresh_interval": 10
  }
}

```

### Docker Compose Setup

- Check your current user and group IDs by running `id -u` and `id -g` in your terminal. You can use these values to set the `PUID` and `PGID` environment variables in the Docker Compose file.
- You should also set `user` to your user ID and group ID in the Docker Compose file to ensure proper file permissions.

Create a `docker-compose.yml` file with the following content:

```yaml
services:
  decypharr:
    image: cy01/blackhole:latest
    container_name: decypharr
    user: "${PUID:-1000}:${PGID:-1000}"
    volumes:
      - /mnt/:/mnt:rslave
      - /opt/decypharr/:/app
    environment:
      - UMASK=002
      - PUID=1000 # Replace with your user ID
      - PGID=1000 # Replace with your group ID
    ports:
      - "8282:8282/tcp"
    restart: unless-stopped
  
  rclone:
    image: rclone/rclone:latest
    container_name: rclone
    restart: unless-stopped
    environment:
      TZ: UTC
    ports:
     - 5572:5572
    volumes:
      - /mnt/remote/realdebrid:/data:rshared
      - /opt/rclone/rclone.conf:/config/rclone/rclone.conf
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
    command: "mount decypharr: /data --allow-non-empty --allow-other --dir-cache-time 10s --rc --rc-addr :5572 --rc-no-auth"
```

#### Docker Notes

- Ensure that the `/mnt/` directory is mounted correctly to access your media files.
- You can check your current user and group IDs and UMASK by running `id -a` and `umask` commands in your terminal.
- You can adjust the `PUID` and `PGID` environment variables to match your user and group IDs for proper file permissions.
- Also adding `--uid=$YOUR_PUID --gid=$YOUR_PGID` to the `rclone mount` command can help with permissions.
- The `UMASK` environment variable can be set to control file permissions created by Decypharr.

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
url = http://your-ip-or-domain:8282/webdav/
vendor = other
pacer_min_sleep = 0
```

You'll still be able to access the directories via `/mnt/remote/realdebrid, /mnt/remote/alldebrid` etc


