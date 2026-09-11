package reacquire

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func (handler *arrHandler) grabBestRelease(
	ctx context.Context,
	instance arr.Arr,
	job *Job,
	bindings []Binding,
	progress JobProgress,
) (Status, error) {
	if err := progress.Update(StatusSearching, nil); err != nil {
		return "", err
	}

	if mutation, found := mutationOfKind(*job, MutationReleaseGrab); found {
		if mutation.State == MutationConfirmed {
			return StatusWaitingForDownload, nil
		}
		if mutation.Attempts > 0 {
			record, found, err := handler.reconcileReleaseMutation(ctx, instance, mutation)
			if err != nil {
				return "", unavailableMutationReconciliation(mutation, err)
			}
			if found {
				if err := confirmMutation(job, progress, StatusSearching, mutation, record.ID); err != nil {
					return "", err
				}
				return StatusWaitingForDownload, nil
			}
			if err := mutationRedispatchError(mutation); err != nil {
				return "", err
			}
		}
		release, err := handler.findPersistedRelease(ctx, instance, bindings, mutation)
		if err != nil {
			return "", err
		}
		return handler.dispatchReleaseMutation(ctx, instance, job, mutation, release, progress)
	}

	releases, err := handler.searchReplacementReleases(ctx, instance, bindings)
	if err != nil {
		return "", err
	}
	for _, release := range releases {
		if !releaseEligible(release) || release.GUID == "" || release.Indexer == "" || release.IndexerID <= 0 {
			continue
		}
		mutation, err := releaseMutation(bindings, release)
		if err != nil {
			return "", err
		}
		mutation, err = ensureMutationIntent(job, progress, StatusSearching, mutation)
		if err != nil {
			return "", err
		}
		return handler.dispatchReleaseMutation(ctx, instance, job, mutation, release, progress)
	}
	return "", fmt.Errorf("arr returned no downloadable replacement release with reconcilable identity")
}

func (handler *arrHandler) searchReplacementReleases(ctx context.Context, instance arr.Arr, bindings []Binding) ([]arr.Release, error) {
	var (
		releases []arr.Release
		err      error
	)
	switch instance.Type {
	case arr.Sonarr:
		episodeIDs, seriesID, seasonNumber := sonarrTargets(bindings)
		if len(episodeIDs) == 1 {
			releases, err = handler.arrs.EpisodeReleases(ctx, instance.Name, episodeIDs[0])
		} else {
			if !singleSonarrSeason(bindings, seriesID, seasonNumber) {
				return nil, fmt.Errorf("interactive Sonarr reacquisition spans multiple seasons")
			}
			releases, err = handler.arrs.SeasonReleases(ctx, instance.Name, seriesID, seasonNumber)
		}
	case arr.Radarr:
		movieIDs := movieTargets(bindings)
		if len(movieIDs) == 0 {
			return nil, fmt.Errorf("movie binding has no movie ID")
		}
		releases, err = handler.arrs.MovieReleases(ctx, instance.Name, movieIDs[0])
	default:
		return nil, fmt.Errorf("interactive search unsupported for arr type %q", instance.Type)
	}
	if err != nil {
		return nil, err
	}
	return releases, nil
}

func releaseMutation(bindings []Binding, release arr.Release) (Mutation, error) {
	if len(bindings) == 0 {
		return Mutation{}, fmt.Errorf("release mutation requires Arr bindings")
	}
	mutation := Mutation{
		Key:              mutationKey(MutationReleaseGrab, release.GUID, strconv.Itoa(release.IndexerID)),
		Kind:             MutationReleaseGrab,
		ReleaseGUID:      release.GUID,
		ReleaseIndexer:   release.Indexer,
		ReleaseIndexerID: release.IndexerID,
	}
	switch bindings[0].ArrType {
	case arr.Sonarr:
		mutation.EpisodeIDs, mutation.SeriesID, mutation.SeasonNumber = sonarrTargets(bindings)
		if len(mutation.EpisodeIDs) == 0 {
			return Mutation{}, fmt.Errorf("interactive Sonarr reacquisition cannot be reconciled without episode IDs")
		}
	case arr.Radarr:
		movieIDs := movieTargets(bindings)
		if len(movieIDs) == 0 {
			return Mutation{}, fmt.Errorf("interactive Radarr reacquisition cannot be reconciled without a movie ID")
		}
		mutation.MovieIDs = []int{movieIDs[0]}
	default:
		return Mutation{}, fmt.Errorf("interactive search unsupported for arr type %q", bindings[0].ArrType)
	}
	return mutation, nil
}

func (handler *arrHandler) findPersistedRelease(
	ctx context.Context,
	instance arr.Arr,
	bindings []Binding,
	mutation Mutation,
) (arr.Release, error) {
	releases, err := handler.searchReplacementReleases(ctx, instance, bindings)
	if err != nil {
		return arr.Release{}, err
	}
	for _, release := range releases {
		if release.GUID == mutation.ReleaseGUID &&
			release.Indexer == mutation.ReleaseIndexer &&
			release.IndexerID == mutation.ReleaseIndexerID &&
			releaseEligible(release) {
			return release, nil
		}
	}
	return arr.Release{}, fmt.Errorf("previously selected Arr release is no longer available; refusing to substitute another release")
}

func (handler *arrHandler) dispatchReleaseMutation(
	ctx context.Context,
	instance arr.Arr,
	job *Job,
	mutation Mutation,
	release arr.Release,
	progress JobProgress,
) (Status, error) {
	mutation, err := recordMutationAttempt(job, progress, StatusSearching, mutation)
	if err != nil {
		return "", err
	}
	if err := handler.arrs.GrabRelease(ctx, instance.Name, release); err != nil {
		if !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
			return "", err
		}
		record, found, reconcileErr := handler.reconcileReleaseMutation(ctx, instance, mutation)
		if reconcileErr == nil && found {
			if err := confirmMutation(job, progress, StatusSearching, mutation, record.ID); err != nil {
				return "", err
			}
			return StatusWaitingForDownload, nil
		}
		return "", unresolvedMutation(mutation, err, reconcileErr)
	}
	if err := confirmMutation(job, progress, StatusSearching, mutation, 0); err != nil {
		return "", err
	}
	return StatusWaitingForDownload, nil
}

func (handler *arrHandler) reconcileReleaseMutation(
	ctx context.Context,
	instance arr.Arr,
	mutation Mutation,
) (arr.HistoryRecord, bool, error) {
	records, err := handler.arrs.GrabHistorySince(ctx, instance.Name, mutation.LastDispatchedAt.Add(-mutationClockSkew))
	if err != nil {
		return arr.HistoryRecord{}, false, fmt.Errorf("reconcile release grab: %w", err)
	}
	record, found := findGrabReceipt(records, mutation)
	return record, found, nil
}

func singleSonarrSeason(bindings []Binding, seriesID, seasonNumber int) bool {
	if seriesID <= 0 {
		return false
	}
	for _, binding := range bindings {
		if binding.SeriesID != seriesID || binding.SeasonNumber != seasonNumber {
			return false
		}
	}
	return true
}

func releaseEligible(release arr.Release) bool {
	return !release.Rejected && !release.TemporarilyRejected && len(release.Rejections) == 0 && (release.DownloadAllowed || release.Approved)
}
