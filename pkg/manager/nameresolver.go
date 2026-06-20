package manager

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// resolveEntryName converts a raw debrid torrent name into a clean,
// filesystem-safe directory name (≤ 255 bytes, Linux NAME_MAX).
//
// When prefer_ascii_name is true (the default), the raw name is parsed to
// extract the best ASCII title, year, season and episode, and a compact name
// such as "Percy Jackson and the Olympians (2025) S02E01-E04" is returned.
// This handles mixed Cyrillic, CJK, Arabic, and other non-Western release
// names that embed an English title alongside the native-script title.
//
// When prefer_ascii_name is false, the raw name is returned unchanged except
// for a byte-safe UTF-8 truncation to 255 bytes — useful when the library is
// intentionally non-Western and the native-script name should be preserved.
func (m *Manager) resolveEntryName(raw string) string {
	cfg := config.Get()
	if cfg.PreferASCIIName != nil && !*cfg.PreferASCIIName {
		return utils.TruncateName(utils.RemoveExtension(raw), 255)
	}

	parsed := utils.ParseTorrentName(raw)
	clean := utils.BuildCleanName(parsed, "")
	if clean == "" {
		return utils.TruncateName(utils.RemoveExtension(raw), 255)
	}

	m.logger.Debug().
		Str("raw", raw).
		Str("clean", clean).
		Msg("Resolved entry name")
	return clean
}
