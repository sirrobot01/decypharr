package reacquire

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

var (
	ErrBindingNotFound   = errors.New("arr binding not found")
	ErrBindingUnsafe     = errors.New("arr binding is not authoritative")
	ErrJobNotTerminal    = errors.New("only completed reacquire jobs can be deleted")
	ErrServiceNotStarted = errors.New("arr service not started")
	ErrServiceClosed     = errors.New("arr service closed")
)

type Confidence string

const (
	ConfidenceExactPath Confidence = "exact_path"
	// ConfidenceManagedTarget binds a library symlink that points into the
	// managed mount to the one managed file with that name and size. It is
	// used when the entry folder no longer matches, which happens after a
	// folder-naming change and for the season entries a multi-season torrent
	// is split into.
	ConfidenceManagedTarget   Confidence = "managed_target"
	ConfidenceDownloadHistory Confidence = "download_history"
	ConfidenceHeuristic       Confidence = "heuristic"
)

type Binding struct {
	ArrName                string     `json:"arrName"`
	ArrType                arr.Type   `json:"arrType"`
	ArrInstanceFingerprint string     `json:"arrInstanceFingerprint,omitempty"`
	EntryID                string     `json:"entryId"`
	EntryName              string     `json:"entryName,omitempty"`
	EntryFileID            string     `json:"entryFileId"`
	EntryFileName          string     `json:"entryFileName,omitempty"`
	DownloadID             string     `json:"downloadId,omitempty"`
	ArrFileID              int        `json:"arrFileId,omitzero"`
	LibraryPath            string     `json:"libraryPath,omitempty"`
	SeriesID               int        `json:"seriesId,omitzero"`
	SeasonNumber           int        `json:"seasonNumber,omitzero"`
	EpisodeIDs             []int      `json:"episodeIds,omitempty"`
	MovieID                int        `json:"movieId,omitzero"`
	Confidence             Confidence `json:"confidence,omitempty"`
	Generation             uint64     `json:"generation,omitzero"`
	UpdatedAt              time.Time  `json:"updatedAt"`
}

func (b Binding) AuthorizesMutation() bool {
	return b.ArrFileID > 0 &&
		b.ArrInstanceFingerprint != "" &&
		b.LibraryPath != "" &&
		(b.ArrType == arr.Sonarr || b.ArrType == arr.Radarr) &&
		(b.Confidence == ConfidenceExactPath ||
			b.Confidence == ConfidenceManagedTarget ||
			b.Confidence == ConfidenceDownloadHistory)
}

func (b Binding) validate() error {
	switch {
	case b.ArrName == "":
		return errors.New("arr name is required")
	case b.EntryID == "":
		return errors.New("entry ID is required")
	case b.EntryFileID == "":
		return errors.New("entry file ID is required")
	default:
		return nil
	}
}

func cloneBinding(binding Binding) Binding {
	binding.EpisodeIDs = slices.Clone(binding.EpisodeIDs)
	return binding
}

type Cause string

const (
	CauseStream Cause = "stream"
	CauseRepair Cause = "repair"
	CauseManual Cause = "manual"
)

func (cause Cause) valid() bool {
	return cause == CauseStream || cause == CauseRepair || cause == CauseManual
}

type Strategy string

const (
	StrategyHistoryFailed   Strategy = "history_failed"
	StrategyInteractiveBest Strategy = "interactive_best"
	StrategyCommandSearch   Strategy = "command_search"
)

func (strategy Strategy) normalized() Strategy {
	if strategy == "" {
		return StrategyHistoryFailed
	}
	return strategy
}

func (strategy Strategy) valid() bool {
	strategy = strategy.normalized()
	return strategy == StrategyHistoryFailed || strategy == StrategyInteractiveBest || strategy == StrategyCommandSearch
}

type Status string

const (
	StatusQueued             Status = "queued"
	StatusResolving          Status = "resolving"
	StatusInvalidating       Status = "invalidating"
	StatusBlocklisting       Status = "blocklisting"
	StatusSearching          Status = "searching"
	StatusWaitingForGrab     Status = "waiting_for_grab"
	StatusWaitingForDownload Status = "waiting_for_download"
	StatusWaitingForImport   Status = "waiting_for_import"
	StatusReady              Status = "ready"
	StatusFailed             Status = "failed"
	StatusCancelled          Status = "cancelled"
)

func (status Status) Terminal() bool {
	return status == StatusReady || status == StatusFailed || status == StatusCancelled
}

func (status Status) valid() bool {
	switch status {
	case StatusQueued,
		StatusResolving,
		StatusInvalidating,
		StatusBlocklisting,
		StatusSearching,
		StatusWaitingForGrab,
		StatusWaitingForDownload,
		StatusWaitingForImport,
		StatusReady,
		StatusFailed,
		StatusCancelled:
		return true
	default:
		return false
	}
}

type Request struct {
	EntryID  string   `json:"entryId"`
	FileID   string   `json:"fileId"`
	Cause    Cause    `json:"cause"`
	Strategy Strategy `json:"strategy,omitempty"`
}

func (request Request) validate() error {
	switch {
	case request.EntryID == "":
		return errors.New("entry ID is required")
	case request.FileID == "":
		return errors.New("file ID is required")
	case !request.Cause.valid():
		return fmt.Errorf("invalid reacquire cause %q", request.Cause)
	case !request.Strategy.valid():
		return fmt.Errorf("invalid reacquire strategy %q", request.Strategy)
	default:
		return nil
	}
}

type Job struct {
	ID                     string     `json:"id"`
	Status                 Status     `json:"status"`
	Cause                  Cause      `json:"cause"`
	Strategy               Strategy   `json:"strategy"`
	ArrName                string     `json:"arrName"`
	ArrType                arr.Type   `json:"arrType"`
	EntryID                string     `json:"entryId"`
	FileID                 string     `json:"fileId"`
	DownloadID             string     `json:"downloadId,omitempty"`
	Bindings               []Binding  `json:"bindings"`
	ReplacementDownloadID  string     `json:"replacementDownloadId,omitempty"`
	ReplacementDownloadIDs []string   `json:"replacementDownloadIds,omitempty"`
	Mutations              []Mutation `json:"mutations,omitempty"`
	RetryAt                time.Time  `json:"retryAt,omitzero"`
	LastError              string     `json:"lastError,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	StartedAt              time.Time  `json:"startedAt,omitzero"`
	CompletedAt            time.Time  `json:"completedAt,omitzero"`
}

func cloneJob(job Job) Job {
	bindings := job.Bindings
	job.Bindings = make([]Binding, len(bindings))
	for i, binding := range bindings {
		job.Bindings[i] = cloneBinding(binding)
	}
	job.ReplacementDownloadIDs = slices.Clone(job.ReplacementDownloadIDs)
	mutations := job.Mutations
	job.Mutations = make([]Mutation, len(mutations))
	for i, mutation := range mutations {
		job.Mutations[i] = cloneMutation(mutation)
	}
	return job
}

func validateMutationInstance(instance arr.Arr, bindings []Binding) error {
	fingerprint := instance.Fingerprint()
	if fingerprint == "" {
		return errors.New("arr instance identity is unavailable")
	}
	for _, binding := range bindings {
		if binding.ArrInstanceFingerprint != fingerprint {
			return errors.New("arr instance changed since binding was indexed")
		}
	}
	return nil
}

func sameLibraryPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}
