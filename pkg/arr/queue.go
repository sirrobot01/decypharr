package arr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
)

type QueueAction string

const (
	QueueActionNone              QueueAction = ""
	QueueActionImport            QueueAction = "import"
	QueueActionBlocklist         QueueAction = "blacklist"
	QueueActionBlocklistResearch QueueAction = "blacklist_research"
)

type QueueResponseScheme struct {
	Page          int           `json:"page"`
	PageSize      int           `json:"pageSize"`
	SortKey       string        `json:"sortKey"`
	SortDirection string        `json:"sortDirection"`
	TotalRecords  int           `json:"totalRecords"`
	Records       []QueueSchema `json:"records"`
}

type QueueSchema struct {
	SeriesId              int    `json:"seriesId"`
	EpisodeId             int    `json:"episodeId"`
	SeasonNumber          int    `json:"seasonNumber"`
	Title                 string `json:"title"`
	Status                string `json:"status"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	TrackedDownloadState  string `json:"trackedDownloadState"`
	StatusMessages        []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
	DownloadId                          string `json:"downloadId"`
	Protocol                            string `json:"protocol"`
	DownloadClient                      string `json:"downloadClient"`
	DownloadClientHasPostImportCategory bool   `json:"downloadClientHasPostImportCategory"`
	Indexer                             string `json:"indexer"`
	OutputPath                          string `json:"outputPath"`
	EpisodeHasFile                      bool   `json:"episodeHasFile"`
	Id                                  int    `json:"id"`
}

// catalogMatchers are the predicates for the built-in cleanup rules, keyed by
// config.QueueCleanupRule.ID. text is the lowercased join of a queue item's
// status message titles and messages.
var catalogMatchers = map[string]func(item QueueSchema, text string) bool{
	"failed_download": func(item QueueSchema, _ string) bool {
		return strings.EqualFold(item.Status, "failed")
	},
	"title_mismatch": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "title mismatch")
	},
	"matched_by_id": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "matched to") && strings.Contains(text, "by id")
	},
	"unable_to_parse": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "unable to parse download")
	},
	"no_eligible_files": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "no files found are eligible")
	},
	"episodes_missing": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "not imported or missing from the release")
	},
	"file_empty": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "file is empty")
	},
	"invalid_local_path": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "is not a valid local path")
	},
	"not_grabbed": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "not in a category")
	},
}

func (s *Service) Queue(ctx context.Context, name string) ([]QueueSchema, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}

	items := make([]QueueSchema, 0)
	for page := 1; ; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {"200"}}
		var response QueueResponseScheme
		resp, err := s.get(ctx, instance, "api/v3/queue?"+query.Encode(), &response)
		if err != nil {
			return items, err
		}
		if err := expectStatus(resp, http.StatusOK); err != nil {
			return items, err
		}

		items = append(items, response.Records...)
		if len(response.Records) == 0 || len(items) >= response.TotalRecords {
			return items, nil
		}
	}
}

// CleanupQueue applies the configured cleanup rules to one instance's queue.
func (s *Service) CleanupQueue(ctx context.Context, name string) error {
	items, err := s.Queue(ctx, name)
	if err != nil {
		return err
	}

	rules := config.Get().QueueCleanup.Rules
	var blocklist, blocklistResearch []int
	var manualImports []string
	for _, item := range items {
		switch resolveQueueAction(item, rules) {
		case QueueActionBlocklist:
			blocklist = append(blocklist, item.Id)
		case QueueActionBlocklistResearch:
			blocklistResearch = append(blocklistResearch, item.Id)
		case QueueActionImport:
			manualImports = append(manualImports, item.DownloadId)
		}
	}

	if len(blocklistResearch) > 0 {
		if err := s.removeQueueItems(ctx, name, blocklistResearch, false); err != nil {
			s.logger.Error().Err(err).Str("arr", name).Msg("Queue cleanup: blocklist and research failed")
		}
	}
	if len(blocklist) > 0 {
		if err := s.removeQueueItems(ctx, name, blocklist, true); err != nil {
			s.logger.Error().Err(err).Str("arr", name).Msg("Queue cleanup: blocklist failed")
		}
	}
	for _, downloadID := range manualImports {
		if err := s.ManualImport(ctx, name, downloadID); err != nil {
			s.logger.Error().Err(err).Str("arr", name).Msg("Queue cleanup: manual import failed")
		}
	}
	return nil
}

// resolveQueueAction picks what to do with one queue item. Only failed items
// and those flagged warning or error are considered, and the first matching
// rule wins.
func resolveQueueAction(item QueueSchema, rules []config.QueueCleanupRule) QueueAction {
	status := strings.ToLower(item.TrackedDownloadStatus)
	if !strings.EqualFold(item.Status, "failed") && status != "warning" && status != "error" {
		return QueueActionNone
	}

	var builder strings.Builder
	for _, message := range item.StatusMessages {
		builder.WriteString(message.Title)
		builder.WriteByte(' ')
		builder.WriteString(strings.Join(message.Messages, " "))
		builder.WriteByte(' ')
	}
	text := strings.ToLower(builder.String())

	for _, rule := range rules {
		matched := false
		if rule.ID != "" {
			if match, ok := catalogMatchers[rule.ID]; ok {
				matched = match(item, text)
			}
		} else if needle := strings.ToLower(strings.TrimSpace(rule.Match)); needle != "" {
			matched = strings.Contains(text, needle)
		}
		if !matched {
			continue
		}
		switch QueueAction(rule.Action) {
		case QueueActionImport, QueueActionBlocklist, QueueActionBlocklistResearch:
			return QueueAction(rule.Action)
		default:
			return QueueActionNone
		}
	}
	return QueueActionNone
}

// removeQueueItems blocklists and removes queue items. skipRedownload keeps the
// Arr from searching for a replacement.
func (s *Service) removeQueueItems(ctx context.Context, name string, ids []int, skipRedownload bool) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}
	query := url.Values{
		"removeFromClient": {"true"},
		"blocklist":        {"true"},
		"skipRedownload":   {strconv.FormatBool(skipRedownload)},
		"changeCategory":   {"false"},
	}
	payload := struct {
		Ids []int `json:"ids"`
	}{Ids: ids}

	resp, err := s.mutate(ctx, instance, http.MethodDelete, "api/v3/queue/bulk?"+query.Encode(), payload, nil)
	if err != nil {
		return fmt.Errorf("remove queue items: %w", err)
	}
	if err := expectSuccess(resp); err != nil {
		return fmt.Errorf("remove queue items: %w", err)
	}
	return nil
}
