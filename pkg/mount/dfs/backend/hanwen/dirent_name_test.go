package hanwen

import (
	"strings"
	"testing"
)

func TestValidDirentName(t *testing.T) {
	for name, want := range map[string]bool{
		"Release.Name.2024.1080p":            true,
		"":                                   false,
		".":                                  false,
		"..":                                 false,
		"AC/DC.Live":                         false,
		"bad\x00name":                        false,
		strings.Repeat("a", maxDirentName):   true,
		strings.Repeat("a", maxDirentName+1): false,
	} {
		if got := validDirentName(name); got != want {
			t.Errorf("validDirentName(%q) = %v, want %v", truncateForLog(name), got, want)
		}
	}
}
