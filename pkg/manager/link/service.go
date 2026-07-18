package link

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"golang.org/x/sync/singleflight"
)

const (
	MaxReinsertionAttempt = 3
)

var (
	emptyDownloadLink = types.DownloadLink{}
)

// EntryRefresher is a function that refreshes an entry by infohash
type EntryRefresher func(infohash string) (*storage.Entry, error)
type EntryRepairer func(ctx context.Context, entry *storage.Entry) error
type EntrySaver func(entry *storage.Entry) error

// Service handles download link fetching and validation.
// It uses the account-level cache for storing links and only tracks validation state.
type Service struct {
	validated      *xsync.Map[string, error]
	singleflight   singleflight.Group
	clients        *xsync.Map[string, debrid.Client]
	entryRefresher EntryRefresher
	repairer       EntryRepairer
	entrySaver     EntrySaver
	httpClient     *http.Client
	retries        int
	logger         zerolog.Logger
}

// New creates a new LinkService
func New(
	clients *xsync.Map[string, debrid.Client],
	entryRefresher EntryRefresher,
	entryReinsert EntryRepairer,
	entrySaver EntrySaver,
	httpClient *http.Client,
	retries int,
	logger zerolog.Logger,
) *Service {
	return &Service{
		validated:      xsync.NewMap[string, error](),
		clients:        clients,
		entryRefresher: entryRefresher,
		repairer:       entryReinsert,
		entrySaver:     entrySaver,
		httpClient:     httpClient,
		retries:        retries,
		logger:         logger,
	}
}

// GetLink fetches and validates a download link for a file in an entry.
// Links are cached at the account level; this service only tracks validation state.
func (s *Service) GetLink(ctx context.Context, entry *storage.Entry, filename string) (types.DownloadLink, error) {
	// Use singleflight to deduplicate concurrent requests for the same file
	key := entry.InfoHash + ":" + filename
	v, err, _ := s.singleflight.Do(key, func() (any, error) {
		return s.fetchAndValidate(ctx, entry, filename, 0)
	})

	if err != nil {
		return emptyDownloadLink, err
	}

	return v.(types.DownloadLink), nil
}

func (s *Service) getClient(provider string) (debrid.Client, error) {
	c, ok := s.clients.Load(provider)
	if !ok {
		return nil, fmt.Errorf("client for provider %s not found", provider)
	}
	return c, nil
}

// fetchAndValidate fetches a download link and validates it.
// attempt tracks how many re-insertion cycles we've already paid for during
// this GetLink call so we can bail out instead of looping forever when the
// underlying file never resolves (see fetchLink/handleBadLink).
func (s *Service) fetchAndValidate(ctx context.Context, entry *storage.Entry, filename string, attempt int) (types.DownloadLink, error) {
	if err := ctx.Err(); err != nil {
		return emptyDownloadLink, err
	}
	link, err := s.fetchLink(ctx, entry, filename, attempt)
	if err != nil {
		return s.handleBadLink(ctx, err, entry, link, attempt)
	}

	// Is link already validated
	// Check if we've already validated this link
	if validationErr, exists := s.validated.Load(link.DownloadLink); exists {
		if validationErr == nil {
			// Re-fetch if the cached CDN URL has passed its declared expiry.
			// Without this check a 3-hour TorBox CDN URL would be served from
			// s.validated forever — bypassing the HEAD validation that would
			// otherwise catch the expired URL.
			if !link.ExpiresAt.IsZero() && time.Now().After(link.ExpiresAt) {
				return s.invalidateAndRefetch(ctx, entry, link, attempt)
			}
			return link, nil // Already validated successfully
		}
		// Previous validation failed - check if we should retry
		if linkErr := GetLinkError(validationErr); linkErr != nil {
			if linkErr.ShouldRefetch() {
				// Invalidate and refetch
				return s.invalidateAndRefetch(ctx, entry, link, attempt)
			}
		}
		return emptyDownloadLink, validationErr
	}

	// Validate the link
	validationErr := s.validateLink(ctx, &link)

	if validationErr != nil {
		// Handle link error categories
		if linkErr := GetLinkError(validationErr); linkErr != nil {
			if linkErr.ShouldDisableAccount() {
				if err := s.disableLinkAccount(link, linkErr); err != nil {
					s.logger.Error().
						Err(err).
						Str("debrid", link.Debrid).
						Str("token", utils.Mask(link.Token)).
						Str("reason", linkErr.Code).
						Msg("Failed to disable account after link error")
				} else {
					// This will use the next available account and fetch a new link, so we need to refetch and revalidate.
					// Account swap doesn't consume a re-insertion attempt.
					return s.fetchAndValidate(ctx, entry, filename, attempt)
				}
			} else if linkErr.ShouldRefetch() {
				// Invalidate and refetch
				return s.invalidateAndRefetch(ctx, entry, link, attempt)
			}
		}
	}

	// Only cache permanent/refetchable failures and successes.
	// Retryable errors (502, 503, 429) must not be cached: a single transient
	// failure would block all future reads for the file until the cache is
	// explicitly cleared (account-disable or restart).
	if linkErr := GetLinkError(validationErr); linkErr == nil || !linkErr.ShouldRetry() {
		s.validated.Store(link.DownloadLink, validationErr)
	}

	if validationErr == nil {
		return link, nil
	}
	return emptyDownloadLink, validationErr
}

func (s *Service) handleBadLink(ctx context.Context, err error, entry *storage.Entry, dl types.DownloadLink, attempt int) (types.DownloadLink, error) {
	if errors.Is(err, customerror.HosterUnavailableError) {
		if entry.Bad {
			return emptyDownloadLink, fmt.Errorf("can't repair %s since it's been marked as bad", entry.GetFolder())
		}
		if attempt >= MaxReinsertionAttempt {
			s.markEntryBad(entry, dl.Filename, attempt, "hoster_unavailable")
			return emptyDownloadLink, fmt.Errorf("entry %s file %s still unresolvable after %d re-insertion attempts", entry.GetFolder(), dl.Filename, attempt)
		}
		if err := s.repairer(ctx, entry); err != nil {
			return emptyDownloadLink, err
		}

		if entry.Bad {
			// Entry is still bad
			return emptyDownloadLink, fmt.Errorf("entry %s(%s) still bad after repair, un-repairable", entry.GetFolder(), dl.Link)
		}
		// Bypass singleflight re-entry to avoid deadlock
		return s.fetchAndValidate(ctx, entry, dl.Filename, attempt+1)
	}
	// Just return the error
	return dl, err
}

// markEntryBad sets entry.Bad and persists it so subsequent GetLink calls
// for the same entry short-circuit instead of triggering another re-insertion
// cycle. Logged once per call.
func (s *Service) markEntryBad(entry *storage.Entry, filename string, attempt int, reason string) {
	entry.Bad = true
	if s.entrySaver != nil {
		if err := s.entrySaver(entry); err != nil {
			s.logger.Warn().
				Err(err).
				Str("infohash", entry.InfoHash).
				Msg("Failed to persist Bad flag after exhausting re-insertion attempts")
		}
	}
	s.logger.Warn().
		Str("infohash", entry.InfoHash).
		Str("name", entry.Name).
		Str("filename", filename).
		Int("attempts", attempt).
		Str("reason", reason).
		Msg("Giving up on entry after repeated failed re-insertions")
}

// fetchLink fetches a download link from the debrid provider (via account cache)
func (s *Service) fetchLink(ctx context.Context, entry *storage.Entry, filename string, attempt int) (types.DownloadLink, error) {
	file, err := entry.GetFile(filename)
	if err != nil {
		return emptyDownloadLink, NewPermanentError(
			fmt.Errorf("file %s not found in entry %s: %w", filename, entry.Name, err),
			"file_not_found",
		)
	}

	placementFile, err := s.getPlacementFile(entry, filename)
	if err != nil {
		return emptyDownloadLink, err
	}

	if placementFile.Link == "" && placementFile.Id == "" {
		return emptyDownloadLink, NewPermanentError(
			fmt.Errorf("file link is missing for %s in entry %s", filename, entry.Name),
			"link_missing",
		)
	}

	client, err := s.getClient(entry.ActiveProvider)
	if err != nil {
		return emptyDownloadLink, NewPermanentError(
			fmt.Errorf("debrid client not found: %s", entry.ActiveProvider),
			"client_not_found",
		)
	}

	placement := entry.Providers[entry.ActiveProvider]
	if placement == nil {
		return emptyDownloadLink, NewPermanentError(
			fmt.Errorf("no placement found for debrid %s with infohash %s", entry.ActiveProvider, entry.InfoHash),
			"placement_not_found",
		)
	}

	debridFile := &types.File{
		Id:        placementFile.Id,
		Link:      placementFile.Link,
		Path:      placementFile.Path,
		Name:      file.Name,
		Size:      file.Size,
		ByteRange: file.ByteRange,
		Deleted:   file.Deleted,
	}

	// This uses account-level caching internally
	downloadLink, err := client.GetDownloadLink(placement.ID, debridFile)
	if err != nil {
		return downloadLink, err
	}

	if downloadLink.Empty() {
		// Let's try to reinsert the entry
		if entry.Bad {
			return emptyDownloadLink, fmt.Errorf("can't repair %s since it's been marked as bad", entry.GetFolder())
		}
		if attempt >= MaxReinsertionAttempt {
			s.markEntryBad(entry, filename, attempt, "empty_link")
			return emptyDownloadLink, fmt.Errorf("entry %s file %s still resolves to an empty link after %d re-insertion attempts", entry.GetFolder(), filename, attempt)
		}
		if err := s.repairer(ctx, entry); err != nil {
			return emptyDownloadLink, err
		}

		if entry.Bad {
			// Entry is still bad
			return emptyDownloadLink, fmt.Errorf("entry %s(%s) still bad after repair, un-repairable", entry.GetFolder(), downloadLink.Link)
		}
		// Bypass singleflight re-entry to avoid deadlock
		return s.fetchAndValidate(ctx, entry, filename, attempt+1)
	}

	return downloadLink, nil
}

// getPlacementFile retrieves the placement file with refresh fallback
func (s *Service) getPlacementFile(entry *storage.Entry, filename string) (*storage.ProviderFile, error) {
	_, ok := entry.Files[filename]
	if !ok {
		return nil, NewPermanentError(
			fmt.Errorf("file %s not found in entry", filename),
			"file_not_found",
		)
	}

	placement := entry.Providers[entry.ActiveProvider]
	if placement == nil {
		return nil, NewPermanentError(
			fmt.Errorf("no placement found for debrid %s with infohash %s", entry.ActiveProvider, entry.InfoHash),
			"placement_not_found",
		)
	}

	placementFile := placement.Files[filename]
	if placementFile == nil || (placementFile.Link == "" && placementFile.Id == "") {
		if s.entryRefresher == nil {
			return nil, NewPermanentError(
				fmt.Errorf("file %s not available and no refresher configured", filename),
				"no_refresher",
			)
		}

		refreshed, err := s.entryRefresher(entry.InfoHash)
		if err != nil {
			return nil, NewRefetchableError(
				fmt.Errorf("failed to refresh entry: %w", err),
				"refresh_failed",
			)
		}

		file := refreshed.Files[filename]
		if file == nil {
			return nil, NewPermanentError(
				fmt.Errorf("file disappeared after refresh"),
				"file_disappeared",
			)
		}

		placement = refreshed.Providers[entry.ActiveProvider]
		if placement == nil {
			return nil, NewPermanentError(
				fmt.Errorf("placement disappeared after refresh for debrid %s", entry.ActiveProvider),
				"placement_disappeared",
			)
		}

		placementFile = placement.Files[filename]
		if placementFile == nil || (placementFile.Link == "" && placementFile.Id == "") {
			return nil, NewPermanentError(
				fmt.Errorf("file %s not available after refresh", filename),
				"file_not_available",
			)
		}

		*entry = *refreshed
	}

	return placementFile, nil
}

// validateLink validates a download link by making a HEAD request
func (s *Service) validateLink(ctx context.Context, link *types.DownloadLink) error {
	if link == nil {
		return NewPermanentError(ErrEmptyLink, "empty_link")
	}
	if link.Empty() {
		return NewPermanentError(fmt.Errorf("download url is empty for %s||%s", link.Filename, link.Link), "empty_link")
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", link.DownloadLink, nil)
	if err != nil {
		return NewPermanentError(
			fmt.Errorf("failed to create HEAD request: %w", err),
			"request_creation_failed",
		)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return NewRetryableError(
			fmt.Errorf("HEAD request failed: %w", err),
			"network_error",
		)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	errorCode := resp.Header.Get("X-Error")
	if errorCode == "" {
		errorCode = strconv.Itoa(resp.StatusCode)
	}

	return ErrorCodeToLinkError(errorCode)
}

// disableLinkAccount handles errors that require disabling an account
func (s *Service) disableLinkAccount(link types.DownloadLink, linkErr *Error) error {
	client, err := s.getClient(link.Debrid)
	if err != nil {
		return fmt.Errorf("failed to get client for debrid %s: %w", link.Debrid, err)
	}

	accountManager := client.AccountManager()
	account, err := accountManager.GetAccount(link.Token)
	if err != nil {
		return fmt.Errorf("failed to get account for token %s: %w", utils.Mask(link.Token), err)
	}

	if account == nil {
		return fmt.Errorf("account not found for token %s", utils.Mask(link.Token))
	}

	accountManager.Disable(account)

	// Remove all validations for all the links
	s.validated.Clear()
	s.logger.Warn().
		Str("debrid", link.Debrid).
		Str("token", utils.Mask(account.Token)).
		Str("account", utils.Mask(account.Username)).
		Str("reason", linkErr.Code).
		Msg("Disabled account due to error")
	return nil
}

// invalidateAndRefetch removes a link from both validation tracking and account cache
func (s *Service) invalidateAndRefetch(ctx context.Context, entry *storage.Entry, link types.DownloadLink, attempt int) (types.DownloadLink, error) {
	// Remove from validation tracking
	s.validated.Delete(link.DownloadLink)

	// Remove from account cache
	if link.Debrid == "" {
		return emptyDownloadLink, fmt.Errorf("invalid link")
	}

	client, err := s.getClient(link.Debrid)
	if err != nil {
		return emptyDownloadLink, err
	}

	_ = client.DeleteLink(link) // This might fail, doesnt matter

	return s.fetchLink(ctx, entry, link.Filename, attempt)
}

// Clear removes all validation tracking entries
func (s *Service) Clear() {
	s.validated.Clear()
}
