# General Configuration

This section covers the basic configuration options for Decypharr that apply to the entire application.

## Basic Settings

Here are the fundamental configuration options:

```json
{
  "use_auth": false,
  "port": 8282,
  "log_level": "info",
  "discord_webhook_url": "",
  "min_file_size": 0,
  "max_file_size": 0,
  "allowed_file_types": ["mp4", "mkv", "avi", ...],
  
}
```

### Configuration Options

#### Log Level
The `log_level` setting determines how verbose the application logs will be:

- `debug`: Detailed information, useful for troubleshooting
- `info`: General operational information (default)
- `warn`: Warning messages
- `error`: Error messages only
- `trace`: Very detailed information, including all requests and responses

#### Port

The `port` setting specifies the port on which Decypharr will run. The default is `8282`. You can change this to any available port on your server.

Ensure this port:

- Is not used by other applications
- Is accessible to your Arr applications
- Is properly exposed if using Docker (see the Docker Compose example in the Installation guide)


#### Authentication
The `use_auth` option enables basic authentication for the UI:

```json
"use_auth": true
```

When enabled, you'll need to provide a username and password to access the Decypharr interface.


#### File Size Limits

You can set minimum and maximum file size limits for torrents:
```json
"min_file_size": 0,
"max_file_size": 0
```

#### Allowed File Types
You can restrict the types of files that Decypharr will process by specifying allowed file extensions. This is useful for filtering out unwanted file types.

```json
"allowed_file_types": [
  "mp4", "mkv", "avi", "mov",
  "m4v", "mpg", "mpeg", "wmv",
  "m4a", "mp3", "flac", "wav"
]
```

If not specified, all movie, TV show, and music file types are allowed by default.


#### Discord Notifications
To receive notifications on Discord, add your webhook URL:
```json
"discord_webhook_url": "https://discord.com/api/webhooks/..."
```
This will send notifications for various events, such as successful downloads or errors.
