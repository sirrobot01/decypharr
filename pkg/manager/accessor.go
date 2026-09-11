package manager

import (
	"context"

	"github.com/go-co-op/gocron/v2"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/hearsay"
	"github.com/sirrobot01/decypharr/pkg/repair"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
)

func (m *Manager) SetMountManager(mountMgr MountManager) {
	m.mountManager = mountMgr
}

// Repair returns the repair service. It is created during init() so callers
// can rely on a non-nil value once the manager has been constructed.
func (m *Manager) Repair() *repair.Service {
	return m.repair
}

// Hearsay returns the hearsay service, or nil when disabled. A nil
// service is safe to call.
func (m *Manager) Hearsay() *hearsay.Service {
	return m.hearsay
}

func (m *Manager) Scheduler() gocron.Scheduler {
	return m.scheduler
}

// Migrator returns the migrator instance
func (m *Manager) Migrator() *Migrator {
	return m.migrator
}

// Strm returns the .strm reconciler. Created during init(), so callers can
// rely on a non-nil value once the manager has been constructed.
func (m *Manager) Strm() *Strm {
	return m.strm
}

// Arr returns the Arr storage instance
func (m *Manager) Arr() *arr.Service {
	return m.arr
}

func (m *Manager) ArrService() *reacquire.Service {
	return m.arrService
}

func (m *Manager) RefreshArrIndex() bool {
	return m.arrIndexer != nil && m.arrIndexer.Refresh()
}

func (m *Manager) ReindexArrEntry(arrName, entryID string) bool {
	return m.arrIndexer != nil && m.arrIndexer.ReindexEntry(arrName, entryID)
}

func (m *Manager) Queue() *Queue {
	return m.queue
}

func (m *Manager) JobQueue() *JobQueue {
	return m.jobQueue
}

func (m *Manager) Clients() *xsync.Map[string, debrid.Client] {
	return m.clients
}

func (m *Manager) MountManager() MountManager {
	return m.mountManager
}

func (m *Manager) Storage() *storage.Storage {
	return m.storage
}

func (m *Manager) Context() context.Context {
	return m.ctx
}

func (m *Manager) Usenet() *usenet.Usenet {
	return m.usenet
}

// GetDebridSpeedTestResult returns stored speed test result for a specific debrid provider
func (m *Manager) GetDebridSpeedTestResult(provider string) (debridTypes.SpeedTestResult, bool) {
	return m.debridSpeedTestResults.Load(provider)
}
