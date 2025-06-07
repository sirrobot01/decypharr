package repair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"golang.org/x/sync/errgroup"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Repair struct {
	Jobs        map[string]*Job
	arrs        *arr.Storage
	deb         *debrid.Storage
	interval    string
	runOnStart  bool
	ZurgURL     string
	IsZurg      bool
	useWebdav   bool
	autoProcess bool
	logger      zerolog.Logger
	filename    string
	workers     int
	scheduler   gocron.Scheduler

	debridPathCache map[string]string // Cache for path -> debrid name mapping
	cacheMutex      sync.RWMutex
	ctx             context.Context
}

type JobStatus string

const (
	JobStarted    JobStatus = "started"
	JobPending    JobStatus = "pending"
	JobFailed     JobStatus = "failed"
	JobCompleted  JobStatus = "completed"
	JobProcessing JobStatus = "processing"
	JobCancelled  JobStatus = "cancelled"
)

type Job struct {
	ID          string                       `json:"id"`
	Arrs        []string                     `json:"arrs"`
	MediaIDs    []string                     `json:"media_ids"`
	StartedAt   time.Time                    `json:"created_at"`
	BrokenItems map[string][]arr.ContentFile `json:"broken_items"`
	Status      JobStatus                    `json:"status"`
	CompletedAt time.Time                    `json:"finished_at"`
	FailedAt    time.Time                    `json:"failed_at"`
	AutoProcess bool                         `json:"auto_process"`
	Recurrent   bool                         `json:"recurrent"`

	Error string `json:"error"`

	cancelFunc context.CancelFunc
	ctx        context.Context
}

func New(arrs *arr.Storage, engine *debrid.Storage) *Repair {
	cfg := config.Get()
	workers := runtime.NumCPU() * 20
	if cfg.Repair.Workers > 0 {
		workers = cfg.Repair.Workers
	}
	r := &Repair{
		arrs:        arrs,
		logger:      logger.New("repair"),
		interval:    cfg.Repair.Interval,
		runOnStart:  cfg.Repair.RunOnStart,
		ZurgURL:     cfg.Repair.ZurgURL,
		useWebdav:   cfg.Repair.UseWebDav,
		autoProcess: cfg.Repair.AutoProcess,
		filename:    filepath.Join(cfg.Path, "repair.json"),
		deb:         engine,
		workers:     workers,
		ctx:         context.Background(),
	}
	if r.ZurgURL != "" {
		r.IsZurg = true
	}
	// Load jobs from file
	r.loadFromFile()

	return r
}

func (r *Repair) Reset() {
	// Stop scheduler
	if r.scheduler != nil {
		if err := r.scheduler.StopJobs(); err != nil {
			r.logger.Error().Err(err).Msg("Error stopping scheduler")
		}

		if err := r.scheduler.Shutdown(); err != nil {
			r.logger.Error().Err(err).Msg("Error shutting down scheduler")
		}
	}
	// Reset jobs
	r.Jobs = make(map[string]*Job)

}

func (r *Repair) Start(ctx context.Context) error {
	//r.ctx = ctx
	if r.runOnStart {
		r.logger.Info().Msgf("Running initial repair")
		go func() {
			if err := r.AddJob([]string{}, []string{}, r.autoProcess, true); err != nil {
				r.logger.Error().Err(err).Msg("Error running initial repair")
			}
		}()
	}

	r.scheduler, _ = gocron.NewScheduler(gocron.WithLocation(time.Local))

	if jd, err := utils.ConvertToJobDef(r.interval); err != nil {
		r.logger.Error().Err(err).Str("interval", r.interval).Msg("Error converting interval")
	} else {
		_, err2 := r.scheduler.NewJob(jd, gocron.NewTask(func() {
			r.logger.Info().Msgf("Repair job started at %s", time.Now().Format("15:04:05"))
			if err := r.AddJob([]string{}, []string{}, r.autoProcess, true); err != nil {
				r.logger.Error().Err(err).Msg("Error running repair job")
			}
		}))
		if err2 != nil {
			r.logger.Error().Err(err2).Msg("Error creating repair job")
		} else {
			r.scheduler.Start()
			r.logger.Info().Msgf("Repair job scheduled every %s", r.interval)
		}
	}

	<-ctx.Done()

	r.logger.Info().Msg("Stopping repair scheduler")
	r.Reset()

	return nil
}

func (j *Job) discordContext() string {
	format := `
		**ID**: %s
		**Arrs**: %s
		**Media IDs**: %s
		**Status**: %s
		**Started At**: %s
		**Completed At**: %s 
`

	dateFmt := "2006-01-02 15:04:05"

	return fmt.Sprintf(format, j.ID, strings.Join(j.Arrs, ","), strings.Join(j.MediaIDs, ", "), j.Status, j.StartedAt.Format(dateFmt), j.CompletedAt.Format(dateFmt))
}

func (r *Repair) getArrs(arrNames []string) []string {
	arrs := make([]string, 0)
	if len(arrNames) == 0 {
		// No specific arrs, get all
		// Also check if any arrs are set to skip repair
		_arrs := r.arrs.GetAll()
		for _, a := range _arrs {
			if a.SkipRepair {
				continue
			}
			arrs = append(arrs, a.Name)
		}
	} else {
		for _, name := range arrNames {
			a := r.arrs.Get(name)
			if a == nil || a.Host == "" || a.Token == "" {
				continue
			}
			arrs = append(arrs, a.Name)
		}
	}
	return arrs
}

func jobKey(arrNames []string, mediaIDs []string) string {
	return fmt.Sprintf("%s-%s", strings.Join(arrNames, ","), strings.Join(mediaIDs, ","))
}

func (r *Repair) reset(j *Job) {
	// Update job for rerun
	j.Status = JobStarted
	j.StartedAt = time.Now()
	j.CompletedAt = time.Time{}
	j.FailedAt = time.Time{}
	j.BrokenItems = nil
	j.Error = ""
	if j.Recurrent || j.Arrs == nil {
		j.Arrs = r.getArrs([]string{}) // Get new arrs
	}
}

func (r *Repair) newJob(arrsNames []string, mediaIDs []string) *Job {
	arrs := r.getArrs(arrsNames)
	return &Job{
		ID:        uuid.New().String(),
		Arrs:      arrs,
		MediaIDs:  mediaIDs,
		StartedAt: time.Now(),
		Status:    JobStarted,
	}
}

func (r *Repair) preRunChecks() error {

	if r.useWebdav {
		caches := r.deb.Caches()
		if len(caches) == 0 {
			return fmt.Errorf("no caches found")
		}
		return nil
	}

	// Check if zurg url is reachable
	if !r.IsZurg {
		return nil
	}
	resp, err := http.Get(fmt.Sprint(r.ZurgURL, "/http/version.txt"))
	if err != nil {
		r.logger.Error().Err(err).Msgf("Precheck failed: Failed to reach zurg at %s", r.ZurgURL)
		return err
	}
	if resp.StatusCode != http.StatusOK {
		r.logger.Debug().Msgf("Precheck failed: Zurg returned %d", resp.StatusCode)
		return err
	}
	return nil
}

func (r *Repair) AddJob(arrsNames []string, mediaIDs []string, autoProcess, recurrent bool) error {
	key := jobKey(arrsNames, mediaIDs)
	job, ok := r.Jobs[key]
	if job != nil && job.Status == JobStarted {
		return fmt.Errorf("job already running")
	}
	if !ok {
		job = r.newJob(arrsNames, mediaIDs)
	}
	job.AutoProcess = autoProcess
	job.Recurrent = recurrent
	r.reset(job)

	job.ctx, job.cancelFunc = context.WithCancel(r.ctx)
	r.Jobs[key] = job
	go r.saveToFile()
	go func() {
		if err := r.repair(job); err != nil {
			r.logger.Error().Err(err).Msg("Error running repair")
			if !errors.Is(job.ctx.Err(), context.Canceled) {
				job.FailedAt = time.Now()
				job.Error = err.Error()
				job.Status = JobFailed
				job.CompletedAt = time.Now()
			} else {
				job.FailedAt = time.Now()
				job.Error = err.Error()
				job.Status = JobFailed
				job.CompletedAt = time.Now()
			}
		}
	}()
	return nil
}

func (r *Repair) StopJob(id string) error {
	job := r.GetJob(id)
	if job == nil {
		return fmt.Errorf("job %s not found", id)
	}

	// Check if job can be stopped
	if job.Status != JobStarted && job.Status != JobProcessing {
		return fmt.Errorf("job %s cannot be stopped (status: %s)", id, job.Status)
	}

	// Cancel the job
	if job.cancelFunc != nil {
		job.cancelFunc()
		r.logger.Info().Msgf("Job %s cancellation requested", id)
		go func() {
			if job.Status == JobStarted || job.Status == JobProcessing {
				job.Status = JobCancelled
				job.CompletedAt = time.Now()
				job.Error = "Job was cancelled by user"
				r.saveToFile()
			}
		}()

		return nil
	}

	return fmt.Errorf("job %s cannot be cancelled", id)
}

func (r *Repair) repair(job *Job) error {
	defer r.saveToFile()
	if err := r.preRunChecks(); err != nil {
		return err
	}

	// Use a mutex to protect concurrent access to brokenItems
	var mu sync.Mutex
	brokenItems := map[string][]arr.ContentFile{}
	g, ctx := errgroup.WithContext(job.ctx)

	for _, a := range job.Arrs {
		a := a // Capture range variable
		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			var items []arr.ContentFile
			var err error

			if len(job.MediaIDs) == 0 {
				items, err = r.repairArr(job, a, "")
				if err != nil {
					r.logger.Error().Err(err).Msgf("Error repairing %s", a)
					return err
				}
			} else {
				for _, id := range job.MediaIDs {
					someItems, err := r.repairArr(job, a, id)
					if err != nil {
						r.logger.Error().Err(err).Msgf("Error repairing %s with ID %s", a, id)
						return err
					}
					items = append(items, someItems...)
				}
			}

			// Safely append the found items to the shared slice
			if len(items) > 0 {
				mu.Lock()
				brokenItems[a] = items
				mu.Unlock()
			}

			return nil
		})
	}

	// Wait for all goroutines to complete and check for errors
	if err := g.Wait(); err != nil {
		// Check if j0b was canceled
		if errors.Is(ctx.Err(), context.Canceled) {
			job.Status = JobCancelled
			job.CompletedAt = time.Now()
			job.Error = "Job was cancelled"
			return fmt.Errorf("job cancelled")
		}

		job.FailedAt = time.Now()
		job.Error = err.Error()
		job.Status = JobFailed
		job.CompletedAt = time.Now()
		go func() {
			if err := request.SendDiscordMessage("repair_failed", "error", job.discordContext()); err != nil {
				r.logger.Error().Msgf("Error sending discord message: %v", err)
			}
		}()
		return err
	}

	if len(brokenItems) == 0 {
		job.CompletedAt = time.Now()
		job.Status = JobCompleted

		go func() {
			if err := request.SendDiscordMessage("repair_complete", "success", job.discordContext()); err != nil {
				r.logger.Error().Msgf("Error sending discord message: %v", err)
			}
		}()

		return nil
	}

	job.BrokenItems = brokenItems
	if job.AutoProcess {
		// Job is already processed
		job.CompletedAt = time.Now() // Mark as completed
		job.Status = JobCompleted
		go func() {
			if err := request.SendDiscordMessage("repair_complete", "success", job.discordContext()); err != nil {
				r.logger.Error().Msgf("Error sending discord message: %v", err)
			}
		}()
	} else {
		job.Status = JobPending
		go func() {
			if err := request.SendDiscordMessage("repair_pending", "pending", job.discordContext()); err != nil {
				r.logger.Error().Msgf("Error sending discord message: %v", err)
			}
		}()
	}
	return nil
}

func (r *Repair) repairArr(job *Job, _arr string, tmdbId string) ([]arr.ContentFile, error) {
	brokenItems := make([]arr.ContentFile, 0)
	a := r.arrs.Get(_arr)

	r.logger.Info().Msgf("Starting repair for %s", a.Name)
	media, err := a.GetMedia(tmdbId)
	if err != nil {
		r.logger.Info().Msgf("Failed to get %s media: %v", a.Name, err)
		return brokenItems, err
	}
	r.logger.Info().Msgf("Found %d %s media", len(media), a.Name)

	if len(media) == 0 {
		r.logger.Info().Msgf("No %s media found", a.Name)
		return brokenItems, nil
	}
	// Check first media to confirm mounts are accessible
	if err := r.checkMountUp(media); err != nil {
		r.logger.Error().Err(err).Msgf("Mount check failed for %s", a.Name)
		return brokenItems, fmt.Errorf("mount check failed: %w", err)
	}

	// Mutex for brokenItems
	var mu sync.Mutex
	var wg sync.WaitGroup
	workerChan := make(chan arr.Content, min(len(media), r.workers))

	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range workerChan {
				select {
				case <-job.ctx.Done():
					return
				default:
				}
				items := r.getBrokenFiles(job, m)
				if items != nil {
					r.logger.Debug().Msgf("Found %d broken files for %s", len(items), m.Title)
					if job.AutoProcess {
						r.logger.Info().Msgf("Auto processing %d broken items for %s", len(items), m.Title)

						// Delete broken items
						if err := a.DeleteFiles(items); err != nil {
							r.logger.Debug().Msgf("Failed to delete broken items for %s: %v", m.Title, err)
						}

						// Search for missing items
						if err := a.SearchMissing(items); err != nil {
							r.logger.Debug().Msgf("Failed to search missing items for %s: %v", m.Title, err)
						}
					}

					mu.Lock()
					brokenItems = append(brokenItems, items...)
					mu.Unlock()
				}
			}
		}()
	}

	for _, m := range media {
		select {
		case <-job.ctx.Done():
			break
		default:
			workerChan <- m
		}
	}

	close(workerChan)
	wg.Wait()
	if len(brokenItems) == 0 {
		r.logger.Info().Msgf("No broken items found for %s", a.Name)
		return brokenItems, nil
	}

	r.logger.Info().Msgf("Repair completed for %s. %d broken items found", a.Name, len(brokenItems))
	return brokenItems, nil
}

// checkMountUp checks if the mounts are accessible
func (r *Repair) checkMountUp(media []arr.Content) error {
	firstMedia := media[0]
	for _, m := range media {
		if len(m.Files) > 0 {
			firstMedia = m
			break
		}
	}
	files := firstMedia.Files
	if len(files) == 0 {
		return fmt.Errorf("no files found in media %s", firstMedia.Title)
	}
	firstFile := files[0]
	symlinkPath := getSymlinkTarget(firstFile.Path)

	if symlinkPath == "" {
		return fmt.Errorf("no symlink target found for %s", firstFile.Path)
	}
	r.logger.Debug().Msgf("Checking symlink parent directory for %s", symlinkPath)

	parentSymlink := filepath.Dir(filepath.Dir(symlinkPath)) // /mnt/zurg/torrents/movie/movie.mkv -> /mnt/zurg/torrents
	if _, err := os.Stat(parentSymlink); os.IsNotExist(err) {
		return fmt.Errorf("parent directory %s not accessible for %s", parentSymlink, firstFile.Path)
	}
	return nil
}

func (r *Repair) getBrokenFiles(job *Job, media arr.Content) []arr.ContentFile {

	if r.useWebdav {
		return r.getWebdavBrokenFiles(job, media)
	} else if r.IsZurg {
		return r.getZurgBrokenFiles(job, media)
	} else {
		return r.getFileBrokenFiles(job, media)
	}
}

func (r *Repair) getFileBrokenFiles(job *Job, media arr.Content) []arr.ContentFile {
	// This checks symlink target, try to get read a tiny bit of the file

	brokenFiles := make([]arr.ContentFile, 0)

	uniqueParents := collectFiles(media)

	for parent, files := range uniqueParents {
		// Check stat
		// Check file stat first
		for _, file := range files {
			if err := fileIsReadable(file.Path); err != nil {
				r.logger.Debug().Msgf("Broken file found at: %s", parent)
				brokenFiles = append(brokenFiles, file)
			}
		}
	}
	if len(brokenFiles) == 0 {
		r.logger.Debug().Msgf("No broken files found for %s", media.Title)
		return nil
	}
	r.logger.Debug().Msgf("%d broken files found for %s", len(brokenFiles), media.Title)
	return brokenFiles
}

func (r *Repair) getZurgBrokenFiles(job *Job, media arr.Content) []arr.ContentFile {
	// Use zurg setup to check file availability with zurg
	// This reduces bandwidth usage significantly

	brokenFiles := make([]arr.ContentFile, 0)
	uniqueParents := collectFiles(media)
	tr := &http.Transport{
		TLSHandshakeTimeout: 60 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   20 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	client := request.New(request.WithTimeout(0), request.WithTransport(tr))
	// Access zurg url + symlink folder + first file(encoded)
	for parent, files := range uniqueParents {
		r.logger.Debug().Msgf("Checking %s", parent)
		torrentName := url.PathEscape(filepath.Base(parent))

		if len(files) == 0 {
			r.logger.Debug().Msgf("No files found for %s. Skipping", torrentName)
			continue
		}

		for _, file := range files {
			encodedFile := url.PathEscape(file.TargetPath)
			fullURL := fmt.Sprintf("%s/http/__all__/%s/%s", r.ZurgURL, torrentName, encodedFile)
			if _, err := os.Stat(file.Path); os.IsNotExist(err) {
				r.logger.Debug().Msgf("Broken symlink found: %s", fullURL)
				brokenFiles = append(brokenFiles, file)
				continue
			}
			resp, err := client.Get(fullURL)
			if err != nil {
				r.logger.Error().Err(err).Msgf("Failed to reach %s", fullURL)
				brokenFiles = append(brokenFiles, file)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				r.logger.Debug().Msgf("Failed to get download url for %s", fullURL)
				if err := resp.Body.Close(); err != nil {
					return nil
				}
				brokenFiles = append(brokenFiles, file)
				continue
			}
			downloadUrl := resp.Request.URL.String()

			if err := resp.Body.Close(); err != nil {
				return nil
			}
			if downloadUrl != "" {
				r.logger.Trace().Msgf("Found download url: %s", downloadUrl)
			} else {
				r.logger.Debug().Msgf("Failed to get download url for %s", fullURL)
				brokenFiles = append(brokenFiles, file)
				continue
			}
		}
	}
	if len(brokenFiles) == 0 {
		r.logger.Debug().Msgf("No broken files found for %s", media.Title)
		return nil
	}
	r.logger.Debug().Msgf("%d broken files found for %s", len(brokenFiles), media.Title)
	return brokenFiles
}

func (r *Repair) getWebdavBrokenFiles(job *Job, media arr.Content) []arr.ContentFile {
	// Use internal webdav setup to check file availability

	caches := r.deb.Caches()
	if len(caches) == 0 {
		r.logger.Info().Msg("No caches found. Can't use webdav")
		return nil
	}

	clients := r.deb.Clients()
	if len(clients) == 0 {
		r.logger.Info().Msg("No clients found. Can't use webdav")
		return nil
	}

	brokenFiles := make([]arr.ContentFile, 0)
	uniqueParents := collectFiles(media)
	var brokenFilesMutex sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrent torrent checks
	semaphore := make(chan struct{}, min(len(uniqueParents), 30)) // Limit to 5 concurrent checks
	for torrentPath, files := range uniqueParents {
		wg.Add(1)
		go func(torrentPath string, files []arr.ContentFile) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release

			select {
			case <-job.ctx.Done():
				return
			default:
			}

			brokenFilesForTorrent := r.checkTorrentFiles(torrentPath, files, clients, caches)

			if len(brokenFilesForTorrent) > 0 {
				brokenFilesMutex.Lock()
				brokenFiles = append(brokenFiles, brokenFilesForTorrent...)
				brokenFilesMutex.Unlock()
			}
		}(torrentPath, files)
	}

	wg.Wait()
	if len(brokenFiles) == 0 {
		return nil
	}
	r.logger.Debug().Msgf("%d broken files found for %s", len(brokenFiles), media.Title)
	return brokenFiles
}

func (r *Repair) GetJob(id string) *Job {
	for _, job := range r.Jobs {
		if job.ID == id {
			return job
		}
	}
	return nil
}

func (r *Repair) GetJobs() []*Job {
	jobs := make([]*Job, 0)
	for _, job := range r.Jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})

	return jobs
}

func (r *Repair) ProcessJob(id string) error {
	job := r.GetJob(id)
	if job == nil {
		return fmt.Errorf("job %s not found", id)
	}
	// All validation checks remain the same
	if job.Status != JobPending {
		return fmt.Errorf("job %s not pending", id)
	}
	if job.StartedAt.IsZero() {
		return fmt.Errorf("job %s not started", id)
	}
	if !job.CompletedAt.IsZero() {
		return fmt.Errorf("job %s already completed", id)
	}
	if !job.FailedAt.IsZero() {
		return fmt.Errorf("job %s already failed", id)
	}

	brokenItems := job.BrokenItems
	if len(brokenItems) == 0 {
		r.logger.Info().Msgf("No broken items found for job %s", id)
		job.CompletedAt = time.Now()
		job.Status = JobCompleted
		return nil
	}

	if job.ctx == nil || job.ctx.Err() != nil {
		job.ctx, job.cancelFunc = context.WithCancel(r.ctx)
	}

	g, ctx := errgroup.WithContext(job.ctx)
	g.SetLimit(r.workers)

	for arrName, items := range brokenItems {
		items := items
		arrName := arrName
		g.Go(func() error {

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			a := r.arrs.Get(arrName)
			if a == nil {
				r.logger.Error().Msgf("Arr %s not found", arrName)
				return nil
			}

			if err := a.DeleteFiles(items); err != nil {
				r.logger.Error().Err(err).Msgf("Failed to delete broken items for %s", arrName)
				return nil
			}
			// Search for missing items
			if err := a.SearchMissing(items); err != nil {
				r.logger.Error().Err(err).Msgf("Failed to search missing items for %s", arrName)
				return nil
			}
			return nil
		})
	}

	// Update job status to in-progress
	job.Status = JobProcessing
	r.saveToFile()

	// Launch a goroutine to wait for completion and update the job
	go func() {
		if err := g.Wait(); err != nil {
			job.FailedAt = time.Now()
			job.Error = err.Error()
			job.CompletedAt = time.Now()
			job.Status = JobFailed
			r.logger.Error().Err(err).Msgf("Job %s failed", id)
		} else {
			job.CompletedAt = time.Now()
			job.Status = JobCompleted
			r.logger.Info().Msgf("Job %s completed successfully", id)
		}

		r.saveToFile()
	}()

	return nil
}

func (r *Repair) saveToFile() {
	// Save jobs to file
	data, err := json.Marshal(r.Jobs)
	if err != nil {
		r.logger.Error().Err(err).Msg("Failed to marshal jobs")
	}
	_ = os.WriteFile(r.filename, data, 0644)
}

func (r *Repair) loadFromFile() {
	data, err := os.ReadFile(r.filename)
	if err != nil && os.IsNotExist(err) {
		r.Jobs = make(map[string]*Job)
		return
	}
	_jobs := make(map[string]*Job)
	err = json.Unmarshal(data, &_jobs)
	if err != nil {
		r.logger.Error().Err(err).Msg("Failed to unmarshal jobs; resetting")
		r.Jobs = make(map[string]*Job)
		return
	}
	jobs := make(map[string]*Job)
	for k, v := range _jobs {
		if v.Status != JobPending {
			// Skip jobs that are not pending processing due to reboot
			continue
		}
		jobs[k] = v
	}
	r.Jobs = jobs
}

func (r *Repair) DeleteJobs(ids []string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		for k, job := range r.Jobs {
			if job.ID == id {
				delete(r.Jobs, k)
			}
		}
	}
	go r.saveToFile()
}

// Cleanup Cleans up the repair instance
func (r *Repair) Cleanup() {
	r.Jobs = make(map[string]*Job)
	r.arrs = nil
	r.deb = nil
	r.ctx = nil
	r.logger.Info().Msg("Repair stopped")
}
