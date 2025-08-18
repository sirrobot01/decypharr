# Internal Mounting

This guide explains how to use Decypharr's internal mounting feature to eliminate the need for external rclone setup.

## Overview

![Decypharr Internal Mounting](../images/settings/rclone.png)

Instead of requiring users to install and configure rclone separately, Decypharr can now mount your WebDAV endpoints internally using rclone as a library dependency. This provides a seamless experience where files appear as regular filesystem paths without any external dependencies.

## Prerequisites

- **Docker users**: FUSE support may need to be enabled in the container depending on your Docker setup
- **macOS users**: May need [macFUSE](https://osxfuse.github.io/) installed for mounting functionality
- **Linux users**: FUSE should be available by default on most distributions
- **Windows users**: Mounting functionality may be limited

### Configuration Options

You can set the options in the Web UI or directly in the configuration file:

#### Note:
Check the Rclone documentation for more details on the available options: [Rclone Mount Options](https://rclone.org/commands/rclone_mount/).

## How It Works

1. **WebDAV Server**: Decypharr starts its internal WebDAV server for enabled providers
2. **Internal Mount**: Rclone is used internally to mount the WebDAV endpoint to a local filesystem path
3. **File Access**: Your applications can access files using regular filesystem paths like `/mnt/decypharr/realdebrid/__all__/MyMovie/`

## Benefits

- **Automatic Setup**: Mounting is handled automatically by Decypharr using internal rclone rcd
- **Filesystem Access**: Files appear as regular directories and files
- **Seamless Integration**: Works with existing media servers without changes

## Docker Compose

```yaml
version: '3.8'
services:
  decypharr:
    image: sirrobot01/decypharr:latest
    container_name: decypharr
    ports:
      - "8282:8282"
    volumes:
      - ./config:/config
      - /mnt:/mnt:rshared  # Important: use 'rshared' for mount propagation
    devices:
      - /dev/fuse:/dev/fuse:rwm
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
    environment:
      - UMASK=002
      - PUID=1000  # Change to your user ID
      - PGID=1000  # Change to your group ID
```

**Important Docker Notes:**
- Mount volumes with `:rshared` to allow mount propagation
- Include `/dev/fuse` device for FUSE mounting

## Troubleshooting

### Mount Failures

If mounting fails, check:

1. **FUSE Installation**: 
   - **macOS**: Install macFUSE from https://osxfuse.github.io/
   - **Linux**: Install fuse package (`apt install fuse` or `yum install fuse`)
   - **Docker**: Fuse is already included in the container, but ensure the host supports it
2. **Permissions**: Ensure the application has sufficient privileges

### No Mount Methods Available

If you see "no mount method available" errors:

1. **Check Platform Support**: Some platforms have limited FUSE support
2. **Install Dependencies**: Ensure FUSE libraries are installed
3. **Use WebDAV Directly**: Access files via `http://localhost:8282/webdav/provider/`
4. **External Mounting**: Use OS-native WebDAV mounting as fallback