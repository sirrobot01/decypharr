package config

// SMB configures the read-only SMB2/SMB3 server. Experimental: facetfs's SMB
// package has passed Linux client acceptance only, and it does not implement
// SMB encryption — sessions are signed, not private, so serve trusted
// networks only. Like NFS, SMB is a thin protocol adapter over the catalog,
// and it reads through the same shared cache (see ShareCache).
type SMB struct {
	Enabled     bool   `json:"enabled,omitempty"`
	BindAddress string `json:"bind_address,omitempty"`
	// Port is the listen port. SMB clients connect to 445; the unprivileged
	// default suits a container whose host maps 445 onto it.
	Port      uint16 `json:"port,omitempty"`
	ShareName string `json:"share_name,omitempty"`
	// Username/Password authenticate every session; the server grants no
	// anonymous access. The NTLM domain a client sends is ignored.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// RequireSigning refuses clients that will not sign. Off by default:
	// signing is still used whenever the client asks for it, and mandatory
	// signing costs real CPU at streaming bitrates.
	RequireSigning  bool     `json:"require_signing,omitempty"`
	AllowedNetworks []string `json:"allowed_networks,omitempty"`
}

func (s SMB) IsZero() bool {
	return !s.Enabled && s.BindAddress == "" && s.Port == 0 && s.ShareName == "" &&
		s.Username == "" && s.Password == "" && !s.RequireSigning && len(s.AllowedNetworks) == 0
}

func (c *Config) setSMBDefaults() {
	if !c.SMB.Enabled {
		return
	}
	if c.SMB.BindAddress == "" {
		c.SMB.BindAddress = c.BindAddress
	}
	if c.SMB.BindAddress == "" {
		c.SMB.BindAddress = "0.0.0.0"
	}
	if c.SMB.Port == 0 {
		c.SMB.Port = DefaultSMBPort
	}
	if c.SMB.ShareName == "" {
		c.SMB.ShareName = "decypharr"
	}
	if len(c.SMB.AllowedNetworks) == 0 {
		c.SMB.AllowedNetworks = []string{
			"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"::1/128", "fc00::/7", "fe80::/10",
		}
	}
}
