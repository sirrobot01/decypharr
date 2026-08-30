package manager

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type VirtualFolders struct {
	byName  map[string]compiledVirtualFolder
	folders []string
}

type compiledVirtualFolder struct {
	definition config.VirtualFolder
	conditions []directoryFilter
}

type directoryFilter struct {
	definition     config.VirtualFolderCondition
	regex          *regexp.Regexp
	sizeThreshold  int64
	ageThreshold   time.Duration
	countThreshold int
}

type VirtualFolderPreviewItem struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Size     int64  `json:"size"`
}

func compileVirtualFolders(definitions []config.VirtualFolder) (*VirtualFolders, error) {
	compiled := &VirtualFolders{
		byName:  make(map[string]compiledVirtualFolder, len(definitions)),
		folders: make([]string, 0, len(definitions)),
	}

	for _, definition := range definitions {
		config.NormalizeVirtualFolder(&definition)
		if err := config.ValidateVirtualFolder(definition, nil); err != nil {
			return nil, fmt.Errorf("virtual folder %q: %w", definition.Name, err)
		}

		folder := compiledVirtualFolder{definition: definition}
		for _, condition := range definition.Conditions {
			filter := directoryFilter{definition: condition}
			var err error
			switch condition.Field {
			case config.VirtualFolderFieldEntryName, config.VirtualFolderFieldFileName, config.VirtualFolderFieldCategory:
				if condition.Operator == config.VirtualFolderOperatorMatchesRegex || condition.Operator == config.VirtualFolderOperatorNotMatchesRegex {
					pattern := condition.Value
					if !condition.CaseSensitive {
						pattern = "(?i:" + pattern + ")"
					}
					filter.regex, err = regexp.Compile(pattern)
				}
			case config.VirtualFolderFieldSize:
				filter.sizeThreshold, err = config.ParseSize(condition.Value)
			case config.VirtualFolderFieldAdded:
				filter.ageThreshold, err = utils.ParseDuration(condition.Value)
			case config.VirtualFolderFieldFileCount:
				filter.countThreshold, err = strconv.Atoi(condition.Value)
			}
			if err != nil {
				return nil, fmt.Errorf("virtual folder %q condition %s/%s: %w", definition.Name, condition.Field, condition.Operator, err)
			}
			folder.conditions = append(folder.conditions, filter)
		}

		compiled.byName[definition.Name] = folder
		compiled.folders = append(compiled.folders, definition.Name)
	}
	return compiled, nil
}

func (m *Manager) initVirtualFolders() {
	compiled := &VirtualFolders{
		byName:  make(map[string]compiledVirtualFolder, len(m.config.VirtualFolders)),
		folders: make([]string, 0, len(m.config.VirtualFolders)),
	}
	for _, definition := range m.config.VirtualFolders {
		one, err := compileVirtualFolders([]config.VirtualFolder{definition})
		if err != nil {
			m.logger.Error().Err(err).Msg("Ignoring invalid virtual folder")
			continue
		}
		for name, folder := range one.byName {
			compiled.byName[name] = folder
			compiled.folders = append(compiled.folders, name)
		}
	}
	m.virtualFoldersMu.Lock()
	m.virtualFolders = compiled
	m.virtualFoldersMu.Unlock()
}

// ApplyVirtualFolders atomically replaces the compiled definitions, clears all
// directory caches, and invalidates the mount root so additions/removals become
// visible without a service restart.
func (m *Manager) ApplyVirtualFolders(definitions []config.VirtualFolder) error {
	compiled, err := compileVirtualFolders(definitions)
	if err != nil {
		return err
	}

	m.virtualFoldersMu.Lock()
	m.virtualFolders = compiled
	m.virtualFoldersMu.Unlock()

	if m.entry != nil {
		m.entry.Refresh()
	}
	if m.mountManager != nil {
		go func() {
			if err := m.mountManager.Refresh([]string{""}); err != nil {
				m.logger.Warn().Err(err).Msg("Failed to refresh mount after virtual-folder update")
			}
		}()
	}
	return nil
}

func (m *Manager) virtualFoldersSnapshot() *VirtualFolders {
	m.virtualFoldersMu.RLock()
	defer m.virtualFoldersMu.RUnlock()
	return m.virtualFolders
}

func (m *Manager) GetVirtualFolders() []string {
	virtualFolders := m.virtualFoldersSnapshot()
	if virtualFolders == nil {
		return nil
	}
	return append([]string(nil), virtualFolders.folders...)
}

func (cf *VirtualFolders) has(folderName string) bool {
	if cf == nil {
		return false
	}
	_, ok := cf.byName[folderName]
	return ok
}

func (cf *VirtualFolders) isTimeSensitive(folderName string) bool {
	if cf == nil {
		return false
	}
	folder, ok := cf.byName[folderName]
	if !ok {
		return false
	}
	for _, condition := range folder.conditions {
		if condition.definition.Field == config.VirtualFolderFieldAdded {
			return true
		}
	}
	return false
}

func (cf *VirtualFolders) matchesFilter(folderName string, meta *storage.EntryMetaInfo, getFileNames func() []string) bool {
	folder, ok := cf.byName[folderName]
	if !ok || meta == nil {
		return false
	}
	if meta.Bad && !folder.definition.IncludeBad {
		return false
	}
	if len(folder.conditions) == 0 {
		return true
	}

	matchAny := folder.definition.Match == config.VirtualFolderMatchAny
	for _, filter := range folder.conditions {
		matched := checkSingleFilter(filter, meta, getFileNames)
		if matchAny && matched {
			return true
		}
		if !matchAny && !matched {
			return false
		}
	}
	return !matchAny
}

func checkSingleFilter(filter directoryFilter, meta *storage.EntryMetaInfo, getFileNames func() []string) bool {
	condition := filter.definition
	switch condition.Field {
	case config.VirtualFolderFieldEntryName:
		return matchText(meta.Name, condition, filter.regex)
	case config.VirtualFolderFieldFileName:
		fileNames := getFileNames()
		negative := isNegativeOperator(condition.Operator)
		for _, name := range fileNames {
			if matchText(name, positiveCondition(condition), filter.regex) {
				return !negative
			}
		}
		return negative
	case config.VirtualFolderFieldSize:
		if condition.Operator == config.VirtualFolderOperatorGreaterThan {
			return meta.Size > filter.sizeThreshold
		}
		return meta.Size < filter.sizeThreshold
	case config.VirtualFolderFieldAdded:
		return time.Since(meta.AddedOn) <= filter.ageThreshold
	case config.VirtualFolderFieldFileCount:
		if condition.Operator == config.VirtualFolderOperatorGreaterThan {
			return len(getFileNames()) > filter.countThreshold
		}
		return len(getFileNames()) < filter.countThreshold
	case config.VirtualFolderFieldProtocol:
		return matchEquality(meta.Protocol, condition.Value, condition.Operator, false)
	case config.VirtualFolderFieldProvider:
		return matchEquality(meta.Provider, condition.Value, condition.Operator, condition.CaseSensitive)
	case config.VirtualFolderFieldCategory:
		return matchText(meta.Category, condition, filter.regex)
	default:
		return false
	}
}

func matchText(candidate string, condition config.VirtualFolderCondition, compiledRegex *regexp.Regexp) bool {
	wanted := condition.Value
	if !condition.CaseSensitive && condition.Operator != config.VirtualFolderOperatorMatchesRegex && condition.Operator != config.VirtualFolderOperatorNotMatchesRegex {
		candidate = strings.ToLower(candidate)
		wanted = strings.ToLower(wanted)
	}

	switch condition.Operator {
	case config.VirtualFolderOperatorContains:
		return strings.Contains(candidate, wanted)
	case config.VirtualFolderOperatorNotContains:
		return !strings.Contains(candidate, wanted)
	case config.VirtualFolderOperatorStartsWith:
		return strings.HasPrefix(candidate, wanted)
	case config.VirtualFolderOperatorNotStartsWith:
		return !strings.HasPrefix(candidate, wanted)
	case config.VirtualFolderOperatorEndsWith:
		return strings.HasSuffix(candidate, wanted)
	case config.VirtualFolderOperatorNotEndsWith:
		return !strings.HasSuffix(candidate, wanted)
	case config.VirtualFolderOperatorEquals:
		return candidate == wanted
	case config.VirtualFolderOperatorNotEquals:
		return candidate != wanted
	case config.VirtualFolderOperatorMatchesRegex:
		return compiledRegex != nil && compiledRegex.MatchString(candidate)
	case config.VirtualFolderOperatorNotMatchesRegex:
		return compiledRegex == nil || !compiledRegex.MatchString(candidate)
	default:
		return false
	}
}

func isNegativeOperator(operator string) bool {
	switch operator {
	case config.VirtualFolderOperatorNotContains,
		config.VirtualFolderOperatorNotStartsWith,
		config.VirtualFolderOperatorNotEndsWith,
		config.VirtualFolderOperatorNotEquals,
		config.VirtualFolderOperatorNotMatchesRegex:
		return true
	default:
		return false
	}
}

func positiveCondition(condition config.VirtualFolderCondition) config.VirtualFolderCondition {
	switch condition.Operator {
	case config.VirtualFolderOperatorNotContains:
		condition.Operator = config.VirtualFolderOperatorContains
	case config.VirtualFolderOperatorNotStartsWith:
		condition.Operator = config.VirtualFolderOperatorStartsWith
	case config.VirtualFolderOperatorNotEndsWith:
		condition.Operator = config.VirtualFolderOperatorEndsWith
	case config.VirtualFolderOperatorNotEquals:
		condition.Operator = config.VirtualFolderOperatorEquals
	case config.VirtualFolderOperatorNotMatchesRegex:
		condition.Operator = config.VirtualFolderOperatorMatchesRegex
	}
	return condition
}

func matchEquality(candidate, wanted, operator string, caseSensitive bool) bool {
	if !caseSensitive {
		candidate = strings.ToLower(candidate)
		wanted = strings.ToLower(wanted)
	}
	equal := candidate == wanted
	if operator == config.VirtualFolderOperatorNotEquals {
		return !equal
	}
	return equal
}

func (m *Manager) virtualFolderFileNames(meta *storage.EntryMetaInfo) func() []string {
	var loaded bool
	var names []string
	return func() []string {
		if loaded {
			return names
		}
		loaded = true
		item, err := m.storage.Get(meta.InfoHash)
		if err != nil || item == nil {
			return nil
		}
		names = make([]string, 0, len(item.Files))
		for name := range item.Files {
			names = append(names, name)
		}
		return names
	}
}

func (m *Manager) PreviewVirtualFolder(definition config.VirtualFolder, limit int) (int, []VirtualFolderPreviewItem, error) {
	compiled, err := compileVirtualFolders([]config.VirtualFolder{definition})
	if err != nil {
		return 0, nil, err
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}

	total := 0
	samples := make([]VirtualFolderPreviewItem, 0, limit)
	seen := make(map[string]struct{})
	err = m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		if !compiled.matchesFilter(definition.Name, meta, m.virtualFolderFileNames(meta)) {
			return nil
		}
		if _, ok := seen[meta.Name]; ok {
			return nil
		}
		seen[meta.Name] = struct{}{}
		total++
		if len(samples) < limit {
			samples = append(samples, VirtualFolderPreviewItem{
				Name: meta.Name, Provider: meta.Provider, Protocol: meta.Protocol, Size: meta.Size,
			})
		}
		return nil
	})
	return total, samples, err
}
