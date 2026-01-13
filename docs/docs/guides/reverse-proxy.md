# Reverse Proxy Setup

This guide covers how to run Decypharr behind a reverse proxy like Nginx, including configuration for subfolders and external authentication.

## Configuration Options

Decypharr can be accessed either via a dedicated subdomain or as a subfolder on an existing domain:

| Setup | URL Example | Config |
|-------|-------------|--------|
| Subfolder (recommended) | `https://example.com/decypharr/` | `url_base: "/decypharr/"` |
| Subdomain | `https://decypharr.example.com/` | `url_base: "/"` |

## Subfolder Setup (Recommended)

Running Decypharr at a subfolder allows you to host multiple services on a single domain.

### Decypharr Configuration

Set the `url_base` in your `config.yaml`:

```yaml
url_base: "/decypharr/"
```

!!! note
    The `url_base` must start and end with a `/`. Decypharr will normalize it automatically if you forget.

### Nginx Configuration

```nginx
upstream decypharr {
    server 127.0.0.1:8282;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name example.com;

    include /etc/nginx/snippets/ssl-params.conf;
    ssl_certificate /etc/nginx/ssl/example.crt;
    ssl_certificate_key /etc/nginx/ssl/example.key;

    location /decypharr/ {
        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr/decypharr/;
    }

    # Other services...
}
```

!!! warning "Important"
    The `proxy_pass` URL **must** include the subfolder path (`/decypharr/`) to preserve the full request path. Using `proxy_pass http://decypharr/;` will strip the path and cause 404 errors.

### With External Authentication

When using external authentication (Authelia, Authentik, etc.) or basic auth, bypass it for API routes so Sonarr/Radarr can still communicate with Decypharr:

```nginx
upstream decypharr {
    server 127.0.0.1:8282;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name example.com;

    include /etc/nginx/snippets/ssl-params.conf;
    ssl_certificate /etc/nginx/ssl/example.crt;
    ssl_certificate_key /etc/nginx/ssl/example.key;

    # Decypharr API - no external auth (uses built-in API token)
    location /decypharr/api/ {
        auth_request off;  # or auth_basic off;
        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr/decypharr/api/;
    }

    # Decypharr web UI - require auth
    location /decypharr/ {
        auth_request /authelia;
        auth_request_set $target_url $scheme://$http_host$request_uri;
        error_page 401 =302 https://auth.example.com/?rd=$target_url;

        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr/decypharr/;
    }

    # Authelia endpoint
    location = /authelia {
        internal;
        proxy_pass http://authelia:9091/api/verify;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
    }

    # Other services...
}
```

## Subdomain Setup

If you prefer a dedicated subdomain for Decypharr.

### Decypharr Configuration

Use the default `url_base` (or omit it):

```yaml
url_base: "/"
```

### Nginx Configuration

```nginx
upstream decypharr {
    server 127.0.0.1:8282;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name decypharr.example.com;

    include /etc/nginx/snippets/ssl-params.conf;
    ssl_certificate /etc/nginx/ssl/decypharr.example.com.crt;
    ssl_certificate_key /etc/nginx/ssl/decypharr.example.com.key;

    location / {
        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr;
    }
}
```

### With External Authentication

```nginx
upstream decypharr {
    server 127.0.0.1:8282;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name decypharr.example.com;

    include /etc/nginx/snippets/ssl-params.conf;
    ssl_certificate /etc/nginx/ssl/decypharr.example.com.crt;
    ssl_certificate_key /etc/nginx/ssl/decypharr.example.com.key;

    # API routes - no external auth (uses built-in API token)
    location /api/ {
        auth_request off;  # or auth_basic off;
        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr;
    }

    # Everything else - require auth
    location / {
        auth_request /authelia;
        auth_request_set $target_url $scheme://$http_host$request_uri;
        auth_request_set $user $upstream_http_remote_user;
        auth_request_set $groups $upstream_http_remote_groups;
        error_page 401 =302 https://auth.example.com/?rd=$target_url;

        include /etc/nginx/snippets/proxy.conf;
        proxy_pass http://decypharr;
    }

    # Authelia endpoint
    location = /authelia {
        internal;
        proxy_pass http://authelia:9091/api/verify;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Basic Authentication

If using basic auth instead of external SSO:

```nginx
# API routes - no auth
location /api/ {
    auth_basic off;
    include /etc/nginx/snippets/proxy.conf;
    proxy_pass http://decypharr;
}

# Web UI - require basic auth
location / {
    auth_basic "Decypharr";
    auth_basic_user_file /etc/nginx/.htpasswd;
    include /etc/nginx/snippets/proxy.conf;
    proxy_pass http://decypharr;
}
```

Create the `.htpasswd` file:

```bash
htpasswd -c /etc/nginx/.htpasswd username
```

## Troubleshooting

### 404 on Index Page

If you get a 404 error when accessing Decypharr:

1. Ensure `url_base` in `config.yaml` matches your Nginx `location` path
2. Make sure `proxy_pass` includes the full path (e.g., `http://decypharr/decypharr/` not `http://decypharr/`)

### Authentication Issues

If you're having trouble with authentication behind a reverse proxy:

1. Decypharr uses cookies with `Path: "/"` which should work for any subfolder
2. Make sure your proxy is passing the required headers (check `proxy.conf`)
3. Check that `X-Forwarded-Proto` is set correctly for HTTPS

### API Access Denied

If Sonarr/Radarr can't connect to Decypharr:

1. Ensure the `/api/` location has `auth_basic off` or `auth_request off`
2. Verify the API token is correctly configured in both Decypharr and your *arr apps
3. Check Nginx error logs: `tail -f /var/log/nginx/error.log`
