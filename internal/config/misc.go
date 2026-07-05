package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrFileIsSample      = errors.New("file is sample")
	ErrFileExtNotAllowed = errors.New("file extension not allowed")
)

var (
	sampleMatch = `(?i)(^|[\s/\\])(sample|trailer|thumb|special|extras?)s?[-/]|(\((sample|trailer|thumb|special|extras?)s?\))|(-\s*(sample|trailer|thumb|special|extras?)s?)`
	sampleRegex = regexp.MustCompile(sampleMatch)
)

func isSample(path string) bool {
	filename := filepath.Base(path)
	if strings.HasSuffix(strings.ToLower(filename), "sample.mkv") {
		return true
	}
	return sampleRegex.MatchString(filename)
}

func (c *Config) IsFileAllowed(filename string, filesize int64) error {
	// Skip samples if configured
	if !c.AllowSamples && isSample(filename) {
		// Skip sample files
		return ErrFileIsSample
	}

	if !c.isNameAllowed(filename) {
		return ErrFileExtNotAllowed
	}

	// Check size constraints
	if !c.isSizeAllowed(filesize) {
		return fmt.Errorf("file size %d is not allowed. expected range: [%d - %d]", filesize, c.GetMinFileSize(), c.GetMaxFileSize())
	}
	return nil
}

func (c *Config) isSizeAllowed(size int64) bool {
	if size == 0 {
		return true // Maybe the debrid hasn't reported the size yet
	}
	if c.GetMinFileSize() > 0 && size < c.GetMinFileSize() {
		return false
	}
	if c.GetMaxFileSize() > 0 && size > c.GetMaxFileSize() {
		return false
	}
	return true
}
func (c *Config) isNameAllowed(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	// Remove the leading dot
	ext = ext[1:]

	return slices.Contains(c.AllowedExt, ext)
}

func getDefaultExtensions() []string {
	videoExts := strings.Split("webm,m4v,3gp,nsv,ty,strm,rm,rmvb,m3u,ifo,mov,qt,divx,xvid,bivx,nrg,pva,wmv,asf,asx,ogm,ogv,m2v,avi,bin,dat,dvr-ms,mpg,mpeg,mp4,avc,vp3,svq3,nuv,viv,dv,fli,flv,wpl,vob,mkv,mk3d,ts,wtv,m2ts", ",")
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

	// Absolute size-based cache. Order matters: two-letter units must be
	// checked before the bare "B" suffix. ParseFloat below means decimal
	// values (e.g. "2.2TB", "1.5GB") are supported for every unit.
	multiplier := 1.0
	switch {
	case strings.HasSuffix(sizeStr, "PB"):
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "PB")
	case strings.HasSuffix(sizeStr, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "TB")
	case strings.HasSuffix(sizeStr, "GB"):
		multiplier = 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	case strings.HasSuffix(sizeStr, "MB"):
		multiplier = 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	case strings.HasSuffix(sizeStr, "KB"):
		multiplier = 1024
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	case strings.HasSuffix(sizeStr, "B"):
		sizeStr = strings.TrimSuffix(sizeStr, "B")
	}

	size, err := strconv.ParseFloat(strings.TrimSpace(sizeStr), 64)
	if err != nil {
		return 0, err
	}

	return int64(size * multiplier), nil
}
