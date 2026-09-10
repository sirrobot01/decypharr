package reacquire

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type LibraryRequest struct {
	ArrName     string
	ArrFileID   int
	LibraryPath string
	Cause       Cause
}

// ReacquireLibraryFile verifies an unindexed file before it queues replacement.
func (s *Service) ReacquireLibraryFile(ctx context.Context, request LibraryRequest) (*Job, error) {
	release, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer release()
	if s.arrs == nil {
		return nil, fmt.Errorf("Arr registry is unavailable")
	}
	if request.ArrFileID <= 0 || request.LibraryPath == "" || !request.Cause.valid() {
		return nil, fmt.Errorf("Arr file ID, library path, and cause are required")
	}
	instance, ok := s.arrs.Get(request.ArrName)
	if !ok {
		return nil, fmt.Errorf("Arr %q is unavailable", request.ArrName)
	}
	if binding, ok := s.index.ByArrFile(instance.Name, request.ArrFileID); ok {
		if !binding.AuthorizesMutation() || !sameLibraryPath(binding.LibraryPath, request.LibraryPath) {
			return nil, ErrBindingUnsafe
		}
		return s.enqueue(Request{EntryID: binding.EntryID, FileID: binding.EntryFileID,
			Cause: request.Cause, Strategy: StrategyHistoryFailed}, binding)
	}
	fileID := strconv.Itoa(request.ArrFileID)
	entryID := "arr-library:" + instance.Fingerprint() + ":" + fileID
	// Return an existing job before the remote file is read. The job may have deleted it.
	s.jobsMu.RLock()
	id, exists := s.activeReacquisitions[jobKey{arrName: instance.Name, entryID: entryID, fileID: fileID}]
	job := cloneJob(s.jobs[id])
	s.jobsMu.RUnlock()
	if exists {
		return &job, nil
	}
	current, found, err := s.arrs.LibraryFile(ctx, instance.Name, request.ArrFileID)
	if err != nil {
		return nil, err
	}
	if !found || current.ArrFileID != request.ArrFileID || !sameLibraryPath(request.LibraryPath, current.Path) {
		return nil, fmt.Errorf("%w: Arr file no longer matches the broken path", ErrBindingUnsafe)
	}
	binding := Binding{
		ArrName: instance.Name, ArrType: instance.Type, ArrInstanceFingerprint: instance.Fingerprint(),
		EntryID: entryID, EntryFileID: fileID, ArrFileID: current.ArrFileID, LibraryPath: current.Path,
		SeriesID: current.SeriesID, SeasonNumber: current.SeasonNumber, EpisodeIDs: current.EpisodeIDs,
		MovieID: current.MovieID, Confidence: ConfidenceLibraryFile,
	}
	if !binding.AuthorizesMutation() {
		return nil, ErrBindingUnsafe
	}
	if err := validateSearchBindings(instance, []Binding{binding}); err != nil {
		return nil, err
	}
	return s.enqueue(Request{EntryID: entryID, FileID: fileID, Cause: request.Cause, Strategy: StrategyCommandSearch}, binding)
}

// reconcileLibraryJobs checks imported replacements for jobs without managed entries.
func (s *Service) reconcileLibraryJobs(ctx context.Context) {
	if s.arrs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, executionTimeout)
	defer cancel()
	for _, job := range s.Jobs() {
		if ctx.Err() != nil {
			return
		}
		if !job.Status.waiting() || len(job.Bindings) != 1 || job.Bindings[0].Confidence != ConfidenceLibraryFile {
			continue
		}
		binding := job.Bindings[0]
		instance, ok := s.arrs.Get(job.ArrName)
		if !ok || instance.Fingerprint() != binding.ArrInstanceFingerprint {
			continue
		}
		files, err := s.arrs.LibraryFilesForMedia(ctx, job.ArrName, []int{cmp.Or(binding.MovieID, binding.SeriesID)})
		if err != nil {
			continue
		}
		remaining := slices.Clone(binding.EpisodeIDs)
		ready := false
		for _, file := range files {
			if file.ArrFileID == binding.ArrFileID || file.ArrFileID <= 0 || file.Path == "" {
				continue
			}
			if instance.Type == arr.Radarr && file.MovieID == binding.MovieID {
				ready = true
				break
			}
			if instance.Type == arr.Sonarr && file.SeriesID == binding.SeriesID {
				remaining = slices.DeleteFunc(remaining, func(id int) bool { return slices.Contains(file.EpisodeIDs, id) })
				ready = len(binding.EpisodeIDs) > 0 && len(remaining) == 0
			}
		}
		if ready {
			_, _ = s.updateJobDurable(job.ID, StatusReady, func(job *Job) { job.LastError = "" })
		}
	}
}
