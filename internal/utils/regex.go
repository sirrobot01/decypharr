package utils

import (
	"path/filepath"
	"strings"
)

// videoExtensions is a set of known video file extensions (lowercase, without dot)
var videoExtensions = map[string]struct{}{
	"webm": {}, "m4v": {}, "3gp": {}, "nsv": {}, "ty": {},
	"rm": {}, "rmvb": {}, "ifo": {}, "mov": {}, "qt": {},
	"divx": {}, "xvid": {}, "bivx": {}, "nrg": {}, "pva": {}, "wmv": {},
	"asf": {}, "asx": {}, "ogm": {}, "ogv": {}, "m2v": {}, "avi": {},
	"bin": {}, "dat": {}, "dvr-ms": {}, "mpg": {}, "mpeg": {}, "mp4": {},
	"avc": {}, "vp3": {}, "svq3": {}, "nuv": {}, "viv": {}, "dv": {},
	"fli": {}, "flv": {}, "wpl": {}, "vob": {}, "mkv": {}, "mk3d": {},
	"ts": {}, "wtv": {}, "m2ts": {},
}

// mediaExtensions is a set of known media file extensions (lowercase, without dot)
var mediaExtensions = func() map[string]struct{} {
	m := map[string]struct{}{
		"strm": {}, "m3u": {},
		// Audio
		"mp2": {}, "mp3": {}, "m4a": {}, "m4b": {}, "m4p": {}, "ogg": {},
		"oga": {}, "opus": {}, "wma": {}, "wav": {}, "wv": {}, "flac": {},
		"ape": {}, "aif": {}, "aiff": {}, "aifc": {},
	}
	for ext := range videoExtensions {
		m[ext] = struct{}{}
	}
	return m
}()

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

func IsVideoFile(path string) bool {
	ext := filepath.Ext(path)
	if ext == "" {
		return false
	}
	_, ok := videoExtensions[strings.ToLower(ext[1:])]
	return ok
}
