package wire

import (
	"fmt"
	"sync"
)

type File struct {
	Index        int     `json:"index,omitempty"`
	Name         string  `json:"name,omitempty"`
	Size         int64   `json:"size,omitempty"`
	Progress     int     `json:"progress,omitempty"`
	Priority     int     `json:"priority,omitempty"`
	IsSeed       bool    `json:"is_seed,omitempty"`
	PieceRange   []int   `json:"piece_range,omitempty"`
	Availability float64 `json:"availability,omitempty"`
}

type Torrent struct {
	ID          string  `json:"id"`
	DebridID    string  `json:"debrid_id"`
	Debrid      string  `json:"debrid"`
	TorrentPath string  `json:"-"`
	Files       []*File `json:"files,omitempty"`

	AddedOn           int64   `json:"added_on,omitempty"`
	AmountLeft        int64   `json:"amount_left"`
	AutoTmm           bool    `json:"auto_tmm"`
	Availability      float64 `json:"availability,omitempty"`
	Category          string  `json:"category,omitempty"`
	Completed         int64   `json:"completed"`
	CompletionOn      int     `json:"completion_on,omitempty"`
	ContentPath       string  `json:"content_path"`
	DlLimit           int     `json:"dl_limit"`
	Dlspeed           int64   `json:"dlspeed"`
	Downloaded        int64   `json:"downloaded"`
	DownloadedSession int64   `json:"downloaded_session"`
	Eta               int     `json:"eta"`
	FlPiecePrio       bool    `json:"f_l_piece_prio,omitempty"`
	ForceStart        bool    `json:"force_start,omitempty"`
	Hash              string  `json:"hash"`
	LastActivity      int64   `json:"last_activity,omitempty"`
	MagnetUri         string  `json:"magnet_uri,omitempty"`
	MaxRatio          int     `json:"max_ratio,omitempty"`
	MaxSeedingTime    int     `json:"max_seeding_time,omitempty"`
	Name              string  `json:"name,omitempty"`
	NumComplete       int     `json:"num_complete,omitempty"`
	NumIncomplete     int     `json:"num_incomplete,omitempty"`
	NumLeechs         int     `json:"num_leechs,omitempty"`
	NumSeeds          int     `json:"num_seeds,omitempty"`
	Priority          int     `json:"priority,omitempty"`
	Progress          float64 `json:"progress"`
	Ratio             int     `json:"ratio,omitempty"`
	RatioLimit        int     `json:"ratio_limit,omitempty"`
	SavePath          string  `json:"save_path"`
	SeedingTimeLimit  int     `json:"seeding_time_limit,omitempty"`
	SeenComplete      int64   `json:"seen_complete,omitempty"`
	SeqDl             bool    `json:"seq_dl"`
	Size              int64   `json:"size,omitempty"`
	State             string  `json:"state,omitempty"`
	SuperSeeding      bool    `json:"super_seeding"`
	Tags              string  `json:"tags,omitempty"`
	TimeActive        int     `json:"time_active,omitempty"`
	TotalSize         int64   `json:"total_size,omitempty"`
	Tracker           string  `json:"tracker,omitempty"`
	UpLimit           int64   `json:"up_limit,omitempty"`
	Uploaded          int64   `json:"uploaded,omitempty"`
	UploadedSession   int64   `json:"uploaded_session,omitempty"`
	Upspeed           int64   `json:"upspeed,omitempty"`
	Source            string  `json:"source,omitempty"`

	sync.Mutex
}

func (t *Torrent) Copy() *Torrent {
	return &Torrent{
		ID:                t.ID,
		DebridID:          t.DebridID,
		Debrid:            t.Debrid,
		TorrentPath:       t.TorrentPath,
		AddedOn:           t.AddedOn,
		AmountLeft:        t.AmountLeft,
		AutoTmm:           t.AutoTmm,
		Availability:      t.Availability,
		Category:          t.Category,
		Completed:         t.Completed,
		CompletionOn:      t.CompletionOn,
		ContentPath:       t.ContentPath,
		DlLimit:           t.DlLimit,
		Dlspeed:           t.Dlspeed,
		Downloaded:        t.Downloaded,
		DownloadedSession: t.DownloadedSession,
		Eta:               t.Eta,
		FlPiecePrio:       t.FlPiecePrio,
		ForceStart:        t.ForceStart,
		Hash:              t.Hash,
		LastActivity:      t.LastActivity,
		MagnetUri:         t.MagnetUri,
		MaxRatio:          t.MaxRatio,
		MaxSeedingTime:    t.MaxSeedingTime,
		Name:              t.Name,
		NumComplete:       t.NumComplete,
		NumIncomplete:     t.NumIncomplete,
		NumLeechs:         t.NumLeechs,
		NumSeeds:          t.NumSeeds,
		Priority:          t.Priority,
		Progress:          t.Progress,
		Ratio:             t.Ratio,
		RatioLimit:        t.RatioLimit,
		SavePath:          t.SavePath,
		SeedingTimeLimit:  t.SeedingTimeLimit,
		SeenComplete:      t.SeenComplete,
		SeqDl:             t.SeqDl,
		Size:              t.Size,
		State:             t.State,
		SuperSeeding:      t.SuperSeeding,
		Tags:              t.Tags,
		TimeActive:        t.TimeActive,
		TotalSize:         t.TotalSize,
		Tracker:           t.Tracker,
		UpLimit:           t.UpLimit,
		Uploaded:          t.Uploaded,
		UploadedSession:   t.UploadedSession,
		Upspeed:           t.Upspeed,
		Source:            t.Source,
	}
}

func (t *Torrent) IsReady() bool {
	return (t.AmountLeft <= 0 || t.Progress == 1) && t.TorrentPath != ""
}

func (t *Torrent) discordContext() string {
	format := `
		**Name:** %s
		**Arr:** %s
		**Hash:** %s
		**MagnetURI:** %s
		**Debrid:** %s
	`
	return fmt.Sprintf(format, t.Name, t.Category, t.Hash, t.MagnetUri, t.Debrid)
}
