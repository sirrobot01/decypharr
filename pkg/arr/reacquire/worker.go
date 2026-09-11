package reacquire

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"

	"github.com/google/uuid"
)

const (
	maintenanceInterval = time.Minute
	executionTimeout    = 2 * time.Minute
	waitingTimeout      = 6 * time.Hour
	successRetention    = 24 * time.Hour
	failureRetention    = 7 * 24 * time.Hour
	retryBaseDelay      = time.Second
	retryMaxDelay       = 30 * time.Second
)

func (s *Service) Reacquire(request Request) (*Job, error) {
	release, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer release()
	request.Strategy = request.Strategy.normalized()
	if err := request.validate(); err != nil {
		return nil, err
	}
	binding, ok := s.index.Lookup(request.EntryID, request.FileID)
	if !ok {
		return nil, fmt.Errorf("%w: entry %q file %q", ErrBindingNotFound, request.EntryID, request.FileID)
	}
	if !binding.AuthorizesMutation() {
		return nil, fmt.Errorf("%w: entry %q file %q", ErrBindingUnsafe, request.EntryID, request.FileID)
	}
	key := keyForBinding(binding)

	s.jobsMu.Lock()
	if id, exists := s.activeReacquisitions[key]; exists {
		job := cloneJob(s.jobs[id])
		s.jobsMu.Unlock()
		return &job, nil
	}

	bindings := s.index.ByDownloadID(binding.ArrName, binding.DownloadID)
	bindings = slices.DeleteFunc(bindings, func(binding Binding) bool {
		return !binding.AuthorizesMutation()
	})
	if len(bindings) == 0 {
		bindings = []Binding{binding}
	}
	now := s.now()
	job := Job{
		ID:         uuid.NewString(),
		Status:     StatusQueued,
		Cause:      request.Cause,
		Strategy:   request.Strategy,
		ArrName:    binding.ArrName,
		ArrType:    binding.ArrType,
		EntryID:    request.EntryID,
		FileID:     request.FileID,
		DownloadID: binding.DownloadID,
		Bindings:   bindings,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.jobRepository.Save(job); err != nil {
		s.jobsMu.Unlock()
		return nil, err
	}
	s.jobs[job.ID] = cloneJob(job)
	s.activeReacquisitions[key] = job.ID
	s.jobsMu.Unlock()

	s.signal()
	result := cloneJob(job)
	return &result, nil
}

func (s *Service) Jobs() []Job {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()

	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, cloneJob(job))
	}
	slices.SortFunc(jobs, func(left, right Job) int {
		if result := right.UpdatedAt.Compare(left.UpdatedAt); result != 0 {
			return result
		}
		return cmp.Compare(left.ID, right.ID)
	})
	return jobs
}

func (s *Service) Job(id string) (Job, bool) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	job, ok := s.jobs[id]
	return cloneJob(job), ok
}

// DeleteJobs removes completed job history from memory and the persisted job repository.
// Active jobs are rejected as a batch: deleting one would hide work that can
// still mutate an Arr instance, and deleting only part of a requested batch is
// surprising to callers.
func (s *Service) DeleteJobs(ids []string) (int, error) {
	release, err := s.beginOperation()
	if err != nil {
		return 0, err
	}
	defer release()

	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		job, exists := s.jobs[id]
		if !exists {
			continue
		}
		if !job.Status.Terminal() {
			return 0, fmt.Errorf("%w: job %q is %s", ErrJobNotTerminal, id, job.Status)
		}
		uniqueIDs = append(uniqueIDs, id)
	}

	deleted := 0
	for _, id := range uniqueIDs {
		if err := s.jobRepository.Delete(id); err != nil {
			return deleted, fmt.Errorf("delete reacquire job %q: %w", id, err)
		}
		delete(s.jobs, id)
		deleted++
	}
	return deleted, nil
}

func (s *Service) loadJobs(jobs []Job) error {
	slices.SortFunc(jobs, func(left, right Job) int {
		return left.UpdatedAt.Compare(right.UpdatedAt)
	})
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	clear(s.jobs)
	clear(s.activeReacquisitions)
	for _, job := range jobs {
		job.Strategy = job.Strategy.normalized()
		s.jobs[job.ID] = cloneJob(job)
		if job.Status.Terminal() {
			continue
		}
		key := keyForJob(job)
		if previousID, exists := s.activeReacquisitions[key]; exists {
			previous := s.jobs[previousID]
			previous.Status = StatusCancelled
			previous.LastError = "superseded by duplicate active job"
			previous.UpdatedAt = s.now()
			previous.CompletedAt = previous.UpdatedAt
			if err := s.jobRepository.Save(previous); err != nil {
				return err
			}
			s.jobs[previousID] = previous
		}
		s.activeReacquisitions[key] = job.ID
	}
	return nil
}

func (s *Service) run(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maintainJobs()
			s.signal()
		case <-s.wake:
			for {
				handler := s.currentHandler()
				if handler == nil {
					break
				}
				job, ok := s.nextJob()
				if !ok {
					break
				}
				if !s.runJob(ctx, handler, job) {
					break
				}
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}

func (s *Service) runJob(ctx context.Context, handler Handler, job Job) bool {
	started, err := s.updateJob(job.ID, StatusResolving, func(job *Job) {
		job.RetryAt = time.Time{}
	})
	if err != nil {
		return false
	}
	progress := &serviceJobProgress{service: s, jobID: job.ID}
	jobCtx, cancel := context.WithTimeout(ctx, executionTimeout)
	err = handler.Reacquire(jobCtx, started, progress)
	if err == nil {
		err = jobCtx.Err()
	}
	cancel()
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, arr.ErrMutationOutcomeUnknown) {
		current, ok := s.Job(job.ID)
		if !ok {
			return false
		}
		delay := retryDelay(current, err)
		queued, updateErr := s.updateJobDurable(job.ID, StatusQueued, func(job *Job) {
			job.LastError = err.Error()
			job.RetryAt = s.now().Add(delay)
		})
		if updateErr != nil {
			return false
		}
		s.scheduleRetry(queued)
		return true
	}
	if err != nil {
		_, updateErr := s.updateJob(job.ID, StatusFailed, func(job *Job) {
			job.LastError = err.Error()
			job.RetryAt = time.Time{}
		})
		return updateErr == nil
	}
	current, ok := s.Job(job.ID)
	if !ok || current.Status.Terminal() || progress.waiting.Load() || current.Status.waiting() {
		return true
	}
	_, err = s.updateJob(job.ID, StatusReady, func(job *Job) {
		job.LastError = ""
		job.RetryAt = time.Time{}
	})
	return err == nil
}

func (s *Service) nextJob() (Job, bool) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	var next Job
	found := false
	for _, id := range s.activeReacquisitions {
		job, ok := s.jobs[id]
		if !ok || !job.Status.dispatchable() || job.RetryAt.After(s.now()) {
			continue
		}
		if !found || job.UpdatedAt.Before(next.UpdatedAt) {
			next = cloneJob(job)
			found = true
		}
	}
	return next, found
}

func (s *Service) updateJob(id string, status Status, mutate func(*Job)) (Job, error) {
	return s.updateJobWithDurability(id, status, mutate, false)
}

func (s *Service) updateJobDurable(id string, status Status, mutate func(*Job)) (Job, error) {
	return s.updateJobWithDurability(id, status, mutate, true)
}

func (s *Service) updateJobWithDurability(
	id string,
	status Status,
	mutate func(*Job),
	durable bool,
) (Job, error) {
	if !status.valid() {
		return Job{}, fmt.Errorf("invalid reacquire status %q", status)
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	original, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("reacquire job %q not found", id)
	}
	if original.Status.Terminal() && status != original.Status {
		return Job{}, fmt.Errorf("reacquire job %q is already %s", id, original.Status)
	}
	job := cloneJob(original)
	if mutate != nil {
		mutate(&job)
	}
	job.ID = original.ID
	job.Cause = original.Cause
	job.Strategy = original.Strategy
	job.ArrName = original.ArrName
	job.ArrType = original.ArrType
	job.EntryID = original.EntryID
	job.FileID = original.FileID
	job.DownloadID = original.DownloadID
	job.Bindings = cloneJob(original).Bindings
	job.CreatedAt = original.CreatedAt
	job.Status = status
	job.UpdatedAt = s.now()
	if status != StatusQueued && job.StartedAt.IsZero() {
		job.StartedAt = job.UpdatedAt
	}
	if status.Terminal() {
		job.CompletedAt = job.UpdatedAt
		job.RetryAt = time.Time{}
	}
	var err error
	if durable {
		err = s.jobRepository.SaveDurable(job)
	} else {
		err = s.jobRepository.Save(job)
	}
	if err != nil {
		return Job{}, err
	}
	s.jobs[id] = cloneJob(job)
	if status.Terminal() {
		key := keyForJob(job)
		if s.activeReacquisitions[key] == id {
			delete(s.activeReacquisitions, key)
		}
	}
	return cloneJob(job), nil
}

// completeWaitingJobs re-checks the waiting jobs an indexing change can satisfy.
// It takes the whole change set at once: a generation replace carries every
// binding of an arr.Arr, and checking one binding at a time walks the job table
// once per binding.
func (s *Service) completeWaitingJobs(bindings ...Binding) error {
	downloadsByArr := make(map[string]map[string]struct{})
	for _, binding := range bindings {
		if binding.DownloadID == "" {
			continue
		}
		downloads, ok := downloadsByArr[binding.ArrName]
		if !ok {
			downloads = make(map[string]struct{})
			downloadsByArr[binding.ArrName] = downloads
		}
		downloads[binding.DownloadID] = struct{}{}
	}
	if len(downloadsByArr) == 0 {
		return nil
	}

	s.jobsMu.RLock()
	ids := make([]string, 0)
	for _, id := range s.activeReacquisitions {
		job, ok := s.jobs[id]
		if !ok || !job.Status.waiting() {
			continue
		}
		if replacesDownload(downloadsByArr[job.ArrName], job.DownloadID) {
			ids = append(ids, id)
		}
	}
	s.jobsMu.RUnlock()
	for _, id := range ids {
		job, ok := s.Job(id)
		if !ok {
			continue
		}
		if err := s.completeJobFromIndex(job); err != nil {
			return err
		}
	}
	return nil
}

type serviceJobProgress struct {
	service *Service
	jobID   string
	waiting atomic.Bool
}

func (progress *serviceJobProgress) Update(status Status, mutate func(*Job)) error {
	return progress.update(status, mutate, false)
}

func (progress *serviceJobProgress) UpdateDurable(status Status, mutate func(*Job)) error {
	return progress.update(status, mutate, true)
}

func (progress *serviceJobProgress) update(status Status, mutate func(*Job), durable bool) error {
	if status.waiting() {
		progress.waiting.Store(true)
	}
	var (
		job Job
		err error
	)
	if durable {
		job, err = progress.service.updateJobDurable(progress.jobID, status, mutate)
	} else {
		job, err = progress.service.updateJob(progress.jobID, status, mutate)
	}
	if err != nil || !status.waiting() {
		return err
	}
	return progress.service.completeJobFromIndex(job)
}

func retryDelay(job Job, err error) time.Duration {
	attempts := 0
	for _, mutation := range job.Mutations {
		attempts = max(attempts, mutation.Attempts)
	}
	delay := retryBaseDelay
	for range max(0, min(attempts-1, 5)) {
		delay *= 2
	}
	return min(retryMaxDelay, max(delay, arr.MutationRetryAfter(err)))
}

func (s *Service) scheduleRetry(job Job) {
	if job.RetryAt.IsZero() || s.ctx == nil {
		return
	}
	delay := max(time.Duration(0), job.RetryAt.Sub(s.now()))
	s.wg.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
		case <-timer.C:
			s.signal()
		}
	})
}

func (s *Service) schedulePersistedRetries() {
	for _, job := range s.Jobs() {
		if job.Status.dispatchable() && job.RetryAt.After(s.now()) {
			s.scheduleRetry(job)
		}
	}
}

// replacesDownload reports whether any indexed download differs from the one
// the job is replacing, which is what makes the job worth re-checking.
func replacesDownload(downloads map[string]struct{}, jobDownloadID string) bool {
	if len(downloads) == 0 {
		return false
	}
	if len(downloads) > 1 {
		return true
	}
	_, sameOnly := downloads[jobDownloadID]
	return !sameOnly
}

func (status Status) waiting() bool {
	return status == StatusWaitingForGrab || status == StatusWaitingForDownload || status == StatusWaitingForImport
}

func (status Status) dispatchable() bool {
	return status == StatusQueued || status == StatusResolving || status == StatusInvalidating || status == StatusBlocklisting || status == StatusSearching
}

func keyForBinding(binding Binding) jobKey {
	if binding.DownloadID != "" {
		return jobKey{arrName: binding.ArrName, downloadID: binding.DownloadID}
	}
	return jobKey{arrName: binding.ArrName, entryID: binding.EntryID, fileID: binding.EntryFileID}
}

func keyForJob(job Job) jobKey {
	if job.DownloadID != "" {
		return jobKey{arrName: job.ArrName, downloadID: job.DownloadID}
	}
	return jobKey{arrName: job.ArrName, entryID: job.EntryID, fileID: job.FileID}
}

func (s *Service) completeJobFromIndex(job Job) error {
	replacementDownloadIDs, ready := s.replacementsForJob(job)
	if !ready {
		return nil
	}
	_, err := s.updateJob(job.ID, StatusReady, func(job *Job) {
		job.ReplacementDownloadIDs = replacementDownloadIDs
		job.ReplacementDownloadID = replacementDownloadIDs[0]
		job.LastError = ""
	})
	return err
}

func (s *Service) replacementsForJob(job Job) ([]string, bool) {
	downloads := make(map[string]struct{})
	for _, target := range job.Bindings {
		var candidates []Binding
		switch {
		case target.MovieID > 0:
			candidates = s.index.ByMovieID(job.ArrName, target.MovieID)
		case len(target.EpisodeIDs) > 0:
			for _, episodeID := range target.EpisodeIDs {
				episodeCandidates := s.index.ByEpisodeID(job.ArrName, episodeID)
				candidate, found := replacementCandidate(episodeCandidates, job.DownloadID, func(candidate Binding) bool {
					return candidate.SeriesID == target.SeriesID
				})
				if !found {
					return nil, false
				}
				downloads[candidate.DownloadID] = struct{}{}
			}
			continue
		case target.SeriesID > 0:
			candidates = s.index.BySeriesID(job.ArrName, target.SeriesID)
		default:
			return nil, false
		}

		candidate, found := replacementCandidate(candidates, job.DownloadID, func(candidate Binding) bool {
			if target.MovieID > 0 {
				return candidate.MovieID == target.MovieID
			}
			return candidate.SeriesID == target.SeriesID && candidate.SeasonNumber == target.SeasonNumber
		})
		if !found {
			return nil, false
		}
		downloads[candidate.DownloadID] = struct{}{}
	}
	if len(downloads) == 0 {
		return nil, false
	}
	result := make([]string, 0, len(downloads))
	for downloadID := range downloads {
		result = append(result, downloadID)
	}
	slices.Sort(result)
	return result, true
}

func replacementCandidate(candidates []Binding, oldDownloadID string, matches func(Binding) bool) (Binding, bool) {
	for _, candidate := range candidates {
		if candidate.AuthorizesMutation() && candidate.DownloadID != "" && candidate.DownloadID != oldDownloadID && matches(candidate) {
			return candidate, true
		}
	}
	return Binding{}, false
}

func (s *Service) maintainJobs() {
	now := s.now()
	var expire []string
	var prune []string

	s.jobsMu.RLock()
	for id, job := range s.jobs {
		switch {
		case job.Status.waiting() && now.Sub(job.UpdatedAt) >= waitingTimeout:
			expire = append(expire, id)
		case job.Status == StatusFailed && terminalAge(now, job) >= failureRetention:
			prune = append(prune, id)
		case (job.Status == StatusReady || job.Status == StatusCancelled) && terminalAge(now, job) >= successRetention:
			prune = append(prune, id)
		}
	}
	s.jobsMu.RUnlock()

	for _, id := range expire {
		_, _ = s.updateJob(id, StatusFailed, func(job *Job) {
			job.LastError = "replacement was not imported before the reacquisition timeout"
		})
	}
	for _, id := range prune {
		s.pruneTerminalJob(id, now)
	}
}

func (s *Service) pruneTerminalJob(id string, now time.Time) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job, ok := s.jobs[id]
	if !ok || !job.Status.Terminal() {
		return
	}
	retention := successRetention
	if job.Status == StatusFailed {
		retention = failureRetention
	}
	if terminalAge(now, job) < retention {
		return
	}
	if err := s.jobRepository.Delete(id); err == nil {
		delete(s.jobs, id)
	}
}

func terminalAge(now time.Time, job Job) time.Duration {
	at := job.CompletedAt
	if at.IsZero() {
		at = job.UpdatedAt
	}
	return now.Sub(at)
}
