#!/bin/sh
set -e

# Default values
PUID=${PUID:-1000}
PGID=${PGID:-1000}
UMASK=${UMASK:-022}
DATA_PATH=${DATA_PATH:-/data}

# Set umask
umask "$UMASK"

# Function to create directories and files
setup_directories() {
    # Ensure directories exist
    mkdir -p "$DATA_PATH" "$DATA_PATH/logs" "$DATA_PATH/cache" 2>/dev/null || true

    # Create log file if it doesn't exist
    touch "$DATA_PATH/logs/decypharr.log" 2>/dev/null || true

    # Try to set permissions if possible
    chmod 755 "$DATA_PATH" 2>/dev/null || true
    chmod 666 "$DATA_PATH/logs/decypharr.log" 2>/dev/null || true
}

# Check if we're running as root
if [ "$(id -u)" != "0" ]; then
    echo "Running as non-root user $(id -u):$(id -g) with umask $UMASK"

    # Try to create directories as the current user
    setup_directories

    export USER="$(id -un)"
    export HOME="$DATA_PATH"

    exec "$@"
fi

echo "Running as root, setting up user $PUID:$PGID with umask $UMASK"

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

# Create directories and set proper ownership
mkdir -p "$DATA_PATH/logs" "$DATA_PATH/cache"
chown -R "$PUID:$PGID" "$DATA_PATH"
chmod 755 "$DATA_PATH"
touch "$DATA_PATH/logs/decypharr.log"
chmod 666 "$DATA_PATH/logs/decypharr.log"

# Export for rclone/fuse
export USER="$USERNAME"
export HOME="$DATA_PATH"

# Execute the command as the specified user
exec su-exec "$PUID:$PGID" "$@"