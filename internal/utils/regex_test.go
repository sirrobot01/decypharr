package utils

import (
	"strings"
	"testing"
)

func TestSafeFolderName(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback string
		want     string
	}{
		{
			name:     "removes path separators and invalid characters",
			value:    "../bad:name?",
			fallback: "hash",
			want:     "badname",
		},
		{
			name:     "falls back for empty names",
			value:    " . ",
			fallback: "hash",
			want:     "hash",
		},
		{
			name:     "falls back for reserved device names",
			value:    "CON",
			fallback: "hash",
			want:     "hash",
		},
		{
			name:     "falls back for reserved device names with multiple extensions",
			value:    "CON.foo.bar",
			fallback: "hash",
			want:     "hash",
		},
		{
			name:     "truncates long components",
			value:    strings.Repeat("a", 300),
			fallback: "hash",
			want:     strings.Repeat("a", 255),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeFolderName(tt.value, tt.fallback); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
