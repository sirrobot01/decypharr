# Internal Mounting

This guide explains how to use Decypharr's internal mounting feature to eliminate the need for external rclone setup.

## Overview

Instead of requiring users to install and configure rclone separately, Decypharr can now mount your WebDAV endpoints internally using rclone as a library dependency. This provides a seamless experience where files appear as regular filesystem paths without any external dependencies.

## Prerequisites

- **Docker users**: FUSE support may need to be enabled in the container depending on your Docker setup
- **macOS users**: May need [macFUSE](https://osxfuse.github.io/) installed for mounting functionality
- **Linux users**: FUSE should be available by default on most distributions
- **Windows users**: Mounting functionality may be limited

## Configuration

To enable internal mounting, add these fields to your debrid provider configuration:

```json
{
  "debrids": [
    {
      "name": "realdebrid",
      "api_key": "YOUR_API_KEY",
      "folder": "/mnt/remote/realdebrid",
      "use_webdav": true,
      "enable_internal_mount": true,
      "internal_mount_path": "/mnt/decypharr/realdebrid",
      "torrents_refresh_interval": "15s",
      "download_links_refresh_interval": "40m",
      "auto_expire_links_after": "3d",
      "workers": 50
    }
  ]
}
```

### Configuration Options

You can set the options in the Web UI or directly in the configuration file:

#### Note:
Check the Rclone documentation for more details on the available options: [Rclone Mount Options](https://rclone.org/commands/rclone_mount/).

## How It Works

1. **WebDAV Server**: Decypharr starts its internal WebDAV server for enabled providers
2. **Internal Mount**: Rclone libraries are used internally to mount the WebDAV endpoint to a local filesystem path
3. **File Access**: Your applications can access files using regular filesystem paths like `/mnt/decypharr/realdebrid/MyMovie/`

## Benefits

- **Zero External Dependencies**: No need to install or configure rclone separately - it's built into Decypharr
- **Automatic Setup**: Mounting is handled automatically by Decypharr using internal rclone libraries
- **Filesystem Access**: Files appear as regular directories and files
- **Seamless Integration**: Works with existing media servers without changes

## Docker Compose Example

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
      - /mnt:/mnt:rshared  # Important: use 'shared' for mount propagation
    privileged: true  # Required for mounting
    devices:
      - /dev/fuse:/dev/fuse:rwm
    cap_add:
      - SYS_ADMIN
    environment:
      - UMASK=002
```

⚠️ **Important Docker Notes:**
- Use `privileged: true` or specific capabilities for mounting
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

### Log Messages

Monitor logs for mounting status:

```bash
docker logs decypharr | grep -i mount
```

## Migration from External Rclone

If you're currently using external rclone:

1. **Remove External Rclone**: Uninstall or disable your existing rclone setup
2. **Update Configuration**: Modify rclone configuration to use the internal mount.
3. **Restart Decypharr**: Restart the Decypharr service to apply changes
4. **Verify Mounts**: Check that files are accessible via the new internal mount paths
5. **Test Applications**: Ensure your media applications can access files as expected
6. **Monitor Logs**: Check Decypharr logs for any mount-related messages

## Limitations

- **FUSE Dependency**: Internal mounting requires FUSE support on your system
- **Platform Support**: 
  - **Linux**: Full support with FUSE
  - **macOS**: Requires macFUSE installation
  - **Windows**: Limited support
- **Read-Only**: Mounted filesystems are read-only (which is appropriate for debrid content)
- **Startup Delay**: Initial mounting may take a few seconds during startup
- **Fallback**: If mounting fails, files remain accessible via WebDAV interface

## Advanced Configuration

For advanced users, you can customize the rclone mounting behavior by modifying the mount options in the UI. The default configuration prioritizes:

- **Stability**: Conservative caching and timeout settings
- **Resource Usage**: Minimal memory and CPU overhead