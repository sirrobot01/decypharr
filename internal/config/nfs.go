package config

// NFS configures the read-only NFSv4 server. NFS is a thin protocol adapter
// over the catalog (like WebDAV); reads go through the shared on-disk cache,
// configured once in ShareCache because SMB uses the same one. NFSv4 is a
// single listener: no mount service, portmapper, or advertised ports.
type NFS struct {
	Enabled         bool     `json:"enabled,omitempty"`
	BindAddress     string   `json:"bind_address,omitempty"`
	Port            uint16   `json:"port,omitempty"`
	AllowedNetworks []string `json:"allowed_networks,omitempty"`
}

func (n NFS) IsZero() bool {
	return !n.Enabled && n.BindAddress == "" && n.Port == 0 && len(n.AllowedNetworks) == 0
}

func (c *Config) setNFSDefaults() {
	if !c.NFS.Enabled {
		return
	}
	// NFS is its own listener, so it keeps a separate bind address — NFSv4
	// carries only AUTH_SYS identities, and confining it to one interface is
	// stricter than the application-level AllowedNetworks filter. Inherit the
	// server's bind address when set so the common case is configured in one
	// place; fall back to all interfaces only when neither is specified.
	if c.NFS.BindAddress == "" {
		c.NFS.BindAddress = c.BindAddress
	}
	if c.NFS.BindAddress == "" {
		c.NFS.BindAddress = "0.0.0.0"
	}
	if c.NFS.Port == 0 {
		c.NFS.Port = DefaultNFSPort
	}
	if len(c.NFS.AllowedNetworks) == 0 {
		c.NFS.AllowedNetworks = []string{
			"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"::1/128", "fc00::/7", "fe80::/10",
		}
	}
}
