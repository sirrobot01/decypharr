package reacquire

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type arrHandler struct {
	arrs        *arr.Service
	invalidator Invalidator
}

type Invalidator interface {
	InvalidateReacquire(context.Context, Job) error
}

func NewHandler(arrs *arr.Service, invalidators ...Invalidator) Handler {
	var invalidator Invalidator
	if len(invalidators) > 0 {
		invalidator = invalidators[0]
	}
	return &arrHandler{arrs: arrs, invalidator: invalidator}
}

func (handler *arrHandler) Reacquire(ctx context.Context, job Job, progress JobProgress) error {
	if handler == nil || handler.arrs == nil {
		return fmt.Errorf("arr reacquirer is not configured")
	}
	instance, ok := handler.arrs.Get(job.ArrName)
	if !ok {
		return fmt.Errorf("arr %q is not configured", job.ArrName)
	}
	bindings, err := mutationBindings(job)
	if err != nil {
		return err
	}
	if err := validateMutationInstance(instance, bindings); err != nil {
		return err
	}
	if err := validateSearchBindings(instance, bindings); err != nil {
		return err
	}
	downloadConfig, err := handler.arrs.DownloadClientConfig(ctx, instance.Name)
	if err != nil {
		return err
	}
	if !downloadConfig.EnableCompletedDownloadHandling {
		return fmt.Errorf("arr completed download handling is disabled")
	}

	var failurePlan exactDownloadFailure
	if job.Strategy == StrategyHistoryFailed || job.Strategy == StrategyInteractiveBest {
		failurePlan, err = handler.prepareExactDownloadFailure(ctx, instance, job.DownloadID)
		if err != nil {
			return err
		}
	}
	if job.Strategy == StrategyInteractiveBest {
		if !failurePlan.grabFound {
			return fmt.Errorf("interactive reacquisition requires exact grab history")
		}
		if autoRedownloadsFailure(downloadConfig, failurePlan) {
			return fmt.Errorf("interactive reacquisition requires automatic failed-download redownload to be disabled")
		}
	}

	if err := progress.Update(StatusInvalidating, nil); err != nil {
		return err
	}
	if err := handler.deleteArrFiles(ctx, instance, bindings); err != nil {
		return err
	}
	var waitingStatus Status
	switch job.Strategy {
	case StrategyHistoryFailed:
		waitingStatus, err = handler.failHistory(ctx, instance, &job, bindings, failurePlan, downloadConfig, progress)
	case StrategyCommandSearch:
		waitingStatus, err = handler.searchBindings(ctx, instance, &job, bindings, progress)
	case StrategyInteractiveBest:
		if err = handler.executeExactDownloadFailure(ctx, instance, &job, failurePlan, progress); err == nil {
			waitingStatus, err = handler.grabBestRelease(ctx, instance, &job, bindings, progress)
		}
	default:
		return fmt.Errorf("unsupported reacquire strategy %q", job.Strategy)
	}
	if err != nil {
		return err
	}
	if handler.invalidator != nil {
		invalidationJob := job
		invalidationJob.Bindings = bindings
		if err := handler.invalidator.InvalidateReacquire(ctx, invalidationJob); err != nil {
			return err
		}
	}
	return progress.Update(waitingStatus, nil)
}

func (handler *arrHandler) failHistory(
	ctx context.Context,
	instance arr.Arr,
	job *Job,
	bindings []Binding,
	failure exactDownloadFailure,
	config arr.DownloadClientConfig,
	progress JobProgress,
) (Status, error) {
	if !failure.found {
		return handler.searchBindings(ctx, instance, job, bindings, progress)
	}
	if err := handler.executeExactDownloadFailure(ctx, instance, job, failure, progress); err != nil {
		return "", err
	}
	if failure.alreadyFailed {
		mutation, _ := mutationForKey(*job, mutationKey(MutationHistoryFailed, failure.downloadID))
		if mutation.Attempts == 0 {
			return handler.searchBindings(ctx, instance, job, bindings, progress)
		}
	}
	if !autoRedownloadsFailure(config, failure) {
		return handler.searchBindings(ctx, instance, job, bindings, progress)
	}
	return StatusWaitingForGrab, nil
}

type exactDownloadFailure struct {
	found         bool
	grabFound     bool
	alreadyFailed bool
	downloadID    string
	historyID     int
	failedID      int
	grabRecord    arr.HistoryRecord
}

func (handler *arrHandler) prepareExactDownloadFailure(ctx context.Context, instance arr.Arr, downloadID string) (exactDownloadFailure, error) {
	if downloadID == "" {
		return exactDownloadFailure{}, nil
	}
	failedRecord, alreadyFailed, err := handler.arrs.FailedHistory(ctx, instance.Name, downloadID)
	if err != nil {
		return exactDownloadFailure{}, err
	}
	record, found, err := handler.arrs.GrabHistory(ctx, instance.Name, downloadID)
	if err != nil {
		return exactDownloadFailure{}, err
	}
	if alreadyFailed {
		return exactDownloadFailure{
			found:         true,
			grabFound:     found,
			alreadyFailed: true,
			downloadID:    downloadID,
			failedID:      failedRecord.ID,
			grabRecord:    record,
		}, nil
	}
	return exactDownloadFailure{
		found:      found,
		grabFound:  found,
		downloadID: downloadID,
		historyID:  record.ID,
		grabRecord: record,
	}, nil
}

func autoRedownloadsFailure(config arr.DownloadClientConfig, failure exactDownloadFailure) bool {
	if !config.AutoRedownloadFailed {
		return false
	}
	releaseSource, _ := failure.grabRecord.DataValue("releaseSource")
	return !strings.EqualFold(releaseSource, "InteractiveSearch") || config.AutoRedownloadFailedFromInteractiveSearch
}

func (handler *arrHandler) executeExactDownloadFailure(
	ctx context.Context,
	instance arr.Arr,
	job *Job,
	failure exactDownloadFailure,
	progress JobProgress,
) error {
	if !failure.found {
		return nil
	}
	if err := progress.Update(StatusBlocklisting, nil); err != nil {
		return err
	}
	mutation := Mutation{
		Key:        mutationKey(MutationHistoryFailed, failure.downloadID),
		Kind:       MutationHistoryFailed,
		DownloadID: failure.downloadID,
		HistoryID:  failure.historyID,
	}
	mutation, err := ensureMutationIntent(job, progress, StatusBlocklisting, mutation)
	if err != nil {
		return err
	}
	if mutation.State == MutationConfirmed {
		return nil
	}
	if failure.alreadyFailed {
		return confirmMutation(job, progress, StatusBlocklisting, mutation, failure.failedID)
	}
	if mutation.Attempts > 0 {
		record, found, err := handler.arrs.FailedHistory(ctx, instance.Name, failure.downloadID)
		if err != nil {
			return unavailableMutationReconciliation(mutation, fmt.Errorf("reconcile failed-download history: %w", err))
		}
		if found {
			return confirmMutation(job, progress, StatusBlocklisting, mutation, record.ID)
		}
		if err := mutationRedispatchError(mutation); err != nil {
			return err
		}
	}
	mutation, err = recordMutationAttempt(job, progress, StatusBlocklisting, mutation)
	if err != nil {
		return err
	}
	if err := handler.arrs.FailHistory(ctx, instance.Name, mutation.HistoryID); err != nil {
		if !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
			return err
		}
		record, found, reconcileErr := handler.arrs.FailedHistory(ctx, instance.Name, failure.downloadID)
		if reconcileErr == nil && found {
			return confirmMutation(job, progress, StatusBlocklisting, mutation, record.ID)
		}
		return unresolvedMutation(mutation, err, reconcileErr)
	}
	return confirmMutation(job, progress, StatusBlocklisting, mutation, mutation.HistoryID)
}

func mutationBindings(job Job) ([]Binding, error) {
	bindings := make([]Binding, 0, len(job.Bindings))
	requestedFound := false
	for _, binding := range job.Bindings {
		if binding.ArrName != job.ArrName || binding.ArrType != job.ArrType || !binding.AuthorizesMutation() {
			continue
		}
		if binding.EntryID == job.EntryID && binding.EntryFileID == job.FileID {
			requestedFound = true
		}
		bindings = append(bindings, binding)
	}
	if !requestedFound {
		return nil, fmt.Errorf("requested Arr binding no longer authorizes mutation")
	}
	return bindings, nil
}

func (handler *arrHandler) deleteArrFiles(ctx context.Context, instance arr.Arr, bindings []Binding) error {
	byFileID := make(map[int]Binding, len(bindings))
	for _, binding := range bindings {
		if binding.ArrFileID <= 0 {
			continue
		}
		byFileID[binding.ArrFileID] = binding
	}

	present := make([]int, 0, len(byFileID))
	for fileID, binding := range byFileID {
		current, found, err := handler.arrs.LibraryFile(ctx, instance.Name, binding.ArrFileID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if !bindingMatchesManagedFile(binding, current) {
			return fmt.Errorf("arr file %d no longer matches the indexed media", binding.ArrFileID)
		}
		present = append(present, fileID)
	}

	slices.Sort(present)
	for _, fileID := range present {
		if err := handler.arrs.DeleteLibraryFile(ctx, instance.Name, fileID); err != nil {
			return err
		}
	}
	return nil
}

func bindingMatchesManagedFile(binding Binding, current arr.LibraryFile) bool {
	if binding.ArrFileID != current.ArrFileID || !sameLibraryPath(binding.LibraryPath, current.Path) {
		return false
	}
	switch binding.ArrType {
	case arr.Sonarr:
		return binding.SeriesID > 0 &&
			binding.SeriesID == current.SeriesID &&
			binding.SeasonNumber == current.SeasonNumber &&
			slices.Equal(binding.EpisodeIDs, current.EpisodeIDs)
	case arr.Radarr:
		return binding.MovieID > 0 && binding.MovieID == current.MovieID
	default:
		return false
	}
}

func validateSearchBindings(instance arr.Arr, bindings []Binding) error {
	switch instance.Type {
	case arr.Sonarr:
		episodeIDs, seriesID, _ := sonarrTargets(bindings)
		if len(episodeIDs) == 0 && seriesID <= 0 {
			return fmt.Errorf("sonarr binding has no episode or series target")
		}
	case arr.Radarr:
		if len(movieTargets(bindings)) == 0 {
			return fmt.Errorf("radarr binding has no movie target")
		}
	default:
		return fmt.Errorf("search unsupported for arr type %q", instance.Type)
	}
	return nil
}

func idListKey(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func sonarrTargets(bindings []Binding) ([]int, int, int) {
	var episodeIDs []int
	seriesID := 0
	seasonNumber := 0
	for _, binding := range bindings {
		if seriesID == 0 && binding.SeriesID > 0 {
			seriesID = binding.SeriesID
			seasonNumber = binding.SeasonNumber
		}
		episodeIDs = append(episodeIDs, binding.EpisodeIDs...)
	}
	slices.Sort(episodeIDs)
	return slices.Compact(episodeIDs), seriesID, seasonNumber
}

func movieTargets(bindings []Binding) []int {
	movieIDs := make([]int, 0, len(bindings))
	for _, binding := range bindings {
		if binding.MovieID > 0 {
			movieIDs = append(movieIDs, binding.MovieID)
		}
	}
	slices.Sort(movieIDs)
	return slices.Compact(movieIDs)
}
