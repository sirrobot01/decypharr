package virtualfolders

import (
	"slices"
	"testing"

	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestVirtualFolderEmptyConditionsMatchHealthyItems(t *testing.T) {
	t.Parallel()
	compiled := mustCompileVirtualFolders(t, config.VirtualFolder{Name: "Everything"})
	healthy := &storage.EntryMetaInfo{Name: "Movie", AddedOn: time.Now()}
	bad := &storage.EntryMetaInfo{Name: "Broken", Bad: true, AddedOn: time.Now()}

	if !compiled.Matches("Everything", healthy, func() []string { return nil }) {
		t.Fatal("healthy item did not match empty conditions")
	}
	if compiled.Matches("Everything", bad, func() []string { return nil }) {
		t.Fatal("unhealthy item matched without include_bad")
	}
}

func TestVirtualFolderAllAndAnySemantics(t *testing.T) {
	t.Parallel()
	conditions := []config.VirtualFolderCondition{
		{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorContains, Value: "2160p"},
		{Field: config.VirtualFolderFieldProvider, Operator: config.VirtualFolderOperatorEquals, Value: "realdebrid"},
	}
	compiled := mustCompileVirtualFolders(t,
		config.VirtualFolder{Name: "All", Match: config.VirtualFolderMatchAll, Conditions: conditions},
		config.VirtualFolder{Name: "Any", Match: config.VirtualFolderMatchAny, Conditions: conditions},
	)
	meta := &storage.EntryMetaInfo{Name: "Movie.2160P", Provider: "other", AddedOn: time.Now()}

	if compiled.Matches("All", meta, func() []string { return nil }) {
		t.Fatal("all-condition folder matched only one condition")
	}
	if !compiled.Matches("Any", meta, func() []string { return nil }) {
		t.Fatal("any-condition folder did not match one condition")
	}
}

func TestVirtualFolderCaseSensitivityAndFileConditions(t *testing.T) {
	t.Parallel()
	compiled := mustCompileVirtualFolders(t,
		config.VirtualFolder{Name: "Insensitive", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorContains, Value: "2160p"}}},
		config.VirtualFolder{Name: "Sensitive", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorContains, Value: "2160p", CaseSensitive: true}}},
		config.VirtualFolder{Name: "NoSamples", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldFileName, Operator: config.VirtualFolderOperatorNotContains, Value: "sample"}}},
	)
	meta := &storage.EntryMetaInfo{Name: "Movie.2160P", AddedOn: time.Now()}

	if !compiled.Matches("Insensitive", meta, func() []string { return nil }) {
		t.Fatal("default text match should ignore capitalization")
	}
	if compiled.Matches("Sensitive", meta, func() []string { return nil }) {
		t.Fatal("case-sensitive text match ignored capitalization")
	}
	if compiled.Matches("NoSamples", meta, func() []string { return []string{"Movie.mkv", "SAMPLE.mkv"} }) {
		t.Fatal("negative file condition matched an item containing a sample")
	}
}

func TestVirtualFolderConditionFields(t *testing.T) {
	t.Parallel()
	meta := &storage.EntryMetaInfo{
		Name: "Show.S01.2160p", Size: 30 * 1024 * 1024 * 1024,
		AddedOn: time.Now().Add(-48 * time.Hour), Provider: "realdebrid", Protocol: "torrent", Category: "radarr",
	}
	files := func() []string { return []string{"Show.S01E01.mkv", "Show.S01E02.mkv", "poster.jpg"} }
	tests := []struct {
		name      string
		condition config.VirtualFolderCondition
	}{
		{name: "entry regex", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorMatchesRegex, Value: `S\d+`}},
		{name: "file regex", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldFileName, Operator: config.VirtualFolderOperatorMatchesRegex, Value: `S01E0[12]`}},
		{name: "size", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldSize, Operator: config.VirtualFolderOperatorGreaterThan, Value: "20GB"}},
		{name: "added", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldAdded, Operator: config.VirtualFolderOperatorWithinLast, Value: "7d"}},
		{name: "file count", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldFileCount, Operator: config.VirtualFolderOperatorGreaterThan, Value: "2"}},
		{name: "protocol", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldProtocol, Operator: config.VirtualFolderOperatorEquals, Value: "torrent"}},
		{name: "provider", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldProvider, Operator: config.VirtualFolderOperatorEquals, Value: "RealDebrid"}},
		{name: "category", condition: config.VirtualFolderCondition{Field: config.VirtualFolderFieldCategory, Operator: config.VirtualFolderOperatorContains, Value: "RADARR"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			compiled := mustCompileVirtualFolders(t, config.VirtualFolder{Name: "Test", Conditions: []config.VirtualFolderCondition{tt.condition}})
			if !compiled.Matches("Test", meta, files) {
				t.Fatalf("condition %#v did not match", tt.condition)
			}
		})
	}
}

func TestVirtualFolderAddedConditionIsTimeSensitive(t *testing.T) {
	t.Parallel()
	compiled := mustCompileVirtualFolders(t,
		config.VirtualFolder{Name: "Recent", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldAdded, Operator: config.VirtualFolderOperatorWithinLast, Value: "7d"}}},
		config.VirtualFolder{Name: "4K", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorContains, Value: "2160p"}}},
	)
	if !compiled.IsTimeSensitive("Recent") {
		t.Fatal("relative-time folder was treated as cacheable")
	}
	if compiled.IsTimeSensitive("4K") {
		t.Fatal("static folder was treated as time-sensitive")
	}
}

func mustCompileVirtualFolders(t *testing.T, definitions ...config.VirtualFolder) *Folders {
	t.Helper()
	compiled, err := Compile(definitions)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	return compiled
}

func TestCompileKeepsValidDefinitionsAndOwnsSnapshot(t *testing.T) {
	t.Parallel()
	definitions := []config.VirtualFolder{
		{Name: "First"},
		{Name: "Broken", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorMatchesRegex, Value: "["}}},
		{Name: "Last", Conditions: []config.VirtualFolderCondition{{Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorEquals, Value: "movie"}}},
	}
	compiled, err := Compile(definitions)
	if err == nil {
		t.Fatal("invalid definition was accepted")
	}
	if !slices.Equal(compiled.Names(), []string{"First", "Last"}) {
		t.Fatalf("valid folders = %v", compiled.Names())
	}
	definitions[2].Conditions[0].Value = "changed"
	names := compiled.Names()
	names[0] = "changed"
	if compiled.Names()[0] != "First" || !compiled.Matches("Last", &storage.EntryMetaInfo{Name: "movie"}, nil) {
		t.Fatal("external mutation changed compiled folders")
	}
}
