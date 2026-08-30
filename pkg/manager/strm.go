package manager

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/strm"
)

// maxStrmRead bounds .strm content reads; canonical URLs are far smaller.
const maxStrmRead = 1024

// Strm maintains the .strm export tree. When enabled, every entry in storage
// gets a folder of identity-URL .strm files (plus sidecars) under the
// configured path. Decypharr owns that tree completely: the reconciler
// writes, rewrites, and removes files so disk always matches
// f(entries, config). Files whose content is not one of our URLs are never
// touched.
type Strm struct {
	manager *Manager
	logger  zerolog.Logger
	sweepMu sync.Mutex
}

func NewStrm(m *Manager) *Strm {
	return &Strm{
		manager: m,
		logger:  m.logger.With().Str("component", "strm").Logger(),
	}
}

// StrmReport is the outcome of a reconcile pass.
type StrmReport struct {
	Entries  int      `json:"entries"`
	Verified int      `json:"verified"`
	Written  int      `json:"written"`
	Sidecars int      `json:"sidecars"`
	Deleted  int      `json:"deleted"`
	Errors   []string `json:"errors,omitempty"`
}

func (r *StrmReport) addError(err error) {
	r.Errors = append(r.Errors, err.Error())
}

type strmTarget struct {
	path    string
	content string
}

// entryDir returns the entry's folder inside the export tree; mirrors the
// __all__ mount layout.
func entryDir(cfg *config.Config, entry *storage.Entry) string {
	return filepath.Join(cfg.Strm.Path, entry.GetFolder())
}

// desired returns the .strm files and sidecar downloads an entry should have.
func (s *Strm) desired(entry *storage.Entry) ([]strmTarget, []*storage.File) {
	cfg := config.Get()
	base := strm.BaseURL(cfg)
	dir := entryDir(cfg, entry)
	maxSidecar := cfg.Strm.SidecarMaxBytes()

	var targets []strmTarget
	var sidecars []*storage.File
	for _, f := range entry.GetActiveFiles() {
		switch {
		case utils.IsVideoFile(f.Name):
			targets = append(targets, strmTarget{
				path:    filepath.Join(dir, strm.FileName(f.Name, cfg.Strm.KeepMediaExtension)),
				content: strm.FileURL(base, cfg.Strm.Secret, entry.InfoHash, f.ID, f.Name),
			})
		case cfg.Strm.SidecarsEnabled() && strm.IsSidecar(f.Name) && f.Size > 0 && f.Size <= maxSidecar:
			sidecars = append(sidecars, f)
		}
	}
	return targets, sidecars
}

// SyncEntryAsync reconciles one entry's export folder in the background —
// the post-download and entry-updated trigger. Only entries present in main
// storage are exported; their URLs must resolve.
func (s *Strm) SyncEntryAsync(entry *storage.Entry) {
	if !config.Get().Strm.Active() {
		return
	}
	go func() {
		if ok, _ := s.manager.storage.Exists(entry.InfoHash); !ok {
			return
		}
		rep := &StrmReport{}
		s.syncEntry(s.manager.ctx, entry, rep)
		for _, e := range rep.Errors {
			s.logger.Warn().Str("entry", entry.Name).Msg("strm sync: " + e)
		}
	}()
}

// syncEntry reconciles one entry's folder: (re)write desired .strm files,
// remove stale ones this entry owns, download missing sidecars. Returns the
// desired targets so sweeps know which paths are accounted for.
func (s *Strm) syncEntry(ctx context.Context, entry *storage.Entry, rep *StrmReport) []strmTarget {
	if len(entry.Files) == 0 {
		return nil
	}

	// Backfill stable file IDs for entries stored before IDs existed;
	// AddOrUpdate assigns and persists them.
	for _, f := range entry.Files {
		if f.ID == "" {
			if err := s.manager.storage.AddOrUpdate(entry); err != nil {
				rep.addError(fmt.Errorf("assign file ids for %s: %w", entry.Name, err))
				return nil
			}
			break
		}
	}

	rep.Entries++
	targets, sidecars := s.desired(entry)
	for _, t := range targets {
		current, err := readStrm(t.path)
		if err == nil && current == t.content {
			rep.Verified++
			continue
		}
		if err := writeStrm(t.path, t.content); err != nil {
			rep.addError(err)
			continue
		}
		rep.Written++
	}
	s.removeStale(entry, targets, rep)
	for _, f := range sidecars {
		if ctx.Err() != nil {
			break
		}
		s.syncSidecar(ctx, entry, f, rep)
	}
	return targets
}

// removeStale deletes .strm files in the entry's folder that carry this
// entry's infohash but are no longer desired (renamed by a repair, naming
// config changed). Other entries may share the folder name; their files are
// left alone.
func (s *Strm) removeStale(entry *storage.Entry, targets []strmTarget, rep *StrmReport) {
	keep := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		keep[t.path] = struct{}{}
	}
	_ = filepath.WalkDir(entryDir(config.Get(), entry), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		if _, ok := keep[path]; ok {
			return nil
		}
		content, err := readStrm(path)
		if err != nil {
			return nil
		}
		if infohash, _, ok := strm.ParseURL(content); !ok || infohash != entry.InfoHash {
			return nil
		}
		if err := os.Remove(path); err != nil {
			rep.addError(err)
			return nil
		}
		rep.Deleted++
		return nil
	})
}

func (s *Strm) syncSidecar(ctx context.Context, entry *storage.Entry, file *storage.File, rep *StrmReport) {
	dest := filepath.Join(entryDir(config.Get(), entry), file.Name)
	if fi, err := os.Stat(dest); err == nil && fi.Size() == file.Size {
		return
	}
	if err := s.downloadSidecar(ctx, entry, file, dest); err != nil {
		rep.addError(fmt.Errorf("sidecar %s: %w", file.Name, err))
		return
	}
	rep.Sidecars++
}

func (s *Strm) downloadSidecar(ctx context.Context, entry *storage.Entry, file *storage.File, dest string) error {
	stream, err := s.manager.OpenStreamUntracked(ctx, entry, file.Name, 0)
	if err != nil {
		return err
	}
	defer stream.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(stream, file.Size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != file.Size {
		err = fmt.Errorf("short download: %d of %d bytes", n, file.Size)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// Sweep reconciles the whole export tree: every entry's folder is synced,
// then any .strm left in the tree that is ours but no longer desired —
// deleted entries, renamed files, stale folder names — is removed, pruning
// directories that become empty.
func (s *Strm) Sweep(ctx context.Context) (*StrmReport, error) {
	cfg := config.Get()
	if !cfg.Strm.Active() {
		return nil, fmt.Errorf("strm is disabled or has no path configured")
	}

	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	rep := &StrmReport{}
	entries, err := s.manager.storage.List(nil)
	if err != nil {
		return nil, err
	}

	owned := make(map[string]struct{})
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		for _, t := range s.syncEntry(ctx, e, rep) {
			owned[t.path] = struct{}{}
		}
	}

	var stale []string
	_ = filepath.WalkDir(cfg.Strm.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".strm") {
			return ctx.Err()
		}
		if _, ok := owned[path]; ok {
			return ctx.Err()
		}
		content, err := readStrm(path)
		if err != nil {
			return ctx.Err()
		}
		if _, _, ok := strm.ParseURL(content); ok {
			stale = append(stale, path)
		}
		return ctx.Err()
	})
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			rep.addError(err)
			continue
		}
		rep.Deleted++
		pruneEmptyDirs(filepath.Dir(path), cfg.Strm.Path)
	}

	return rep, nil
}

// SweepAsync runs a background sweep — the regenerate, config-change, and
// startup trigger. A no-op while strm is disabled.
func (s *Strm) SweepAsync(reason string) {
	if !config.Get().Strm.Active() {
		return
	}
	go func() {
		rep, err := s.Sweep(s.manager.ctx)
		if err != nil {
			s.logger.Warn().Err(err).Str("reason", reason).Msg("strm sweep failed")
			return
		}
		s.logger.Info().
			Str("reason", reason).
			Int("entries", rep.Entries).
			Int("written", rep.Written).
			Int("verified", rep.Verified).
			Int("sidecars", rep.Sidecars).
			Int("deleted", rep.Deleted).
			Int("errors", len(rep.Errors)).
			Msg("strm sweep complete")
	}()
}

// RemoveEntryAsync deletes an entry's .strm files right after the entry is
// deleted, so its folder doesn't linger until the next sweep. Only files
// carrying the entry's infohash are removed.
func (s *Strm) RemoveEntryAsync(entry *storage.Entry) {
	cfg := config.Get()
	if !cfg.Strm.Active() {
		return
	}
	go func() {
		dir := entryDir(cfg, entry)
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".strm") {
				return nil
			}
			content, err := readStrm(path)
			if err != nil {
				return nil
			}
			if infohash, _, ok := strm.ParseURL(content); ok && infohash == entry.InfoHash {
				_ = os.Remove(path)
			}
			return nil
		})
		// Sidecars carry no signature; remove them by name while we still
		// know the entry's file list.
		for _, f := range entry.Files {
			if strm.IsSidecar(f.Name) {
				_ = os.Remove(filepath.Join(dir, f.Name))
			}
		}
		pruneEmptyDirs(dir, cfg.Strm.Path)
	}()
}

// pruneEmptyDirs removes empty directories from dir up to (excluding) root.
func pruneEmptyDirs(dir, root string) {
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return // not empty or gone
		}
	}
}

func readStrm(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStrmRead))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeStrm(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}
