# Decypharr
![Decypharr UI - Light Mode](images/main-light.png){: .light-mode-image}
![Decypharr UI - Dark Mode](images/main.png){: .dark-mode-image}

**Decypharr** is an implementation of QbitTorrent with **Multiple Debrid service support**, written in Go.

## What is Decypharr?

**TLDR**; Decypharr is a self-hosted, open-source download client that integrates with multiple Debrid services. It provides a user-friendly interface for managing files and supports popular media management applications like Sonarr and Radarr.


## Key Features

- Mock Qbittorent API that supports Sonarr, Radarr, Lidarr, and other Arr applications
- Multiple Debrid providers support
- WebDAV server support for each Debrid provider with an optional mounting feature(using [rclone](https://rclone.org))
- Repair Worker for missing files, symlinks etc

## Supported Debrid Providers

- [Real Debrid](https://real-debrid.com)
- [Torbox](https://torbox.app)
- [Debrid Link](https://debrid-link.com)
- [All Debrid](https://alldebrid.com)

## Getting Started

Check out our [Installation Guide](installation.md) to get started with Decypharr.