package arr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
)

type Type string

const (
	Sonarr  Type = "sonarr"
	Radarr  Type = "radarr"
	Lidarr  Type = "lidarr"
	Readarr Type = "readarr"
	Others  Type = "others"
)

type Source string

const (
	SourceAuto   Source = "auto"
	SourceManual Source = "manual"
)

// Arr is one configured Arr instance. It carries no behaviour: every call to
// an instance goes through Service.
type Arr struct {
	Name             string `json:"name"`
	Host             string `json:"host"`
	Token            string `json:"token"`
	Type             Type   `json:"type"`
	SkipRepair       bool   `json:"skip_repair"`
	DownloadUncached *bool  `json:"download_uncached"`
	SelectedDebrid   string `json:"selected_debrid,omitempty"`
	Source           Source `json:"source,omitempty"`
}

// Reachable reports whether the instance carries enough to be called.
func (a Arr) Reachable() bool {
	return a.Name != "" && a.Host != "" && a.Token != ""
}

const fingerprintVersion = "v1"

// Fingerprint identifies the configured endpoint without storing credentials.
// It is empty when the host is not a usable absolute URL.
func (a Arr) Fingerprint() string {
	if a.Type == "" {
		return ""
	}
	host, err := canonicalHost(a.Host)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256([]byte(fingerprintVersion + "\x00" + string(a.Type) + "\x00" + host))
	return fingerprintVersion + ":" + hex.EncodeToString(digest[:])
}

func canonicalHost(rawHost string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawHost))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("Arr host must be an absolute URL")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || hostname == "" {
		return "", errors.New("Arr host must use HTTP or HTTPS")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	switch {
	case port != "":
		parsed.Host = net.JoinHostPort(hostname, port)
	case strings.Contains(hostname, ":"):
		parsed.Host = "[" + hostname + "]"
	default:
		parsed.Host = hostname
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawPath = ""
	parsed.Path = path.Clean(parsed.Path)
	if parsed.Path == "." || parsed.Path == "/" {
		parsed.Path = ""
	}
	return parsed.String(), nil
}

func fromConfig(c config.Arr) Arr {
	return Arr{
		Name:             c.Name,
		Host:             c.Host,
		Token:            strings.TrimSpace(c.Token),
		Type:             inferType(c.Host, c.Name),
		SkipRepair:       c.SkipRepair,
		DownloadUncached: c.DownloadUncached,
		SelectedDebrid:   c.SelectedDebrid,
		Source:           Source(c.Source),
	}
}

func (a Arr) toConfig() config.Arr {
	return config.Arr{
		Name:             a.Name,
		Host:             a.Host,
		Token:            a.Token,
		SkipRepair:       a.SkipRepair,
		DownloadUncached: a.DownloadUncached,
		SelectedDebrid:   a.SelectedDebrid,
		Source:           string(a.Source),
	}
}

// inferType guesses from the name and host. Probe resolves it for certain.
func inferType(host, name string) Type {
	switch {
	case strings.Contains(host, "sonarr") || strings.Contains(name, "sonarr"):
		return Sonarr
	case strings.Contains(host, "radarr") || strings.Contains(name, "radarr"):
		return Radarr
	case strings.Contains(host, "lidarr") || strings.Contains(name, "lidarr"):
		return Lidarr
	case strings.Contains(host, "readarr") || strings.Contains(name, "readarr"):
		return Readarr
	default:
		return Others
	}
}

func typeFromAppName(appName string) Type {
	switch strings.ToLower(strings.TrimSpace(appName)) {
	case "sonarr":
		return Sonarr
	case "radarr":
		return Radarr
	case "lidarr":
		return Lidarr
	case "readarr":
		return Readarr
	default:
		return Others
	}
}
