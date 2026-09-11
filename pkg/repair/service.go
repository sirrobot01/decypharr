package repair

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/hearsay"
	"github.com/sirrobot01/decypharr/pkg/notifications"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
)

// Status is the snapshot returned by the /api/repair/status endpoint.
type Status struct {
	Enabled      bool                         `json:"enabled"`
	NextRunAt    *time.Time                   `json:"next_run_at,omitempty"`
	ActiveRun    *storage.RepairRun           `json:"active_run,omitempty"`
	LastRun      *storage.RepairRun           `json:"last_run,omitempty"`
	HealthCounts map[storage.HealthStatus]int `json:"health_counts"`
}

// RunOptions are one-off options for a manually-started repair run.
// Nil fields inherit the persisted repair config.
type RunOptions struct {
	IgnoreLastChecked bool
	AutoRepair        *bool
	UnrestrictLink    bool
	// VerifyContent additionally head-verifies each NZB media file through
	// the serving stack (container-signature check). Catches files whose
	// articles all exist but were assembled wrong — invisible to the default
	// STAT probe. Costs one article download per file. nil falls back to the
	// configured repair.verify_content setting.
	VerifyContent *bool
	ProtocolScope string
}

type ClearStateResult struct {
	Statuses []storage.HealthStatus `json:"statuses"`
	Cleared  int                    `json:"cleared"`
}

const (
	repairSchedulerTag     = "repair-sweep"
	repairStopSchedulerTag = "repair-sweep-stop"
	repairDefaultWorkers   = 5
	repairDefaultRecheck   = 7 * 24 * time.Hour
	repairHistoryRetained  = 100
	// At most this many files probed concurrently within a single entry. The
	// outer worker count comes from cfg.Repair.Workers.
	repairFilesPerEntry          = 2
	repairStopDrainTimeout       = 30 * time.Second
	repairStopFinalRepairTimeout = 5 * time.Minute
)

type Backend interface {
	ProviderClient(string) debrid.Client
	ReinsertEntry(context.Context, *storage.Entry) error
	RemoveTorrentFile(string, string) error
	DeleteEntry(string, bool) error
}

type Reacquirer interface {
	Reacquire(reacquire.Request) (*reacquire.Job, error)
}

type Dependencies struct {
	Scheduler     gocron.Scheduler
	Backend       Backend
	Storage       *storage.Storage
	Arrs          *arr.Service
	Reacquirer    Reacquirer
	Usenet        *usenet.Usenet
	Notifications *notifications.Service
	Hearsay       *hearsay.Service
}

// Service checks entry health and coordinates repairs.
type Service struct {
	scheduler     gocron.Scheduler
	backend       Backend
	storage       *storage.Storage
	arrs          *arr.Service
	reacquirer    Reacquirer
	usenet        *usenet.Usenet
	notifications *notifications.Service
	hearsay       *hearsay.Service
	logger        zerolog.Logger

	mu             sync.Mutex
	parentCtx      context.Context
	activeRunID    string
	cancelRun      context.CancelFunc
	scheduled      bool
	stopScheduled  bool
	activeStopFunc func()
	runWG          sync.WaitGroup
}

// New builds a repair service from its dependencies.
func New(deps Dependencies) *Service {
	return &Service{
		scheduler:     deps.Scheduler,
		backend:       deps.Backend,
		storage:       deps.Storage,
		arrs:          deps.Arrs,
		reacquirer:    deps.Reacquirer,
		usenet:        deps.Usenet,
		notifications: deps.Notifications,
		hearsay:       deps.Hearsay,
		logger:        logger.New("repair"),
		parentCtx:     context.Background(),
	}
}

func (r *Service) cfg() config.RepairConfig { return config.Get().Repair }

func normalizeRepairProtocolScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "all", "both":
		return "all"
	case string(config.ProtocolTorrent):
		return string(config.ProtocolTorrent)
	case string(config.ProtocolNZB):
		return string(config.ProtocolNZB)
	default:
		return ""
	}
}

func (r *Service) effectiveProtocolScope(opts RunOptions) string {
	if scope := normalizeRepairProtocolScope(opts.ProtocolScope); scope != "" {
		return scope
	}
	return "all"
}

func repairProtocolMatches(scope string, protocol config.Protocol) bool {
	switch normalizeRepairProtocolScope(scope) {
	case "", "all":
		return true
	case string(config.ProtocolTorrent):
		return protocol == config.ProtocolTorrent
	case string(config.ProtocolNZB):
		return protocol == config.ProtocolNZB
	default:
		return true
	}
}

func (r *Service) workers() int {
	if w := r.cfg().Workers; w > 0 {
		return w
	}
	return repairDefaultWorkers
}

func (r *Service) recheckInterval() time.Duration {
	raw := r.cfg().RecheckInterval
	if raw == "" {
		return repairDefaultRecheck
	}
	d, err := utils.ParseDuration(raw)
	if err != nil || d <= 0 {
		return repairDefaultRecheck
	}
	return d
}
