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
	Sonarr   Type = "sonarr"
	Radarr   Type = "radarr"
	Lidarr   Type = "lidarr"
	Readarr  Type = "readarr"
	Prowlarr Type = "prowlarr"
	Others   Type = "others"
)

// API roots an instance can serve. Servarr splits them by application family:
// Sonarr and Radarr speak v3, while Lidarr and Readarr (and their forks such as
// Chaptarr) speak v1.
const (
	APIv1 = "api/v1"
	APIv3 = "api/v3"
)

type Source string

const (
	SourceAuto   Source = "auto"
	SourceManual Source = "manual"
)

// Arr is one configured Arr instance. It carries no behaviour: every call to
// an instance goes through Service.
type Arr struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Token string `json:"token"`
	Type  Type   `json:"type"`
	// TypeInferred marks a Type guessed from the name and host, rather than
	// declared in the configuration or confirmed by a probe. A guessed type is
	// verified once and is never written back to the configuration.
	TypeInferred     bool   `json:"-"`
	APIVersion       string `json:"api_version,omitempty"`
	SkipRepair       bool   `json:"skip_repair"`
	DownloadUncached *bool  `json:"download_uncached"`
	SelectedDebrid   string `json:"selected_debrid,omitempty"`
	Source           Source `json:"source,omitempty"`
}

// APIRoot is the API base path an instance serves. An explicit APIVersion wins;
// otherwise the path follows the application family, and an unknown application
// keeps the historical v3 default.
func (a Arr) APIRoot() string {
	switch strings.ToLower(strings.TrimSpace(a.APIVersion)) {
	case "v1", "1", "api/v1":
		return APIv1
	case "v3", "3", "api/v3":
		return APIv3
	}
	switch a.Type {
	case Lidarr, Readarr:
		return APIv1
	default:
		return APIv3
	}
}

// AlternateAPIRoot returns the other API root, used to detect an instance whose
// declared version does not match what it actually serves.
func (a Arr) AlternateAPIRoot() string {
	if a.APIRoot() == APIv1 {
		return APIv3
	}
	return APIv1
}

// HasQueue reports whether the application exposes the Servarr download queue.
// Prowlarr is an indexer manager only and has no queue endpoint.
func (a Arr) HasQueue() bool {
	return a.Type != Prowlarr
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
	// A type the configuration declares is authoritative. A type guessed from
	// the name and host is not: it only addresses the instance until the first
	// probe confirms or corrects it.
	kind := inferType(c.Host, c.Name)
	inferred := true
	if declared := Type(strings.ToLower(strings.TrimSpace(c.Type))); declared != "" {
		kind = declared
		inferred = false
	}
	return Arr{
		Name:             c.Name,
		Host:             c.Host,
		Token:            strings.TrimSpace(c.Token),
		Type:             kind,
		TypeInferred:     inferred,
		APIVersion:       strings.TrimSpace(c.APIVersion),
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
		Type:             a.persistedType(),
		APIVersion:       a.APIVersion,
		SkipRepair:       a.SkipRepair,
		DownloadUncached: a.DownloadUncached,
		SelectedDebrid:   a.SelectedDebrid,
		Source:           string(a.Source),
	}
}

// persistedType is the type to write to the configuration: one the user
// declared, or a probe confirmed. A guessed type stays a runtime fact, so a
// wrong guess cannot become permanent.
func (a Arr) persistedType() string {
	if a.TypeInferred || a.Type == "" || a.Type == Others {
		return ""
	}
	return string(a.Type)
}

// inferType guesses from the name and host. Probe resolves it for certain.
func inferType(host, name string) Type {
	host = strings.ToLower(host)
	name = strings.ToLower(name)
	switch {
	case strings.Contains(host, "sonarr") || strings.Contains(name, "sonarr"):
		return Sonarr
	case strings.Contains(host, "radarr") || strings.Contains(name, "radarr"):
		return Radarr
	case strings.Contains(host, "lidarr") || strings.Contains(name, "lidarr"):
		return Lidarr
	// Chaptarr is a Readarr fork for books: same v1 API, different app name.
	case strings.Contains(host, "chaptarr") || strings.Contains(name, "chaptarr"):
		return Readarr
	case strings.Contains(host, "readarr") || strings.Contains(name, "readarr"):
		return Readarr
	case strings.Contains(host, "prowlarr") || strings.Contains(name, "prowlarr"):
		return Prowlarr
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
	case "chaptarr":
		return Readarr
	case "prowlarr":
		return Prowlarr
	default:
		return Others
	}
}
