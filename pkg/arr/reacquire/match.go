package reacquire

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/arr"
)

type libraryMatch struct {
	library    arr.LibraryFile
	managed    ManagedFile
	confidence Confidence
}

// targetFileKey is the entry folder and filename a library symlink points at.
type targetFileKey struct {
	folder string
	name   string
}

// sizeFileKey identifies a managed file when its entry folder has moved: the
// downloader writes the file under its own name, so only the folder can drift.
type sizeFileKey struct {
	name string
	size int64
}

// matchStats reports how many Arr files reached the index, and why the rest
// did not.
type matchStats struct {
	libraryFiles    int
	managedFiles    int
	matchedFolder   int
	matchedSize     int
	notSymlink      int
	unreadable      int
	foreignTarget   int
	unknownEntry    int
	unknownFile     int
	ambiguousTarget int
	conflicted      int
	managedSkipped  int

	// One example path per miss, so a count that looks wrong can be chased
	// without a second run.
	samples map[string]string
}

// sample records the first library path to land in a miss bucket.
func (s *matchStats) sample(bucket, path string) {
	if s.samples == nil {
		s.samples = make(map[string]string, 5)
	}
	if _, seen := s.samples[bucket]; !seen {
		s.samples[bucket] = path
	}
}

func (s matchStats) matched() int {
	return s.matchedFolder + s.matchedSize
}

// matchLibraryFiles binds Arr library files to the managed files they point at.
// managedRoot is the mount directory that holds every entry folder; an empty
// root means the mount is off, and every readable target is considered.
func matchLibraryFiles(library []arr.LibraryFile, managed []ManagedFile, managedRoot string) ([]libraryMatch, matchStats) {
	stats := matchStats{libraryFiles: len(library), managedFiles: len(managed)}
	managedRoot = filepath.Clean(managedRoot)

	byTarget := make(map[targetFileKey][]ManagedFile, len(managed))
	bySize := make(map[sizeFileKey][]ManagedFile, len(managed))
	folders := make(map[string]struct{}, len(managed))
	for _, file := range managed {
		if file.EntryFolder == "" || file.FileName == "" {
			stats.managedSkipped++
			continue
		}
		name := filepath.Clean(file.FileName)
		folder := targetFileKey{folder: filepath.Clean(file.EntryFolder), name: name}
		byTarget[folder] = append(byTarget[folder], file)
		folders[folder.folder] = struct{}{}
		if file.FileSize > 0 {
			size := sizeFileKey{name: name, size: file.FileSize}
			bySize[size] = append(bySize[size], file)
		}
	}

	byFolder := make([]libraryMatch, 0, min(len(library), len(managed)))
	byTargetSize := make([]libraryMatch, 0)
	for _, libraryFile := range library {
		target, readable := symlinkTarget(libraryFile.Path)
		if target == "" {
			if readable {
				stats.notSymlink++
				stats.sample("not_symlink", libraryFile.Path)
			} else {
				stats.unreadable++
				stats.sample("unreadable", libraryFile.Path)
			}
			continue
		}
		if managedRoot != "." && !isUnder(target, managedRoot) {
			stats.foreignTarget++
			stats.sample("foreign_target", target)
			continue
		}
		name := filepath.Base(target)
		folder := targetFileKey{folder: entryFolderOf(target, managedRoot), name: name}
		if files := byTarget[folder]; len(files) == 1 {
			byFolder = append(byFolder, libraryMatch{library: libraryFile, managed: files[0], confidence: ConfidenceExactPath})
			continue
		} else if len(files) > 1 {
			stats.ambiguousTarget++
			stats.sample("ambiguous_target", target)
			continue
		}
		// The entry folder moved or was renamed. The filename and size still
		// identify the managed file, as long as they identify only one.
		if libraryFile.Size > 0 {
			if files := bySize[sizeFileKey{name: name, size: libraryFile.Size}]; len(files) == 1 {
				byTargetSize = append(byTargetSize, libraryMatch{library: libraryFile, managed: files[0], confidence: ConfidenceManagedTarget})
				continue
			} else if len(files) > 1 {
				stats.ambiguousTarget++
				stats.sample("ambiguous_target", target)
				continue
			}
		}
		if _, ok := folders[folder.folder]; ok {
			stats.unknownFile++
			stats.sample("unknown_file", target)
		} else {
			stats.unknownEntry++
			stats.sample("unknown_entry", target)
		}
	}

	claimedManaged := make(map[entryFileKey]bool, len(byFolder)+len(byTargetSize))
	claimedArrFile := make(map[int]bool, len(byFolder)+len(byTargetSize))
	matches, dropped := resolveMatches(byFolder, claimedManaged, claimedArrFile)
	stats.matchedFolder = len(matches)
	stats.conflicted = dropped
	sized, dropped := resolveMatches(byTargetSize, claimedManaged, claimedArrFile)
	stats.matchedSize = len(sized)
	stats.conflicted += dropped
	return append(matches, sized...), stats
}

// resolveMatches keeps the candidates that bind one managed file to one Arr
// file, and reports how many it dropped. Claimed files are carried across
// calls so a weaker candidate never overrides a stronger one.
func resolveMatches(candidates []libraryMatch, claimedManaged map[entryFileKey]bool, claimedArrFile map[int]bool) ([]libraryMatch, int) {
	managedUses := make(map[entryFileKey]int, len(candidates))
	arrFileUses := make(map[int]int, len(candidates))
	for _, candidate := range candidates {
		managedUses[managedKeyOf(candidate)]++
		arrFileUses[candidate.library.ArrFileID]++
	}

	matches := candidates[:0]
	for _, candidate := range candidates {
		managedKey := managedKeyOf(candidate)
		arrFileID := candidate.library.ArrFileID
		if managedUses[managedKey] != 1 || arrFileUses[arrFileID] != 1 {
			continue
		}
		if claimedManaged[managedKey] || claimedArrFile[arrFileID] {
			continue
		}
		claimedManaged[managedKey] = true
		claimedArrFile[arrFileID] = true
		matches = append(matches, candidate)
	}
	return matches, len(candidates) - len(matches)
}

func managedKeyOf(match libraryMatch) entryFileKey {
	return entryFileKey{entryID: match.managed.EntryID, fileID: match.managed.FileID}
}

// isUnder reports whether path sits inside root.
func isUnder(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// fields reports the counts on a log event.
func (s matchStats) fields(event *zerolog.Event) *zerolog.Event {
	event = event.
		Int("arr_files", s.libraryFiles).
		Int("managed_files", s.managedFiles).
		Int("indexed", s.matched()).
		Int("indexed_by_folder", s.matchedFolder).
		Int("indexed_by_size", s.matchedSize).
		Int("not_symlink", s.notSymlink).
		Int("unreadable", s.unreadable).
		Int("foreign_target", s.foreignTarget).
		Int("unknown_entry", s.unknownEntry).
		Int("unknown_file", s.unknownFile).
		Int("ambiguous_target", s.ambiguousTarget).
		Int("conflicted", s.conflicted).
		Int("managed_incomplete", s.managedSkipped)
	for bucket, path := range s.samples {
		event = event.Str("sample_"+bucket, path)
	}
	return event
}

// entryFolderOf returns the entry folder a mount target sits in: the first
// path component below the mount root. The downloader also links files nested
// in subdirectories of an entry, so the parent directory is not the folder.
func entryFolderOf(target, managedRoot string) string {
	if managedRoot == "." {
		return filepath.Base(filepath.Dir(target))
	}
	relative := strings.TrimPrefix(target, managedRoot+string(os.PathSeparator))
	folder, _, nested := strings.Cut(relative, string(os.PathSeparator))
	if !nested {
		return ""
	}
	return folder
}

// symlinkTarget resolves a library symlink to an absolute path. It reports an
// empty target for a path that is not a symlink, and readable false for one it
// could not read at all.
func symlinkTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", true
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), true
}

func bindingsFromMatches(instance arr.Arr, generation uint64, matches []libraryMatch) []Binding {
	bindings := make([]Binding, 0, len(matches))
	for _, match := range matches {
		// A Binding outlives the Arr response. Take ownership here so a
		// decoder can never make the short path retain its whole document.
		libraryPath := strings.Clone(match.library.Path)
		bindings = append(bindings, Binding{
			ArrName:                instance.Name,
			ArrType:                instance.Type,
			ArrInstanceFingerprint: instance.Fingerprint(),
			EntryID:                match.managed.EntryID,
			EntryName:              match.managed.EntryName,
			EntryFileID:            match.managed.FileID,
			EntryFileName:          match.managed.FileName,
			DownloadID:             match.managed.DownloadID,
			ArrFileID:              match.library.ArrFileID,
			LibraryPath:            libraryPath,
			SeriesID:               match.library.SeriesID,
			SeasonNumber:           match.library.SeasonNumber,
			EpisodeIDs:             match.library.EpisodeIDs,
			MovieID:                match.library.MovieID,
			Confidence:             match.confidence,
			Generation:             generation,
			UpdatedAt:              time.Now().UTC(),
		})
	}
	return bindings
}
