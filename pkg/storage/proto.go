package storage

import (
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

// ============================================================================
// File Conversions
// ============================================================================

func fileToProto(f *File) *FileProto {
	pb := &FileProto{
		Name:     f.Name,
		Path:     f.Path,
		Size:     f.Size,
		Deleted:  f.Deleted,
		InfoHash: f.InfoHash,
	}
	if !f.AddedOn.IsZero() {
		pb.AddedOnUnix = f.AddedOn.Unix()
	}
	if f.ByteRange != nil {
		pb.HasByteRange = true
		pb.ByteRangeStart = f.ByteRange[0]
		pb.ByteRangeEnd = f.ByteRange[1]
	}
	return pb
}

func protoToFile(pb *FileProto) *File {
	f := &File{
		Name:     pb.Name,
		Path:     pb.Path,
		Size:     pb.Size,
		Deleted:  pb.Deleted,
		InfoHash: pb.InfoHash,
	}
	if pb.AddedOnUnix != 0 {
		f.AddedOn = time.Unix(pb.AddedOnUnix, 0)
	}
	if pb.HasByteRange {
		f.ByteRange = &[2]int64{pb.ByteRangeStart, pb.ByteRangeEnd}
	}
	return f
}

// ============================================================================
// ProviderFile Conversions
// ============================================================================

func providerFileToProto(pf *ProviderFile) *ProviderFileProto {
	return &ProviderFileProto{
		Id:   pf.Id,
		Link: pf.Link,
		Path: pf.Path,
	}
}

func protoToProviderFile(pb *ProviderFileProto) *ProviderFile {
	return &ProviderFile{
		Id:   pb.Id,
		Link: pb.Link,
		Path: pb.Path,
	}
}

// ============================================================================
// ProviderEntry Conversions
// ============================================================================

func providerEntryToProto(pe *ProviderEntry) *ProviderEntryProto {
	pb := &ProviderEntryProto{
		Provider: pe.Provider,
		Id:       pe.ID,
		Status:   string(pe.Status),
		Progress: pe.Progress,
		Files:    make(map[string]*ProviderFileProto),
	}
	if !pe.AddedAt.IsZero() {
		pb.AddedAtUnix = pe.AddedAt.Unix()
	}
	if pe.RemovedAt != nil {
		pb.HasRemovedAt = true
		pb.RemovedAtUnix = pe.RemovedAt.Unix()
	}
	if pe.DownloadedAt != nil {
		pb.HasDownloadedAt = true
		pb.DownloadedAtUnix = pe.DownloadedAt.Unix()
	}
	for name, pf := range pe.Files {
		pb.Files[name] = providerFileToProto(pf)
	}
	return pb
}

func protoToProviderEntry(pb *ProviderEntryProto) *ProviderEntry {
	pe := &ProviderEntry{
		Provider: pb.Provider,
		ID:       pb.Id,
		Status:   debridTypes.TorrentStatus(pb.Status),
		Progress: pb.Progress,
		Files:    make(map[string]*ProviderFile),
	}
	if pb.AddedAtUnix != 0 {
		pe.AddedAt = time.Unix(pb.AddedAtUnix, 0)
	}
	if pb.HasRemovedAt {
		t := time.Unix(pb.RemovedAtUnix, 0)
		pe.RemovedAt = &t
	}
	if pb.HasDownloadedAt {
		t := time.Unix(pb.DownloadedAtUnix, 0)
		pe.DownloadedAt = &t
	}
	for name, pf := range pb.Files {
		pe.Files[name] = protoToProviderFile(pf)
	}
	return pe
}

// ============================================================================
// Entry Conversions
// ============================================================================

func EntryToProto(e *Entry) *EntryProto {
	pb := &EntryProto{
		Protocol:         string(e.Protocol),
		InfoHash:         e.InfoHash,
		Name:             e.Name,
		OriginalFilename: e.OriginalFilename,
		Size:             e.Size,
		Bytes:            e.Bytes,
		Magnet:           e.Magnet,
		IsDownloading:    e.IsDownloading,
		SizeDownloaded:   e.SizeDownloaded,
		ActiveProvider:   e.ActiveProvider,
		Providers:        make(map[string]*ProviderEntryProto),
		Files:            make(map[string]*FileProto),
		State:            string(e.State),
		Status:           string(e.Status),
		Progress:         e.Progress,
		Speed:            e.Speed,
		Seeders:          int32(e.Seeders),
		IsComplete:       e.IsComplete,
		Bad:              e.Bad,
		Category:         e.Category,
		Tags:             e.Tags,
		MountPath:        e.MountPath,
		SavePath:         e.SavePath,
		ContentPath:      e.ContentPath,
		Action:           string(e.Action),
		DownloadUncached: e.DownloadUncached,
		CallbackUrl:      e.CallbackURL,
		SkipMultiSeason:  e.SkipMultiSeason,
		LastError:        e.LastError,
		ErrorCount:       int32(e.ErrorCount),
	}

	// Timestamps
	if !e.AddedOn.IsZero() {
		pb.AddedOnUnix = e.AddedOn.Unix()
	}
	if !e.CreatedAt.IsZero() {
		pb.CreatedAtUnix = e.CreatedAt.Unix()
	}
	if !e.UpdatedAt.IsZero() {
		pb.UpdatedAtUnix = e.UpdatedAt.Unix()
	}
	if e.CompletedAt != nil {
		pb.HasCompletedAt = true
		pb.CompletedAtUnix = e.CompletedAt.Unix()
	}
	if e.ImportedAt != nil {
		pb.HasImportedAt = true
		pb.ImportedAtUnix = e.ImportedAt.Unix()
	}
	if e.LastErrorTime != nil {
		pb.HasLastErrorTime = true
		pb.LastErrorTimeUnix = e.LastErrorTime.Unix()
	}

	// Maps
	for name, pe := range e.Providers {
		pb.Providers[name] = providerEntryToProto(pe)
	}
	for name, f := range e.Files {
		pb.Files[name] = fileToProto(f)
	}

	return pb
}

func ProtoToEntry(pb *EntryProto) *Entry {
	e := &Entry{
		Protocol:         config.Protocol(pb.Protocol),
		InfoHash:         pb.InfoHash,
		Name:             pb.Name,
		OriginalFilename: pb.OriginalFilename,
		Size:             pb.Size,
		Bytes:            pb.Bytes,
		Magnet:           pb.Magnet,
		IsDownloading:    pb.IsDownloading,
		SizeDownloaded:   pb.SizeDownloaded,
		ActiveProvider:   pb.ActiveProvider,
		Providers:        make(map[string]*ProviderEntry),
		Files:            make(map[string]*File),
		State:            TorrentState(pb.State),
		Status:           debridTypes.TorrentStatus(pb.Status),
		Progress:         pb.Progress,
		Speed:            pb.Speed,
		Seeders:          int(pb.Seeders),
		IsComplete:       pb.IsComplete,
		Bad:              pb.Bad,
		Category:         pb.Category,
		Tags:             pb.Tags,
		MountPath:        pb.MountPath,
		SavePath:         pb.SavePath,
		ContentPath:      pb.ContentPath,
		Action:           config.DownloadAction(pb.Action),
		DownloadUncached: pb.DownloadUncached,
		CallbackURL:      pb.CallbackUrl,
		SkipMultiSeason:  pb.SkipMultiSeason,
		LastError:        pb.LastError,
		ErrorCount:       int(pb.ErrorCount),
	}

	// Timestamps
	if pb.AddedOnUnix != 0 {
		e.AddedOn = time.Unix(pb.AddedOnUnix, 0)
	}
	if pb.CreatedAtUnix != 0 {
		e.CreatedAt = time.Unix(pb.CreatedAtUnix, 0)
	}
	if pb.UpdatedAtUnix != 0 {
		e.UpdatedAt = time.Unix(pb.UpdatedAtUnix, 0)
	}
	if pb.HasCompletedAt {
		t := time.Unix(pb.CompletedAtUnix, 0)
		e.CompletedAt = &t
	}
	if pb.HasImportedAt {
		t := time.Unix(pb.ImportedAtUnix, 0)
		e.ImportedAt = &t
	}
	if pb.HasLastErrorTime {
		t := time.Unix(pb.LastErrorTimeUnix, 0)
		e.LastErrorTime = &t
	}

	// Maps
	for name, pe := range pb.Providers {
		e.Providers[name] = protoToProviderEntry(pe)
	}
	for name, f := range pb.Files {
		e.Files[name] = protoToFile(f)
	}

	// Ensure non-nil slices
	if e.Tags == nil {
		e.Tags = []string{}
	}

	return e
}

// ============================================================================
// EntryItem Conversions
// ============================================================================

func EntryItemToProto(ei *EntryItem) *EntryItemProto {
	pb := &EntryItemProto{
		Name:  ei.Name,
		Size:  ei.Size,
		Files: make(map[string]*FileProto),
	}
	for name, f := range ei.Files {
		pb.Files[name] = fileToProto(f)
	}
	return pb
}

func ProtoToEntryItem(pb *EntryItemProto) *EntryItem {
	ei := &EntryItem{
		Name:  pb.Name,
		Size:  pb.Size,
		Files: make(map[string]*File),
	}
	for name, f := range pb.Files {
		ei.Files[name] = protoToFile(f)
	}
	return ei
}

// ============================================================================
// Job (Repair) Conversions — removed in repair v2.
// ============================================================================

/*
func JobToProto(j *Job) *JobProto {
	pb := &JobProto{
		Id:          j.ID,
		Arrs:        j.Arrs,
		MediaIds:    j.MediaIDs,
		Status:      string(j.Status),
		AutoProcess: j.AutoProcess,
		Recurrent:   j.Recurrent,
		Error:       j.Error,
		BrokenItems: make(map[string]*BrokenItemsProto),
	}
	if !j.StartedAt.IsZero() {
		pb.StartedAtUnix = j.StartedAt.Unix()
	}
	if !j.CompletedAt.IsZero() {
		pb.CompletedAtUnix = j.CompletedAt.Unix()
	}
	if !j.FailedAt.IsZero() {
		pb.FailedAtUnix = j.FailedAt.Unix()
	}

	// Convert broken items
	for key, files := range j.BrokenItems {
		protoFiles := make([]*ContentFileProto, len(files))
		for i, f := range files {
			protoFiles[i] = &ContentFileProto{
				Name:         f.Name,
				Path:         f.Path,
				Id:           int32(f.Id),
				EpisodeId:    int32(f.EpisodeId),
				FileId:       int32(f.FileId),
				TargetPath:   f.TargetPath,
				IsSymlink:    f.IsSymlink,
				IsBroken:     f.IsBroken,
				SeasonNumber: int32(f.SeasonNumber),
				Processed:    f.Processed,
				Size:         f.Size,
			}
		}
		pb.BrokenItems[key] = &BrokenItemsProto{Files: protoFiles}
	}

	return pb
}

func ProtoToJob(pb *JobProto) *Job {
	j := &Job{
		ID:          pb.Id,
		Arrs:        pb.Arrs,
		MediaIDs:    pb.MediaIds,
		Status:      JobStatus(pb.Status),
		AutoProcess: pb.AutoProcess,
		Recurrent:   pb.Recurrent,
		Error:       pb.Error,
		BrokenItems: make(map[string][]arr.ContentFile),
	}
	if pb.StartedAtUnix != 0 {
		j.StartedAt = time.Unix(pb.StartedAtUnix, 0)
	}
	if pb.CompletedAtUnix != 0 {
		j.CompletedAt = time.Unix(pb.CompletedAtUnix, 0)
	}
	if pb.FailedAtUnix != 0 {
		j.FailedAt = time.Unix(pb.FailedAtUnix, 0)
	}

	// Convert broken items
	for key, protoFiles := range pb.BrokenItems {
		files := make([]arr.ContentFile, len(protoFiles.Files))
		for i, f := range protoFiles.Files {
			files[i] = arr.ContentFile{
				Name:         f.Name,
				Path:         f.Path,
				Id:           int(f.Id),
				EpisodeId:    int(f.EpisodeId),
				FileId:       int(f.FileId),
				TargetPath:   f.TargetPath,
				IsSymlink:    f.IsSymlink,
				IsBroken:     f.IsBroken,
				SeasonNumber: int(f.SeasonNumber),
				Processed:    f.Processed,
				Size:         f.Size,
			}
		}
		j.BrokenItems[key] = files
	}

	return j
}
*/

// ============================================================================
// SwitcherJob Conversions
// ============================================================================

func SwitcherJobToProto(sj *SwitcherJob) *SwitcherJobProto {
	pb := &SwitcherJobProto{
		Id:             sj.ID,
		InfoHash:       sj.InfoHash,
		SourceProvider: sj.SourceProvider,
		TargetProvider: sj.TargetProvider,
		Status:         string(sj.Status),
		Progress:       sj.Progress,
		Error:          sj.Error,
		KeepOld:        sj.KeepOld,
		WaitComplete:   sj.WaitComplete,
	}
	if !sj.CreatedAt.IsZero() {
		pb.CreatedAtUnix = sj.CreatedAt.Unix()
	}
	if sj.CompletedAt != nil {
		pb.HasCompletedAt = true
		pb.CompletedAtUnix = sj.CompletedAt.Unix()
	}
	return pb
}

func ProtoToSwitcherJob(pb *SwitcherJobProto) *SwitcherJob {
	sj := &SwitcherJob{
		ID:             pb.Id,
		InfoHash:       pb.InfoHash,
		SourceProvider: pb.SourceProvider,
		TargetProvider: pb.TargetProvider,
		Status:         SwitcherStatus(pb.Status),
		Progress:       pb.Progress,
		Error:          pb.Error,
		KeepOld:        pb.KeepOld,
		WaitComplete:   pb.WaitComplete,
	}
	if pb.CreatedAtUnix != 0 {
		sj.CreatedAt = time.Unix(pb.CreatedAtUnix, 0)
	}
	if pb.HasCompletedAt {
		t := time.Unix(pb.CompletedAtUnix, 0)
		sj.CompletedAt = &t
	}
	return sj
}

// ============================================================================
// SystemMigrationStatus Conversions
// ============================================================================

func SystemMigrationStatusToProto(sms *SystemMigrationStatus) *SystemMigrationStatusProto {
	pb := &SystemMigrationStatusProto{
		Running:   sms.Running,
		Total:     int32(sms.Total),
		Completed: int32(sms.Completed),
		Errors:    int32(sms.Errors),
		ErrorList: sms.ErrorList,
	}
	if !sms.StartedAt.IsZero() {
		pb.StartedAtUnix = sms.StartedAt.Unix()
	}
	if !sms.UpdatedAt.IsZero() {
		pb.UpdatedAtUnix = sms.UpdatedAt.Unix()
	}
	return pb
}

func ProtoToSystemMigrationStatus(pb *SystemMigrationStatusProto) *SystemMigrationStatus {
	sms := &SystemMigrationStatus{
		Running:   pb.Running,
		Total:     int(pb.Total),
		Completed: int(pb.Completed),
		Errors:    int(pb.Errors),
		ErrorList: pb.ErrorList,
	}
	if pb.StartedAtUnix != 0 {
		sms.StartedAt = time.Unix(pb.StartedAtUnix, 0)
	}
	if pb.UpdatedAtUnix != 0 {
		sms.UpdatedAt = time.Unix(pb.UpdatedAtUnix, 0)
	}
	return sms
}
