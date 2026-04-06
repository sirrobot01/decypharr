package manager

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

const (
	filterByInclude string = "include"
	filterByExclude string = "exclude"

	filterByStartsWith    string = "starts_with"
	filterByEndsWith      string = "ends_with"
	filterByNotStartsWith string = "not_starts_with"
	filterByNotEndsWith   string = "not_ends_with"

	filterByRegex    string = "regex"
	filterByNotRegex string = "not_regex"

	filterByExactMatch    string = "exact_match"
	filterByNotExactMatch string = "not_exact_match"

	filterBySizeGT string = "size_gt"
	filterBySizeLT string = "size_lt"

	filterBLastAdded string = "last_added"

	filterByFileCountGT   string = "file_count_gt"
	filterByFileCountLT   string = "file_count_lt"
	filterByFilesRegex    string = "files_regex"
	filterByNotFilesRegex string = "not_files_regex"
)

type CustomFolders struct {
	filters map[string][]directoryFilter
	folders []string
}

type directoryFilter struct {
	filterType     string
	value          string
	regex          *regexp.Regexp // only for regex/not_regex/files_regex/not_files_regex
	sizeThreshold  int64          // only for size_gt/size_lt
	ageThreshold   time.Duration  // only for last_added
	countThreshold int            // only for file_count_gt/file_count_lt
}

func (m *Manager) initCustomFolders() {
	var customFolders []string
	dirFilters := map[string][]directoryFilter{}
	for name, value := range m.config.CustomFolders {
		for filterType, v := range value.Filters {
			df := directoryFilter{filterType: filterType, value: v}
			switch filterType {
			case filterByRegex, filterByNotRegex, filterByFilesRegex, filterByNotFilesRegex:
				df.regex = regexp.MustCompile(v)
			case filterBySizeGT, filterBySizeLT:
				df.sizeThreshold, _ = config.ParseSize(v)
			case filterBLastAdded:
				df.ageThreshold, _ = utils.ParseDuration(v)
			case filterByFileCountGT, filterByFileCountLT:
				fmt.Sscanf(v, "%d", &df.countThreshold)
			}
			dirFilters[name] = append(dirFilters[name], df)
		}
		customFolders = append(customFolders, name)

	}
	m.customFolders = &CustomFolders{
		filters: dirFilters,
		folders: customFolders,
	}
}

func (m *Manager) GetCustomFolders() []string {
	return m.customFolders.folders
}

// matchesFilter checks if a torrent matches all filters for a folder.
// getFileNames is a lazy loader called only when files_regex/not_files_regex/file_count filters are needed.
func (cf *CustomFolders) matchesFilter(folderName string, fileInfo os.FileInfo, addedTime time.Time, getFileNames func() []string) bool {
	filters, ok := cf.filters[folderName]
	if !ok {
		return false
	}

	// Separate regex and files_regex filters — when both are present, treat as OR
	var regexFilters []directoryFilter
	var filesRegexFilters []directoryFilter
	var otherFilters []directoryFilter

	for _, f := range filters {
		switch f.filterType {
		case filterByRegex:
			regexFilters = append(regexFilters, f)
		case filterByFilesRegex:
			filesRegexFilters = append(filesRegexFilters, f)
		default:
			otherFilters = append(otherFilters, f)
		}
	}

	// If both regex and files_regex present: OR logic (folder name OR file contents match)
	if len(regexFilters) > 0 && len(filesRegexFilters) > 0 {
		name := fileInfo.Name()
		nameMatched := false
		for _, f := range regexFilters {
			if f.regex.MatchString(name) {
				nameMatched = true
				break
			}
		}
		if !nameMatched {
			fileNames := getFileNames()
			filesMatched := false
			for _, f := range filesRegexFilters {
				for _, fn := range fileNames {
					if f.regex.MatchString(fn) {
						filesMatched = true
						break
					}
				}
				if filesMatched {
					break
				}
			}
			if !filesMatched {
				return false
			}
		}
	} else {
		// Single type present — AND logic
		for _, filter := range append(regexFilters, filesRegexFilters...) {
			if !cf.checkSingleFilter(filter, fileInfo, addedTime, getFileNames) {
				return false
			}
		}
	}

	// All other filters AND match
	for _, filter := range otherFilters {
		if !cf.checkSingleFilter(filter, fileInfo, addedTime, getFileNames) {
			return false
		}
	}

	return true
}

// checkSingleFilter checks if a single filter matches
func (cf *CustomFolders) checkSingleFilter(filter directoryFilter, fileInfo os.FileInfo, addedTime time.Time, getFileNames func() []string) bool {
	name := fileInfo.Name()
	size := fileInfo.Size()

	switch filter.filterType {
	case filterByInclude:
		return strings.Contains(name, filter.value)
	case filterByExclude:
		return !strings.Contains(name, filter.value)
	case filterByStartsWith:
		return regexp.MustCompile("^" + regexp.QuoteMeta(filter.value)).MatchString(name)
	case filterByEndsWith:
		return regexp.MustCompile(regexp.QuoteMeta(filter.value) + "$").MatchString(name)
	case filterByNotStartsWith:
		return !regexp.MustCompile("^" + regexp.QuoteMeta(filter.value)).MatchString(name)
	case filterByNotEndsWith:
		return !regexp.MustCompile(regexp.QuoteMeta(filter.value) + "$").MatchString(name)
	case filterByRegex:
		return filter.regex.MatchString(name)
	case filterByNotRegex:
		return !filter.regex.MatchString(name)
	case filterByExactMatch:
		return name == filter.value
	case filterByNotExactMatch:
		return name != filter.value
	case filterBySizeGT:
		return size > filter.sizeThreshold
	case filterBySizeLT:
		return size < filter.sizeThreshold
	case filterBLastAdded:
		return time.Since(addedTime) <= filter.ageThreshold
	case filterByFileCountGT:
		return len(getFileNames()) > filter.countThreshold
	case filterByFileCountLT:
		return len(getFileNames()) < filter.countThreshold
	case filterByFilesRegex:
		for _, fn := range getFileNames() {
			if filter.regex.MatchString(fn) {
				return true
			}
		}
		return false
	case filterByNotFilesRegex:
		for _, fn := range getFileNames() {
			if filter.regex.MatchString(fn) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
