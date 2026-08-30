package config

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	StrmDeliveryProxy    = "proxy"
	StrmDeliveryRedirect = "redirect"

	DefaultStrmSidecarMaxSize = "32MB"
)

// Strm configures the .strm export: when enabled, every completed entry gets
// a folder of identity-URL .strm files under Path, kept in sync by the
// reconciler and served by the /stream endpoint.
type Strm struct {
	Enabled bool `json:"enabled,omitempty"`
	// Path is the root of the .strm tree, one folder per entry. Decypharr
	// owns this tree completely: files are written, rewritten, and removed
	// to match the library.
	Path string `json:"path,omitempty"`
	// Secret signs stream URLs (HMAC-SHA256). Generated on first load if
	// empty. Rotating it invalidates every written .strm; regenerate after.
	Secret string `json:"secret,omitempty"`
	// DeliveryMode is how /stream serves bytes: "proxy" (default) streams
	// through decypharr, "redirect" 302s to the provider link when possible.
	DeliveryMode string `json:"delivery_mode,omitempty"`
	// KeepMediaExtension writes Movie.mkv.strm instead of Movie.strm.
	KeepMediaExtension bool `json:"keep_media_extension,omitempty"`
	// DownloadSidecars downloads small subtitle/nfo files next to the .strm
	// (a .strm cannot deliver them). Defaults to true.
	DownloadSidecars *bool `json:"download_sidecars,omitempty"`
	// SidecarMaxSize caps which files count as sidecars. Defaults to 32MB.
	SidecarMaxSize string `json:"sidecar_max_size,omitempty"`
}

func (s Strm) IsZero() bool {
	return !s.Enabled && s.Path == "" && s.Secret == "" && s.DeliveryMode == "" &&
		!s.KeepMediaExtension && s.DownloadSidecars == nil && s.SidecarMaxSize == ""
}

// Active reports whether the .strm export should run.
func (s Strm) Active() bool {
	return s.Enabled && s.Path != ""
}

func (s Strm) SidecarsEnabled() bool {
	return s.DownloadSidecars == nil || *s.DownloadSidecars
}

func (s Strm) SidecarMaxBytes() int64 {
	size, err := ParseSize(s.SidecarMaxSize)
	if err != nil || size <= 0 {
		size, _ = ParseSize(DefaultStrmSidecarMaxSize)
	}
	return size
}

func (c *Config) setStrmDefaults() {
	if c.Strm.Secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		c.Strm.Secret = hex.EncodeToString(b)
	}
	if c.Strm.DeliveryMode == "" {
		c.Strm.DeliveryMode = StrmDeliveryProxy
	}
	if c.Strm.SidecarMaxSize == "" {
		c.Strm.SidecarMaxSize = DefaultStrmSidecarMaxSize
	}
}
