package utils

import (
	"path/filepath"
	"strings"
)

// mediaExtensions is a set of known media file extensions (lowercase, without dot)
var mediaExtensions = map[string]struct{}{
	// Video
	"webm": {}, "m4v": {}, "3gp": {}, "nsv": {}, "ty": {}, "strm": {},
	"rm": {}, "rmvb": {}, "m3u": {}, "ifo": {}, "mov": {}, "qt": {},
	"divx": {}, "xvid": {}, "bivx": {}, "nrg": {}, "pva": {}, "wmv": {},
	"asf": {}, "asx": {}, "ogm": {}, "ogv": {}, "m2v": {}, "avi": {},
	"bin": {}, "dat": {}, "dvr-ms": {}, "mpg": {}, "mpeg": {}, "mp4": {},
	"avc": {}, "vp3": {}, "svq3": {}, "nuv": {}, "viv": {}, "dv": {},
	"fli": {}, "flv": {}, "wpl": {}, "vob": {}, "mkv": {}, "mk3d": {},
	"ts": {}, "wtv": {}, "m2ts": {},
	// Audio
	"mp2": {}, "mp3": {}, "m4a": {}, "m4b": {}, "m4p": {}, "ogg": {},
	"oga": {}, "opus": {}, "wma": {}, "wav": {}, "wv": {}, "flac": {},
	"ape": {}, "aif": {}, "aiff": {}, "aifc": {},
}

func RemoveInvalidChars(value string) string {
	return strings.Map(func(r rune) rune {
		if r == filepath.Separator || r == ':' {
			return r
		}
		if filepath.IsAbs(string(r)) {
			return r
		}
		if strings.ContainsRune(filepath.VolumeName("C:"+string(r)), r) {
			return r
		}
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, value)
}

func SafeFolderName(value, fallback string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." {
		return fallback
	}
	if isReservedWindowsName(value) {
		return fallback
	}
	value = truncateFolderName(value, 255)
	value = strings.Trim(value, " .")
	if value == "" || isReservedWindowsName(value) {
		return fallback
	}
	return value
}

func truncateFolderName(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	var builder strings.Builder
	for _, r := range value {
		if builder.Len()+len(string(r)) > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func isReservedWindowsName(value string) bool {
	name := strings.ToUpper(value)
	if base, _, ok := strings.Cut(name, "."); ok {
		name = base
	}
	switch name {
	case "CON", "PRN", "AUX", "NUL":
		return true
	case "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9":
		return true
	case "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func RemoveExtension(value string) string {
	ext := filepath.Ext(value)
	if ext == "" {
		return value
	}
	// Remove the leading dot and lowercase for lookup
	extLower := strings.ToLower(ext[1:])
	if _, ok := mediaExtensions[extLower]; ok {
		name := value[:len(value)-len(ext)]
		if name != "" && name != "." {
			return name
		}
	}
	return value
}

func IsMediaFile(path string) bool {
	ext := filepath.Ext(path)
	if ext == "" {
		return false
	}
	extLower := strings.ToLower(ext[1:])
	_, ok := mediaExtensions[extLower]
	return ok
}
