package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// extendedDurationRegex matches duration strings like "2d", "10d", "1w", "2w3d", "1w2d3h"
var extendedDurationRegex = regexp.MustCompile(`^(\d+w)?(\d+d)?(.*)$`)

// ParseDuration extends Go's time.ParseDuration to support:
//   - weeks (w): 1w = 7 days
//   - days (d): 1d = 24 hours
//
// Examples: "2d", "10d", "1w", "2w3d", "1w2d3h30m", "48h"
// Falls back to standard time.ParseDuration for unsupported formats.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	matches := extendedDurationRegex.FindStringSubmatch(s)
	if matches == nil {
		// No match, try standard parsing
		return time.ParseDuration(s)
	}

	var total time.Duration

	// Parse weeks
	if matches[1] != "" {
		weeksStr := strings.TrimSuffix(matches[1], "w")
		weeks, err := strconv.ParseInt(weeksStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid weeks value: %s", matches[1])
		}
		total += time.Duration(weeks) * 7 * 24 * time.Hour
	}

	// Parse days
	if matches[2] != "" {
		daysStr := strings.TrimSuffix(matches[2], "d")
		days, err := strconv.ParseInt(daysStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid days value: %s", matches[2])
		}
		total += time.Duration(days) * 24 * time.Hour
	}

	// Parse remaining (hours, minutes, seconds, etc.) using standard parser
	remainder := matches[3]
	if remainder != "" {
		dur, err := time.ParseDuration(remainder)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %s", remainder)
		}
		total += dur
	}

	// If no w/d and no remainder matched, this is not a valid extended format
	// Try standard parsing as fallback
	if matches[1] == "" && matches[2] == "" && matches[3] == "" {
		return time.ParseDuration(s)
	}

	return total, nil
}
