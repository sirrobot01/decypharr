# Usage Guide

This guide will help you get started with Decypharr after installation.

## Basic Setup

1. Create your `config.json` file (see [Configuration](configuration/index.md) for details)
2. Start the Decypharr service using Docker or binary
3. Access the UI at `http://localhost:8282` (or your configured host/port)
4. Connect your Arr applications (Sonarr, Radarr, etc.)

## Connecting to Sonarr/Radarr

To connect Decypharr to your Sonarr or Radarr instance:

1. In Sonarr/Radarr, go to **Settings → Download Client → Add Client → qBittorrent**
2. Configure the following settings:
   - **Host**: `localhost` (or the IP of your Decypharr server)
   - **Port**: `8282` (or your configured qBittorrent port)
   - **Username**: `http://sonarr:8989` (your Arr host with http/https)
   - **Password**: `sonarr_token` (your Arr API token)
   - **Category**: e.g., `sonarr`, `radarr` (match what you configured in Decypharr)
   - **Use SSL**: `No`
   - **Sequential Download**: `No` or `Yes` (if you want to download torrents locally instead of symlink)
3. Click **Test** to verify the connection
4. Click **Save** to add the download client

![Sonarr/Radarr Setup](images/sonarr-setup.png)

## Using the UI

The Decypharr UI provides a familiar qBittorrent-like interface with additional features for Debrid services:

- Add new torrents
- Monitor download status
- Access WebDAV functionality
- Edit your configuration

Access the UI at `http://localhost:8282` or your configured host/port.