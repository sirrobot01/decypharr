package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	VirtualFolderFieldEntryName = "entry_name"
	VirtualFolderFieldFileName  = "file_name"
	VirtualFolderFieldSize      = "size"
	VirtualFolderFieldAdded     = "added"
	VirtualFolderFieldFileCount = "file_count"
	VirtualFolderFieldProtocol  = "protocol"
	VirtualFolderFieldProvider  = "provider"
	VirtualFolderFieldCategory  = "category"

	VirtualFolderOperatorContains        = "contains"
	VirtualFolderOperatorNotContains     = "not_contains"
	VirtualFolderOperatorStartsWith      = "starts_with"
	VirtualFolderOperatorNotStartsWith   = "not_starts_with"
	VirtualFolderOperatorEndsWith        = "ends_with"
	VirtualFolderOperatorNotEndsWith     = "not_ends_with"
	VirtualFolderOperatorEquals          = "equals"
	VirtualFolderOperatorNotEquals       = "not_equals"
	VirtualFolderOperatorMatchesRegex    = "matches_regex"
	VirtualFolderOperatorNotMatchesRegex = "not_matches_regex"
	VirtualFolderOperatorGreaterThan     = "greater_than"
	VirtualFolderOperatorLessThan        = "less_than"
	VirtualFolderOperatorWithinLast      = "within_last"
)

var virtualFolderReservedNames = []string{
	"__all__",
	"__bad__",
	"torrents",
	"nzbs",
	"version.txt",
}

var windowsReservedFolderName = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$`)

var legacyFilterOrder = []string{
	"name", "category",
	"include", "exclude",
	"starts_with", "ends_with", "not_starts_with", "not_ends_with",
	"exact_match", "not_exact_match",
	"regex", "not_regex",
	"size_gt", "size_lt", "last_added",
	"file_count_gt", "file_count_lt",
	"files_regex", "not_files_regex",
}

var virtualFolderDurationPattern = regexp.MustCompile(`^(?:(\d+)w)?(?:(\d+)d)?(.*)$`)

// MigrateVirtualFolders converts the former map-based custom_folders format to
// the ordered virtual_folders format. It is idempotent and intentionally uses
// documented all-condition semantics instead of preserving the old hidden
// regex/files_regex OR exception.
func (c *Config) MigrateVirtualFolders() {
	if len(c.VirtualFolders) > 0 {
		for i := range c.VirtualFolders {
			NormalizeVirtualFolder(&c.VirtualFolders[i])
		}
		c.CustomFolders = nil
		return
	}
	if len(c.CustomFolders) == 0 {
		return
	}

	names := make([]string, 0, len(c.CustomFolders))
	for name := range c.CustomFolders {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	for _, name := range names {
		legacy := c.CustomFolders[name]
		folder := VirtualFolder{
			Name:  strings.TrimSpace(name),
			Match: VirtualFolderMatchAll,
		}

		keys := make([]string, 0, len(legacy.Filters))
		for _, key := range legacyFilterOrder {
			if _, ok := legacy.Filters[key]; ok {
				keys = append(keys, key)
			}
		}
		for key := range legacy.Filters {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}

		for _, key := range keys {
			condition := legacyVirtualFolderCondition(key, legacy.Filters[key])
			folder.Conditions = append(folder.Conditions, condition)
		}
		NormalizeVirtualFolder(&folder)
		c.VirtualFolders = append(c.VirtualFolders, folder)
	}

	// Save emits only the canonical representation after an old config has been
	// loaded. The original file remains untouched until the user's next save.
	c.CustomFolders = nil
}

// NormalizeVirtualFolder trims user-entered values and applies defaults before
// validation, compilation, or preview.
func NormalizeVirtualFolder(folder *VirtualFolder) {
	folder.Name = strings.TrimSpace(folder.Name)
	if folder.Match == "" {
		folder.Match = VirtualFolderMatchAll
	}
	for i := range folder.Conditions {
		folder.Conditions[i].Field = strings.TrimSpace(folder.Conditions[i].Field)
		folder.Conditions[i].Operator = strings.TrimSpace(folder.Conditions[i].Operator)
		folder.Conditions[i].Value = strings.TrimSpace(folder.Conditions[i].Value)
	}
}

func legacyVirtualFolderCondition(filterType, value string) VirtualFolderCondition {
	c := VirtualFolderCondition{Value: value}
	switch filterType {
	case "include":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorContains
		c.CaseSensitive = true
	case "exclude":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorNotContains
		c.CaseSensitive = true
	case "starts_with":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorStartsWith
		c.CaseSensitive = true
	case "not_starts_with":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorNotStartsWith
		c.CaseSensitive = true
	case "ends_with":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorEndsWith
		c.CaseSensitive = true
	case "not_ends_with":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorNotEndsWith
		c.CaseSensitive = true
	case "exact_match":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorEquals
		c.CaseSensitive = true
	case "not_exact_match":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorNotEquals
		c.CaseSensitive = true
	case "regex":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorMatchesRegex
		c.CaseSensitive = true
	case "not_regex":
		c.Field, c.Operator = VirtualFolderFieldEntryName, VirtualFolderOperatorNotMatchesRegex
		c.CaseSensitive = true
	case "size_gt":
		c.Field, c.Operator = VirtualFolderFieldSize, VirtualFolderOperatorGreaterThan
	case "size_lt":
		c.Field, c.Operator = VirtualFolderFieldSize, VirtualFolderOperatorLessThan
	case "last_added":
		c.Field, c.Operator = VirtualFolderFieldAdded, VirtualFolderOperatorWithinLast
	case "file_count_gt":
		c.Field, c.Operator = VirtualFolderFieldFileCount, VirtualFolderOperatorGreaterThan
	case "file_count_lt":
		c.Field, c.Operator = VirtualFolderFieldFileCount, VirtualFolderOperatorLessThan
	case "files_regex":
		c.Field, c.Operator = VirtualFolderFieldFileName, VirtualFolderOperatorMatchesRegex
		c.CaseSensitive = true
	case "not_files_regex":
		c.Field, c.Operator = VirtualFolderFieldFileName, VirtualFolderOperatorNotMatchesRegex
		c.CaseSensitive = true
	case "name":
		c.Field = VirtualFolderFieldEntryName
		c.Operator, c.Value = legacyTextOperator(value)
	case "category":
		c.Field = VirtualFolderFieldCategory
		c.Operator, c.Value = legacyTextOperator(value)
	default:
		// Retain unknown legacy keys so validation can give the user a useful
		// error instead of silently dropping their configuration.
		c.Field, c.Operator = filterType, "unknown"
	}
	return c
}

func legacyTextOperator(value string) (string, string) {
	if !strings.ContainsAny(value, "*?") {
		return VirtualFolderOperatorContains, value
	}
	quoted := regexp.QuoteMeta(value)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	quoted = strings.ReplaceAll(quoted, `\?`, `.`)
	return VirtualFolderOperatorMatchesRegex, "^" + quoted + "$"
}

func (c *Config) ValidateVirtualFolders() error {
	reserved := append([]string(nil), virtualFolderReservedNames...)
	for _, d := range c.Debrids {
		if strings.TrimSpace(d.Name) != "" {
			reserved = append(reserved, d.Name)
		}
	}

	seen := make(map[string]struct{}, len(c.VirtualFolders))
	for i := range c.VirtualFolders {
		folder := &c.VirtualFolders[i]
		NormalizeVirtualFolder(folder)
		if err := ValidateVirtualFolder(*folder, reserved); err != nil {
			return fmt.Errorf("virtual folder %d: %w", i+1, err)
		}
		key := strings.ToLower(folder.Name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("virtual folder %q is defined more than once", folder.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateVirtualFolder validates a single definition. reservedNames is used by
// the config validator for built-in/provider collision checks and may be nil for
// a standalone preview.
func ValidateVirtualFolder(folder VirtualFolder, reservedNames []string) error {
	NormalizeVirtualFolder(&folder)
	name := strings.TrimSpace(folder.Name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("name %q is not allowed", name)
	}
	if !utf8.ValidString(name) || len([]byte(name)) > 255 {
		return fmt.Errorf("name must be valid UTF-8 and no longer than 255 bytes")
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("name contains characters that are not portable across mounts and shares")
	}
	if strings.HasSuffix(name, ".") || windowsReservedFolderName.MatchString(name) {
		return fmt.Errorf("name %q is reserved by common filesystems", name)
	}
	for _, reserved := range reservedNames {
		if strings.EqualFold(name, strings.TrimSpace(reserved)) {
			return fmt.Errorf("name %q conflicts with the built-in or provider folder %q", name, reserved)
		}
	}

	match := folder.Match
	if match == "" {
		match = VirtualFolderMatchAll
	}
	if match != VirtualFolderMatchAll && match != VirtualFolderMatchAny {
		return fmt.Errorf("match must be %q or %q", VirtualFolderMatchAll, VirtualFolderMatchAny)
	}

	for i, condition := range folder.Conditions {
		if err := validateVirtualFolderCondition(condition); err != nil {
			return fmt.Errorf("condition %d: %w", i+1, err)
		}
	}
	return nil
}

func validateVirtualFolderCondition(condition VirtualFolderCondition) error {
	value := strings.TrimSpace(condition.Value)
	if value == "" {
		return fmt.Errorf("value is required")
	}

	textOperators := []string{
		VirtualFolderOperatorContains, VirtualFolderOperatorNotContains,
		VirtualFolderOperatorStartsWith, VirtualFolderOperatorNotStartsWith,
		VirtualFolderOperatorEndsWith, VirtualFolderOperatorNotEndsWith,
		VirtualFolderOperatorEquals, VirtualFolderOperatorNotEquals,
		VirtualFolderOperatorMatchesRegex, VirtualFolderOperatorNotMatchesRegex,
	}
	comparisonOperators := []string{VirtualFolderOperatorGreaterThan, VirtualFolderOperatorLessThan}

	switch condition.Field {
	case VirtualFolderFieldEntryName, VirtualFolderFieldFileName, VirtualFolderFieldCategory:
		if !slices.Contains(textOperators, condition.Operator) {
			return fmt.Errorf("operator %q is not valid for %s", condition.Operator, condition.Field)
		}
		if condition.Operator == VirtualFolderOperatorMatchesRegex || condition.Operator == VirtualFolderOperatorNotMatchesRegex {
			if _, err := regexp.Compile(value); err != nil {
				return fmt.Errorf("invalid regular expression: %w", err)
			}
		}
	case VirtualFolderFieldSize:
		if !slices.Contains(comparisonOperators, condition.Operator) {
			return fmt.Errorf("operator %q is not valid for size", condition.Operator)
		}
		size, err := ParseSize(value)
		if err != nil {
			return fmt.Errorf("invalid size %q: %w", value, err)
		}
		if size < 0 {
			return fmt.Errorf("size must not be negative")
		}
	case VirtualFolderFieldAdded:
		if condition.Operator != VirtualFolderOperatorWithinLast {
			return fmt.Errorf("operator %q is not valid for added time", condition.Operator)
		}
		d, err := parseVirtualFolderDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid duration %q; use a positive value such as 12h, 7d, or 2w", value)
		}
	case VirtualFolderFieldFileCount:
		if !slices.Contains(comparisonOperators, condition.Operator) {
			return fmt.Errorf("operator %q is not valid for file count", condition.Operator)
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("file count must be a non-negative whole number")
		}
	case VirtualFolderFieldProtocol:
		if condition.Operator != VirtualFolderOperatorEquals && condition.Operator != VirtualFolderOperatorNotEquals {
			return fmt.Errorf("operator %q is not valid for protocol", condition.Operator)
		}
		if value != "torrent" && value != "nzb" {
			return fmt.Errorf("protocol must be %q or %q", "torrent", "nzb")
		}
	case VirtualFolderFieldProvider:
		if condition.Operator != VirtualFolderOperatorEquals && condition.Operator != VirtualFolderOperatorNotEquals {
			return fmt.Errorf("operator %q is not valid for provider", condition.Operator)
		}
	default:
		return fmt.Errorf("field %q is not supported", condition.Field)
	}

	return nil
}

func parseVirtualFolderDuration(value string) (time.Duration, error) {
	matches := virtualFolderDurationPattern.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("invalid duration")
	}
	var total time.Duration
	if matches[1] != "" {
		weeks, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return 0, err
		}
		total += time.Duration(weeks) * 7 * 24 * time.Hour
	}
	if matches[2] != "" {
		days, err := strconv.ParseInt(matches[2], 10, 64)
		if err != nil {
			return 0, err
		}
		total += time.Duration(days) * 24 * time.Hour
	}
	if matches[3] != "" {
		remainder, err := time.ParseDuration(matches[3])
		if err != nil {
			return 0, err
		}
		total += remainder
	}
	if total == 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	return total, nil
}
