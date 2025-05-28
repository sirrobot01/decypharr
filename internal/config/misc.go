package config

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (c *Config) IsAllowedFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	// Remove the leading dot
	ext = ext[1:]

	for _, allowed := range c.AllowedExt {
		if ext == allowed {
			return true
		}
	}
	return false
}

func getDefaultExtensions() []string {
	videoExts := strings.Split("webm,m4v,3gp,nsv,ty,strm,rm,rmvb,m3u,ifo,mov,qt,divx,xvid,bivx,nrg,pva,wmv,asf,asx,ogm,ogv,m2v,avi,bin,dat,dvr-ms,mpg,mpeg,mp4,avc,vp3,svq3,nuv,viv,dv,fli,flv,wpl,img,iso,vob,mkv,mk3d,ts,wtv,m2ts'", ",")
	musicExts := strings.Split("MP3,WAV,FLAC,OGG,WMA,AIFF,ALAC,M4A,APE,AC3,DTS,M4P,MID,MIDI,MKA,MP2,MPA,RA,VOC,WV,AMR", ",")

	// Combine both slices
	allExts := append(videoExts, musicExts...)

	// Convert to lowercase
	for i, ext := range allExts {
		allExts[i] = strings.ToLower(ext)
	}

	// Remove duplicates
	seen := make(map[string]struct{})
	var unique []string

	for _, ext := range allExts {
		if _, ok := seen[ext]; !ok {
			seen[ext] = struct{}{}
			unique = append(unique, ext)
		}
	}

	sort.Strings(unique)
	return unique
}

func ParseSize(sizeStr string) (int64, error) {
	sizeStr = strings.ToUpper(strings.TrimSpace(sizeStr))

	// Absolute size-based cache
	multiplier := 1.0
	if strings.HasSuffix(sizeStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	} else if strings.HasSuffix(sizeStr, "MB") {
		multiplier = 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	} else if strings.HasSuffix(sizeStr, "KB") {
		multiplier = 1024
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	}

	size, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		return 0, err
	}

	return int64(size * multiplier), nil
}
