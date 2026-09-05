package storage

import (
	"cmp"
	"maps"

	"github.com/sirrobot01/decypharr/internal/config"
)

// HandleExistingEntryMerge merges an incoming entry with an existing one that
// shares the same infohash. This preserves placements, files, and tags from
// the existing entry that the incoming entry may not know about.
func HandleExistingEntryMerge(existing, incoming *Entry) *Entry {
	if incoming.Protocol == config.ProtocolNZB {
		return incoming
	}
	incoming.Files = mergeFiles(existing.Files, incoming.Files)
	incoming.ActiveProvider = cmp.Or(incoming.ActiveProvider, existing.ActiveProvider)
	incoming.Providers = mergeProviders(existing.Providers, incoming.Providers)
	incoming.Tags = mergeTags(existing.Tags, incoming.Tags)

	return incoming
}

func mergeProviders(existing, incoming map[string]*ProviderEntry) map[string]*ProviderEntry {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := maps.Clone(existing)
	for k, v := range incoming {
		if current, exists := merged[k]; !exists || v.AddedAt.After(current.AddedAt) {
			merged[k] = v
		}
	}

	return merged
}

func mergeFiles(existing, incoming map[string]*File) map[string]*File {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := maps.Clone(existing)
	for k, v := range incoming {
		if current, exists := merged[k]; !exists || v.AddedOn.After(current.AddedOn) {
			merged[k] = v
		}
	}

	return merged
}

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
