package manager

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Multi-season detection patterns
var (
	// Pre-compiled patterns for multi-season replacement
	multiSeasonReplacements = []multiSeasonPattern{
		// S01-08 -> S01 (or whatever target season)
		{regexp.MustCompile(`(?i)S(\d{1,2})-\d{1,2}`), "S%02d"},

		// S01-S08 -> S01
		{regexp.MustCompile(`(?i)S(\d{1,2})-S\d{1,2}`), "S%02d"},

		// Season 1-8 -> Season 1
		{regexp.MustCompile(`(?i)Season\.?\s*\d{1,2}-\d{1,2}`), "Season %02d"},

		// Seasons 1-8 -> Season 1
		{regexp.MustCompile(`(?i)Seasons\.?\s*\d{1,2}-\d{1,2}`), "Season %02d"},

		// Complete Series -> Season X
		{regexp.MustCompile(`(?i)Complete\.?Series`), "Season %02d"},

		// All Seasons -> Season X
		{regexp.MustCompile(`(?i)All\.?Seasons?`), "Season %02d"},
	}

	// Also pre-compile other patterns
	seasonPattern     = regexp.MustCompile(`(?i)(?:season\.?\s*|s)(\d{1,2})`)
	qualityIndicators = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|BluRay|WEB-DL|HDTV|x264|x265|HEVC)`)

	multiSeasonIndicators = []*regexp.Regexp{
		regexp.MustCompile(`(?i)complete\.?series`),
		regexp.MustCompile(`(?i)all\.?seasons?`),
		regexp.MustCompile(`(?i)season\.?\s*\d+\s*-\s*\d+`),
		regexp.MustCompile(`(?i)s\d+\s*-\s*s?\d+`),
		regexp.MustCompile(`(?i)seasons?\s*\d+\s*-\s*\d+`),
	}
)

type multiSeasonPattern struct {
	pattern     *regexp.Regexp
	replacement string
}

// SeasonInfo represents information about a season in a multi-season torrent
type SeasonInfo struct {
	SeasonNumber int
	Files        []*storage.File
	InfoHash     string
	Name         string
}

// convertToMultiSeason converts a normal torrent to a multi-season torrents
func convertToMultiSeason(torrent *storage.Entry, seasons []SeasonInfo) []*storage.Entry {
	seasonResults := make([]*storage.Entry, 0, len(seasons))
	for _, seasonInfo := range seasons {
		// Filter files to only include this season's files
		seasonFiles := make(map[string]*storage.File)
		size := int64(0)
		for _, file := range seasonInfo.Files {
			seasonFiles[file.Name] = &storage.File{
				Name:      file.Name,
				Size:      file.Size,
				ByteRange: file.ByteRange,
				Deleted:   file.Deleted,
				InfoHash:  file.InfoHash,
				AddedOn:   torrent.AddedOn,
			}
			size += file.Size
		}

		// Create a season-specific managed torrent
		seasonTorrent := &storage.Entry{
			Protocol:         torrent.Protocol,
			InfoHash:         seasonInfo.InfoHash,
			Name:             seasonInfo.Name,
			OriginalFilename: torrent.OriginalFilename,
			Size:             size,
			Bytes:            size,
			Magnet:           torrent.Magnet,
			Category:         torrent.Category,
			SavePath:         torrent.SavePath,
			Status:           debridTypes.TorrentStatusDownloading,
			ActiveProvider:   torrent.ActiveProvider,
			Action:           torrent.Action,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
			AddedOn:          time.Now(),
			Providers:        make(map[string]*storage.ProviderEntry),
			Files:            seasonFiles,
		}

		// Copy placement
		for debridName, placement := range torrent.Providers {
			seasonTorrent.Providers[debridName] = placement
		}
		seasonResults = append(seasonResults, seasonTorrent)
	}
	return seasonResults
}

func replaceMultiSeasonPattern(name string, targetSeason int) string {
	result := name

	// Apply each pre-compiled pattern replacement
	for _, msp := range multiSeasonReplacements {
		if msp.pattern.MatchString(result) {
			replacement := fmt.Sprintf(msp.replacement, targetSeason)
			result = msp.pattern.ReplaceAllString(result, replacement)
			return result
		}
	}

	// If no multi-season pattern found, try to insert season info intelligently
	return insertSeasonIntoName(result, targetSeason)
}

func insertSeasonIntoName(name string, seasonNum int) string {
	// Check if season info already exists
	if seasonPattern.MatchString(name) {
		return name // Already has season info, keep as is
	}

	// Try to find a good insertion point (before quality indicators)
	if loc := qualityIndicators.FindStringIndex(name); loc != nil {
		// Insert season before quality info
		before := strings.TrimSpace(name[:loc[0]])
		after := name[loc[0]:]
		return fmt.Sprintf("%s S%02d %s", before, seasonNum, after)
	}

	// If no quality indicators found, append at the end
	return fmt.Sprintf("%s S%02d", name, seasonNum)
}

func findAllSeasons(files []*storage.File) map[int]bool {
	seasons := make(map[int]bool)

	for _, file := range files {
		// Check filename first
		if season := extractSeason(file.Name); season > 0 {
			seasons[season] = true
			continue
		}

		// Check full path
		if season := extractSeason(file.Path); season > 0 {
			seasons[season] = true
		}
	}

	return seasons
}

// extractSeason pulls season number from a string
func extractSeason(text string) int {
	matches := seasonPattern.FindStringSubmatch(text)
	if len(matches) > 1 {
		if num, err := strconv.Atoi(matches[1]); err == nil && num > 0 && num < 100 {
			return num
		}
	}
	return 0
}

func hasMultiSeasonIndicators(torrentName string) bool {
	for _, pattern := range multiSeasonIndicators {
		if pattern.MatchString(torrentName) {
			return true
		}
	}
	return false
}

// groupFilesBySeason puts files into season buckets
func groupFilesBySeason(files []*storage.File, knownSeasons map[int]bool) map[int][]*storage.File {
	groups := make(map[int][]*storage.File)

	// Initialize groups
	for season := range knownSeasons {
		groups[season] = []*storage.File{}
	}

	for _, file := range files {
		// Try to find season from filename or path
		season := extractSeason(file.Name)
		if season == 0 {
			season = extractSeason(file.Path)
		}

		// If we found a season and it's known, add the file
		if season > 0 && knownSeasons[season] {
			groups[season] = append(groups[season], file)
		} else {
			// If no season found, try path-based inference
			inferredSeason := inferSeasonFromPath(file.Path, knownSeasons)
			if inferredSeason > 0 {
				groups[inferredSeason] = append(groups[inferredSeason], file)
			} else if len(knownSeasons) == 1 {
				// If only one season exists, default to it
				for season := range knownSeasons {
					groups[season] = append(groups[season], file)
				}
			}
		}
	}

	return groups
}

func inferSeasonFromPath(path string, knownSeasons map[int]bool) int {
	pathParts := strings.Split(path, "/")

	for _, part := range pathParts {
		if season := extractSeason(part); season > 0 && knownSeasons[season] {
			return season
		}
	}

	return 0
}

// Helper to get sorted season list for logging
func getSortedSeasons(seasons map[int]bool) []int {
	result := make([]int, 0, len(seasons))
	for season := range seasons {
		result = append(result, season)
	}
	sort.Ints(result)
	return result
}

// generateSeasonHash creates a unique hash for a season based on original hash
func generateSeasonHash(originalHash string, seasonNumber int) string {
	source := fmt.Sprintf("%s-%d", originalHash, seasonNumber)
	hash := md5.Sum([]byte(source))
	return fmt.Sprintf("%x", hash)
}
