#!/bin/sh
set -e

# Default values
PUID=${PUID:-1000}
PGID=${PGID:-1000}

# Create group if it doesn't exist
if ! getent group "$PGID" > /dev/null 2>&1; then
    addgroup -g "$PGID" appgroup
fi

# Create user if it doesn't exist
if ! getent passwd "$PUID" > /dev/null 2>&1; then
    adduser -D -u "$PUID" -G "$(getent group "$PGID" | cut -d: -f1)" -s /bin/sh appuser
fi

# Get the actual username and groupname
USERNAME=$(getent passwd "$PUID" | cut -d: -f1)
GROUPNAME=$(getent group "$PGID" | cut -d: -f1)

# Ensure directories exist and have correct permissions
mkdir -p /app/logs /app/cache
chown -R "$PUID:$PGID" /app
chmod 755 /app
touch /app/logs/decypharr.log
chmod 666 /app/logs/decypharr.log

# Export for rclone/fuse
export USER="$USERNAME"
export HOME="/app"

# Execute the command as the specified user
exec su-exec "$PUID:$PGID" "$@"