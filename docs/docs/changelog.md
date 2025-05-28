# Changelog

## 1.0.0

- Add WebDAV support for debrid providers
- Some refactoring and code cleanup
- Fixes
    - Fix Alldebrid not downloading torrents
    - Fix Alldebrid not downloading uncached torrents
    - Fix uncached torrents not being downloaded for RealDebrid
- Add support for multiple download API keys for debrid providers
- Add support for editable config.json via the UI
- Fix downloading timeout
- Fix UMASK for Windows
- Retries 50x(except 503) errors for RD


## 0.5.0

- A more refined repair worker (with more control)
- UI Improvements
    - Pagination for torrents
    - Dark mode
    - Ordered torrents table
- Fix Arr API flaky behavior
- Discord Notifications
- Minor bug fixes
- Add Tautulli support
    - playback_failed event triggers a repair
- Miscellaneous improvements
    - Add an option to skip the repair worker for a specific arr
    - Arr specific uncached downloading option
    - Option to download uncached torrents from UI
- Remove QbitTorrent Log level (Use the global log level)

## 0.4.2

- Hotfixes
    - Fix saving torrents error
    - Fix bugs with the UI
- Speed improvements

## 0.4.1

- Adds optional UI authentication
- Downloaded Torrents persist on restart
- Fixes
    - Fix Alldebrid struggling to find the correct file
- Minor bug fixes or speed-gains
- A new cleanup worker to clean up ARR queues

## 0.4.0

- Add support for multiple debrid providers
- A full-fledged UI for adding torrents, repairing files, viewing config and managing torrents
- Fix issues with Alldebrid
    - Fix file transversal bug
    - Fix files with no parent directory
- Logging
    - Add a more robust logging system
    - Add logging to a file
    - Add logging to the UI
- Qbittorrent
    - Add support for tags (creating, deleting, listing)
    - Add support for categories (creating, deleting, listing)
    - Fix issues with arr sending torrents using a different content type

## 0.3.3

- Add AllDebrid Support
- Fix Torbox not downloading uncached torrents
- Fix Rar files being downloaded

## 0.3.2

- Fix DebridLink not downloading
- Fix Torbox with uncached torrents
- Add new /internal/cached endpoint to check if an hash is cached
- Implement per-debrid local cache
- Fix file check for torbox
- Other minor bug fixes

## 0.3.1

- Add DebridLink Support
- Refactor error handling

## 0.3.0

- Add UI for adding torrents
- Refraction of the code
- Fix Torbox bug
- Update CI/CD
- Update Readme

## 0.2.7

- Add support for multiple debrid providers
- Add Torbox support
- Add support for configurable debrid cache checks
- Add support for configurable debrid download uncached torrents

## 0.2.6

- Delete torrent for empty matched files
- Update Readme

## 0.2.5

- Fix ContentPath not being set prior
- Rewrote Readme
- Cleaned up the code

## 0.2.4

- Add file download support (Sequential Download)
- Fix http handler error
- Fix *arrs map failing concurrently
- Fix cache not being updated

## 0.2.3

- Delete uncached items from RD
- Fail if the torrent is not cached (optional)
- Fix cache not being updated

## 0.2.2

- Fix name mismatch in the cache
- Fix directory mapping with mounts
- Add Support for refreshing the *arrs

## 0.2.1

- Fix Uncached torrents not being downloaded/downloaded
- Minor bug fixed
- Fix Race condition in the cache and file system

## 0.2.0

- Implement 0.2.0-beta changes
- Removed Blackhole
- Added QbitTorrent API
- Cleaned up the code

## 0.2.0-beta

- Switch to QbitTorrent API instead of Blackhole
- Rewrote the whole codebase

## 0.1.4

- Rewrote Report log
- Fix YTS, 1337x not grabbing infohash
- Fix Torrent symlink bug

## 0.1.3

- Searching for infohashes in the xml description/summary/comments
- Added local cache support
- Added max cache size
- Rewrite blackhole.go
- Bug fixes
    - Fixed indexer getting disabled
    - Fixed blackhole not working

## 0.1.2

- Bug fixes
- Code cleanup
- Get available hashes at once

## 0.1.1

- Added support for "No Blackhole" for Arrs
- Added support for "Cached Only" for Proxy
- Bug Fixes

## 0.1.0

- Initial Release
- Added Real Debrid Support
- Added Arrs Support
- Added Proxy Support
- Added Basic Authentication for Proxy
- Added Rate Limiting for Debrid Providers