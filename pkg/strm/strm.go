// Package strm builds and verifies the identity-based stream URLs written
// into .strm files:
//
//	{base}/stream/{infohash}/{fileID}/{displayName}?s={sig}
//
// The infohash and per-file ID survive renames, folder-naming changes,
// repairs, and provider migrations; the display name is cosmetic and ignored
// by the resolver.
package strm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// Sign returns the URL signature for a file: hex(hmac-sha256(secret, infohash+"/"+fileID)).
func Sign(secret, infohash, fileID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(infohash + "/" + fileID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is the valid signature for infohash/fileID.
func Verify(secret, infohash, fileID, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, infohash, fileID)), []byte(sig))
}

// BaseURL returns the externally reachable server base, read live from
// config: AppURL when set, otherwise the bind address and port. URLBase is
// appended unless already present.
func BaseURL(cfg *config.Config) string {
	base := strings.TrimSuffix(cfg.AppURL, "/")
	if base == "" {
		host := cfg.BindAddress
		if host == "" || host == "0.0.0.0" {
			host = "localhost"
		}
		base = fmt.Sprintf("http://%s:%s", host, cfg.Port)
	}
	if ub := strings.Trim(cfg.URLBase, "/"); ub != "" && !strings.HasSuffix(base, "/"+ub) {
		base += "/" + ub
	}
	return base
}

// FileURL builds the canonical, signed stream URL for one file.
func FileURL(base, secret, infohash, fileID, displayName string) string {
	return fmt.Sprintf("%s/stream/%s/%s/%s?s=%s",
		base, infohash, fileID, url.PathEscape(displayName), Sign(secret, infohash, fileID))
}

// ParseURL extracts the identity from a canonical stream URL. It returns
// ok=false for anything else (foreign .strm files, legacy WebDAV URLs), keyed
// on the /stream/{infohash}/{fileID}/{name} path shape with hex identifiers.
func ParseURL(raw string) (infohash, fileID string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+3 < len(segs); i++ {
		if segs[i] == "stream" && isHex(segs[i+1]) && isHex(segs[i+2]) {
			return segs[i+1], segs[i+2], true
		}
	}
	return "", "", false
}

func isHex(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// FileName maps a media file name to its .strm name. By default the media
// extension is replaced (Movie.mkv -> Movie.strm); keepExt appends instead.
func FileName(mediaName string, keepExt bool) string {
	if keepExt {
		return mediaName + ".strm"
	}
	return utils.RemoveExtension(mediaName) + ".strm"
}

// sidecarExtensions are small companion files players read from disk next to
// the .strm (a .strm cannot deliver them).
var sidecarExtensions = map[string]struct{}{
	"srt": {}, "ass": {}, "ssa": {}, "sub": {}, "idx": {},
	"vtt": {}, "smi": {}, "sup": {}, "nfo": {},
}

// IsSidecar reports whether name has a sidecar extension.
func IsSidecar(name string) bool {
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	_, ok := sidecarExtensions[strings.ToLower(ext[1:])]
	return ok
}
