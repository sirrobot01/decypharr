package storage

import "maps"

import "github.com/sirrobot01/decypharr/internal/config"

// HandleExistingEntryMerge merges an incoming entry with an existing one that
// shares the same infohash. This preserves placements, files, and tags from
// the existing entry that the incoming entry may not know about.
func HandleExistingEntryMerge(existing, incoming *Entry) *Entry {
	// If NZB entry, ignore merging - just return incoming
	if incoming.Protocol == config.ProtocolNZB {
		return incoming
	}
	incoming.Files = mergeFiles(existing.Files, incoming.Files)
	incoming.ActiveProvider = selectActivePlacement(existing, incoming)
	incoming.Providers = mergeProviders(existing.Providers, incoming.Providers)
	incoming.Tags = mergeTags(existing.Tags, incoming.Tags)

	return incoming
}

// mergeProviders merges two placement maps, preferring newer data for same debrid
func mergeProviders(existing, incoming map[string]*ProviderEntry) map[string]*ProviderEntry {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := make(map[string]*ProviderEntry)

	// Copy existing placements
	maps.Copy(merged, existing)

	// Merge incoming placements (overwrites if same key)
	for k, v := range incoming {
		if existingPlacement, exists := merged[k]; exists {
			// Keep placement with more recent UpdatedAt
			if v.AddedAt.After(existingPlacement.AddedAt) {
				merged[k] = v
			}
		} else {
			merged[k] = v
		}
	}

	return merged
}

// mergeFiles merges two file maps, preferring files with newer AddedOn
func mergeFiles(existing, incoming map[string]*File) map[string]*File {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := make(map[string]*File)

	// Copy existing files
	maps.Copy(merged, existing)

	// Merge incoming files
	for k, v := range incoming {
		if existingFile, exists := merged[k]; exists {
			// Prefer file with newer AddedOn timestamp
			if v.AddedOn.After(existingFile.AddedOn) {
				merged[k] = v
			}
		} else {
			merged[k] = v
		}
	}

	return merged
}

// selectActivePlacement selects the active debrid placement
func selectActivePlacement(existing, incoming *Entry) string {
	// Prefer incoming if it has an active placement
	if incoming.ActiveProvider != "" {
		return incoming.ActiveProvider
	}
	return existing.ActiveProvider
}

// mergeTags merges two tag slices, removing duplicates
func mergeTags(existing, incoming []string) []string {
	if len(existing) == 0 {
		return incoming
	}
	if len(incoming) == 0 {
		return existing
	}

	tagSet := make(map[string]bool)
	for _, tag := range existing {
		tagSet[tag] = true
	}
	for _, tag := range incoming {
		tagSet[tag] = true
	}

	merged := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		merged = append(merged, tag)
	}
	return merged
}
