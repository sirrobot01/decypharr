package manager

import (
	"testing"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestIsListableEntryMetaSkipsInternalAndInvalidNames(t *testing.T) {
	tests := []struct {
		name string
		meta *storage.EntryMetaInfo
		want bool
	}{
		{
			name: "valid entry",
			meta: &storage.EntryMetaInfo{InfoHash: "abc123", Name: "Example"},
			want: true,
		},
		{
			name: "migration status",
			meta: &storage.EntryMetaInfo{InfoHash: "__migration_status__", Name: ""},
			want: false,
		},
		{
			name: "empty name",
			meta: &storage.EntryMetaInfo{InfoHash: "abc123", Name: ""},
			want: false,
		},
		{
			name: "dot name",
			meta: &storage.EntryMetaInfo{InfoHash: "abc123", Name: "."},
			want: false,
		},
		{
			name: "slash name",
			meta: &storage.EntryMetaInfo{InfoHash: "abc123", Name: "bad/name"},
			want: false,
		},
		{
			name: "nul name",
			meta: &storage.EntryMetaInfo{InfoHash: "abc123", Name: "bad\x00name"},
			want: false,
		},
		{
			name: "nil meta",
			meta: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isListableEntryMeta(tt.meta); got != tt.want {
				t.Fatalf("isListableEntryMeta() = %v, want %v", got, tt.want)
			}
		})
	}
}
