package utils

import (
	"fmt"
	"regexp"
	"strings"
)

// ParsedName holds structured information extracted from a raw torrent/release name.
type ParsedName struct {
	Title   string // Best English title candidate (may be empty)
	Year    string // 4-digit year, e.g. "2025" (empty if not found)
	Season  int    // Season number, 0 if unknown
	EpStart int    // First episode number, 0 if unknown
	EpEnd   int    // Last episode number when a range is present, 0 otherwise
	IsTV    bool   // True if any season/episode info was found
}

var (
	// S01E01, S01E01-E04, S01E01E04, S01E01-04
	reStandardSE = regexp.MustCompile(`(?i)[Ss](\d{1,2})[Ee](\d{1,3})(?:[Ee-]?(\d{1,3}))?`)
	// "Season 2" / "Сезон 2"
	reSeasonWord = regexp.MustCompile(`(?i)(?:season|сезон)\s*(\d{1,2})`)
	// "Episodes 1-4" / "Серии 1-4 из" / "Серии 1-4"
	reEpRange = regexp.MustCompile(`(?i)(?:episodes?|серии?)\s*(\d{1,3})[-–—]\s*(\d{1,3})`)
	// "Episode 1" / "Серия 1"
	reEpSingle = regexp.MustCompile(`(?i)(?:episode|серия)\s*(\d{1,3})`)
	// 4-digit year surrounded by delimiters
	reYear = regexp.MustCompile(`(?:^|[\s\[\(,])(\d{4})(?:[\s\]\),\.]|$)`)
	// Longest capitalised ASCII word-run (title candidate)
	reAsciiTitle = regexp.MustCompile(`[A-Z][A-Za-z0-9 ':\-\.!,&]{1,}`)
	// Fully dot-separated first token ("Breaking.Bad.S05E10")
	reDotted = regexp.MustCompile(`^[A-Za-z0-9]+(?:\.[A-Za-z0-9]+){2,}`)
	// Technical tag keywords that mark where the title region ends
	reTechTag = regexp.MustCompile(`(?i)\b(?:HEVC|H\.?264|H\.?265|x264|x265|AVC|HVEC|WEB[-\.]?DL|WEBRip|BluRay|BDRip|HDRip|HDTV|DVDRip|CAM|TS|4[Kk]|1080[pi]|720[pi]|480[pi]|2160[pi]|HDR|SDR|Dolby|DTS|AAC|AC3|MP3|FLAC|MVO|MKV|MP4|AVI|REMUX|REPACK|PROPER|EXTENDED|THEATRICAL|UNRATED|LIMITED|IMAX)\b`)
)

// ParseTorrentName extracts structured info from a raw torrent/release name.
// It handles mixed Cyrillic+English names, dotted names, and common tagging conventions.
func ParseTorrentName(raw string) ParsedName {
	p := ParsedName{}

	// Normalise dot-separated names ("Breaking.Bad.S05E10.720p") to spaces.
	working := raw
	firstWord := strings.Fields(raw)
	if len(firstWord) > 0 && reDotted.MatchString(firstWord[0]) {
		working = strings.ReplaceAll(raw, ".", " ")
	}

	// ── Season / Episode ──────────────────────────────────────────────────────
	if m := reStandardSE.FindStringSubmatch(working); m != nil {
		p.Season = nameParseAtoi(m[1])
		p.EpStart = nameParseAtoi(m[2])
		if m[3] != "" {
			ep2 := nameParseAtoi(m[3])
			if ep2 != p.EpStart {
				p.EpEnd = ep2
			}
		}
		p.IsTV = true
	} else {
		if m := reSeasonWord.FindStringSubmatch(working); m != nil {
			p.Season = nameParseAtoi(m[1])
			p.IsTV = true
		}
		if m := reEpRange.FindStringSubmatch(working); m != nil {
			p.EpStart = nameParseAtoi(m[1])
			p.EpEnd = nameParseAtoi(m[2])
		} else if m := reEpSingle.FindStringSubmatch(working); m != nil {
			p.EpStart = nameParseAtoi(m[1])
		}
	}

	// ── Year ─────────────────────────────────────────────────────────────────
	for _, m := range reYear.FindAllStringSubmatch(working, -1) {
		if y := nameParseAtoi(m[1]); y >= 1900 && y <= 2099 {
			p.Year = m[1]
			break
		}
	}

	// ── Title region ─────────────────────────────────────────────────────────
	// Cut at the first technical tag or bracket to avoid including codec/group info.
	titleRegion := working
	if loc := reTechTag.FindStringIndex(titleRegion); loc != nil && loc[0] > 5 {
		titleRegion = titleRegion[:loc[0]]
	}
	for _, ch := range []string{"[", "("} {
		if idx := strings.Index(titleRegion, ch); idx > 5 {
			if idx < len(titleRegion) {
				titleRegion = titleRegion[:idx]
			}
		}
	}
	// Cut at season/episode marker so it isn't folded into the title.
	if m := reStandardSE.FindStringIndex(titleRegion); m != nil {
		titleRegion = titleRegion[:m[0]]
	} else if m := reSeasonWord.FindStringIndex(titleRegion); m != nil {
		titleRegion = titleRegion[:m[0]]
	}

	// Pick the longest capitalised ASCII run from the title region.
	best := ""
	for _, r := range reAsciiTitle.FindAllString(titleRegion, -1) {
		r = nameTrimTitle(r)
		if len(r) > len(best) {
			best = r
		}
	}
	p.Title = best

	return p
}

// BuildCleanName constructs a short, meaningful, filesystem-safe name from a
// ParsedName. canonicalTitle (from TMDB/TVDB) overrides p.Title when non-empty.
// The result is guaranteed to be ≤ 255 bytes (Linux NAME_MAX).
func BuildCleanName(p ParsedName, canonicalTitle string) string {
	title := p.Title
	if canonicalTitle != "" {
		title = canonicalTitle
	}
	if title == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(title)

	if p.Year != "" {
		sb.WriteString(" (")
		sb.WriteString(p.Year)
		sb.WriteByte(')')
	}

	if p.Season > 0 {
		sb.WriteString(fmt.Sprintf(" S%02d", p.Season))
		if p.EpStart > 0 {
			sb.WriteString(fmt.Sprintf("E%02d", p.EpStart))
			if p.EpEnd > 0 && p.EpEnd != p.EpStart {
				sb.WriteString(fmt.Sprintf("-E%02d", p.EpEnd))
			}
		}
	}

	return TruncateName(sb.String(), 255)
}

// nameTrimTitle cleans leading/trailing punctuation and collapses spaces.
func nameTrimTitle(s string) string {
	s = strings.TrimRight(s, " -.,:/")
	s = strings.TrimLeft(s, " -.,:/")
	return strings.Join(strings.Fields(s), " ")
}

// nameParseAtoi converts a digit-only string to int without stdlib overhead.
func nameParseAtoi(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}
