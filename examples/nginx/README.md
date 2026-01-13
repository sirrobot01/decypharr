# Nginx Reverse Proxy Configuration

Example Nginx configuration files for running Decypharr behind a reverse proxy.

## Files

- `subfolder.conf` - Run Decypharr at a subfolder (e.g., `/decypharr/`) - **recommended**
- `subdomain.conf` - Run Decypharr on its own subdomain (e.g., `decypharr.example.com`)

## Prerequisites

These configurations assume you have the standard Nginx snippets:

- `/etc/nginx/snippets/proxy.conf` - Proxy settings (included with Nginx)
- `/etc/nginx/snippets/ssl-params.conf` - SSL parameters (included with Nginx)

## Quick Start

1. Choose the appropriate config file for your setup
2. Copy it to `/etc/nginx/sites-available/decypharr.conf`
3. Update server names, SSL certificate paths, and upstream address
4. Enable: `ln -s /etc/nginx/sites-available/decypharr.conf /etc/nginx/sites-enabled/`
5. Test: `nginx -t`
6. Reload: `systemctl reload nginx`

## Decypharr Configuration

### Subfolder Setup

```yaml
# config.yaml
url_base: "/decypharr/"
```

### Subdomain Setup

```yaml
# config.yaml
url_base: "/"  # default, can be omitted
```

## Authentication

Each config file includes commented examples for:

- **External Auth** (Authelia, Authentik) - Uses `auth_request`
- **Basic Auth** - Uses `auth_basic` with `.htpasswd`

Both bypass authentication for `/api/` routes so Sonarr/Radarr can communicate with Decypharr using its built-in API token.

Create `.htpasswd` for basic auth:

```bash
htpasswd -c /etc/nginx/.htpasswd username
```

## Documentation

See the full [Reverse Proxy Guide](https://sirrobot01.github.io/decypharr/guides/reverse-proxy/) for detailed instructions.
