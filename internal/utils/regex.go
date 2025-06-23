package utils

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	videoMatch  = "(?i)(\\.)(webm|m4v|3gp|nsv|ty|strm|rm|rmvb|m3u|ifo|mov|qt|divx|xvid|bivx|nrg|pva|wmv|asf|asx|ogm|ogv|m2v|avi|bin|dat|dvr-ms|mpg|mpeg|mp4|avc|vp3|svq3|nuv|viv|dv|fli|flv|wpl|img|iso|vob|mkv|mk3d|ts|wtv|m2ts)$"
	musicMatch  = "(?i)(\\.)(mp2|mp3|m4a|m4b|m4p|ogg|oga|opus|wma|wav|wv|flac|ape|aif|aiff|aifc)$"
	sampleMatch = `(?i)(^|[\s/\\])(sample|trailer|thumb|special|extras?)s?[-/]|(\((sample|trailer|thumb|special|extras?)s?\))|(-\s*(sample|trailer|thumb|special|extras?)s?)`
)

var (
	mediaRegex  = regexp.MustCompile(videoMatch + "|" + musicMatch)
	sampleRegex = regexp.MustCompile(sampleMatch)
)

func RegexMatch(re *regexp.Regexp, value string) bool {
	return re.MatchString(value)
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

func RemoveExtension(value string) string {
	if loc := mediaRegex.FindStringIndex(value); loc != nil {
		return value[:loc[0]]
	}
	return value
}

func IsMediaFile(path string) bool {
	return RegexMatch(mediaRegex, path)
}

func IsSampleFile(path string) bool {
	if strings.HasSuffix(strings.ToLower(path), "sample.mkv") {
		return true
	}
	return RegexMatch(sampleRegex, path)
}
